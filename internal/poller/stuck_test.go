package poller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ericdahl-dev/aws-green/internal/aggregator"
	awsclient "github.com/ericdahl-dev/aws-green/internal/aws"
	"github.com/ericdahl-dev/aws-green/internal/config"
	"github.com/ericdahl-dev/aws-green/internal/state"
	"github.com/ericdahl-dev/aws-green/internal/webhooks"
)

// stuckClock is a hand-cranked clock so threshold crossings can be exercised
// without waiting on the wall clock.
type stuckClock struct {
	t time.Time
}

func (c *stuckClock) now() time.Time          { return c.t }
func (c *stuckClock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newStuckClock() *stuckClock              { return &stuckClock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)} }
func stuckFailingFactory(string, string) (Fetcher, error) {
	return nil, errors.New("no client")
}

// newStuckPoller builds a Poller wired to a fake clock, with no AWS clients.
func newStuckPoller(t *testing.T, cfg *config.Config, clock *stuckClock) *Poller {
	t.Helper()
	p := New(cfg, stuckFailingFactory, nil, nil)
	p.now = clock.now
	return p
}

func stuckConfig(thresholdMinutes int, hooks ...config.Webhook) *config.Config {
	return &config.Config{
		Settings: config.Settings{
			PollInterval:          30,
			StuckThresholdMinutes: thresholdMinutes,
		},
		Webhooks: hooks,
	}
}

func stuckPipelineProject(name, account string, status aggregator.ExecutionStatus) state.ProjectState {
	return state.ProjectState{
		Name:    name,
		Account: account,
		Pipeline: state.PipelineState{
			Account: account,
			Name:    name + "-pipeline",
			Stages: []state.StageState{
				{Name: "Source", Status: aggregator.StatusSucceeded},
				{Name: "Deploy", Status: status},
			},
		},
	}
}

func stuckReasons(events []webhooks.Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Reason
	}
	return out
}

func TestStuckPipelineFiresOnceAfterThreshold(t *testing.T) {
	clock := newStuckClock()
	p := newStuckPoller(t, stuckConfig(30), clock)
	projects := []state.ProjectState{stuckPipelineProject("my-app", "prod", aggregator.StatusInProgress)}

	// First sighting starts the clock but must not alert.
	if events := p.evaluateStuck(projects); len(events) != 0 {
		t.Fatalf("expected no events on first detection, got %v", stuckReasons(events))
	}

	// Still inside the threshold.
	clock.advance(29 * time.Minute)
	if events := p.evaluateStuck(projects); len(events) != 0 {
		t.Fatalf("expected no events before threshold, got %v", stuckReasons(events))
	}

	clock.advance(2 * time.Minute)
	events := p.evaluateStuck(projects)
	if len(events) != 1 {
		t.Fatalf("expected 1 event at threshold, got %v", stuckReasons(events))
	}
	evt := events[0]
	if evt.Event != "pipeline_stuck" || evt.Reason != "pipeline_in_progress" {
		t.Errorf("unexpected event %+v", evt)
	}
	if evt.Resource != "my-app-pipeline" || evt.Account != "prod" || evt.Project != "my-app" {
		t.Errorf("unexpected identity in event %+v", evt)
	}
	if evt.Detail != "stage Deploy" || evt.Status != string(aggregator.StatusInProgress) {
		t.Errorf("unexpected detail in event %+v", evt)
	}
	// StuckSince is when it was first seen stuck, not when it alerted.
	if want := clock.now().Add(-31 * time.Minute); !evt.StuckSince.Equal(want) {
		t.Errorf("stuck_since: got %v, want %v", evt.StuckSince, want)
	}

	// Still stuck on later cycles: must not fire again.
	for i := 0; i < 3; i++ {
		clock.advance(30 * time.Minute)
		if events := p.evaluateStuck(projects); len(events) != 0 {
			t.Fatalf("cycle %d: expected no repeat event, got %v", i, stuckReasons(events))
		}
	}
}

func TestStuckRecoveryAllowsAlertingAgain(t *testing.T) {
	clock := newStuckClock()
	p := newStuckPoller(t, stuckConfig(30), clock)
	bad := []state.ProjectState{stuckPipelineProject("my-app", "prod", aggregator.StatusFailed)}
	good := []state.ProjectState{stuckPipelineProject("my-app", "prod", aggregator.StatusSucceeded)}

	p.evaluateStuck(bad)
	clock.advance(31 * time.Minute)
	if events := p.evaluateStuck(bad); len(events) != 1 {
		t.Fatalf("expected first alert, got %v", stuckReasons(events))
	}

	// Recovery clears the bookkeeping.
	clock.advance(time.Minute)
	if events := p.evaluateStuck(good); len(events) != 0 {
		t.Fatalf("expected no events once healthy, got %v", stuckReasons(events))
	}
	if len(p.stuck) != 0 {
		t.Fatalf("expected stuck entries to be pruned, got %d", len(p.stuck))
	}

	// Going bad again re-arms the alert.
	clock.advance(time.Minute)
	p.evaluateStuck(bad)
	clock.advance(31 * time.Minute)
	if events := p.evaluateStuck(bad); len(events) != 1 {
		t.Fatalf("expected a second alert after recovery, got %v", stuckReasons(events))
	}
}

func TestStuckReasonChangeRestartsClock(t *testing.T) {
	clock := newStuckClock()
	p := newStuckPoller(t, stuckConfig(30), clock)
	running := []state.ProjectState{stuckPipelineProject("my-app", "prod", aggregator.StatusInProgress)}
	failed := []state.ProjectState{stuckPipelineProject("my-app", "prod", aggregator.StatusFailed)}

	p.evaluateStuck(running)
	clock.advance(31 * time.Minute)
	if events := p.evaluateStuck(running); len(events) != 1 {
		t.Fatalf("expected in-progress alert, got %v", stuckReasons(events))
	}

	// The in-progress deploy fails: a new condition, so the clock restarts
	// and a fresh alert is allowed once it too outlives the threshold.
	clock.advance(time.Minute)
	if events := p.evaluateStuck(failed); len(events) != 0 {
		t.Fatalf("expected no immediate alert on reason change, got %v", stuckReasons(events))
	}
	clock.advance(31 * time.Minute)
	events := p.evaluateStuck(failed)
	if len(events) != 1 || events[0].Reason != "pipeline_failed" {
		t.Fatalf("expected pipeline_failed alert, got %v", stuckReasons(events))
	}
}

func TestStuckKeysAreAccountQualified(t *testing.T) {
	clock := newStuckClock()
	p := newStuckPoller(t, stuckConfig(30), clock)
	// Same project name in two accounts — a very common AWS layout.
	both := []state.ProjectState{
		stuckPipelineProject("my-app", "prod", aggregator.StatusFailed),
		stuckPipelineProject("my-app", "staging", aggregator.StatusFailed),
	}

	p.evaluateStuck(both)
	if len(p.stuck) != 2 {
		t.Fatalf("expected 2 tracked resources, got %d", len(p.stuck))
	}

	clock.advance(31 * time.Minute)
	events := p.evaluateStuck(both)
	if len(events) != 2 {
		t.Fatalf("expected an alert per account, got %d", len(events))
	}
	accounts := map[string]bool{events[0].Account: true, events[1].Account: true}
	if !accounts["prod"] || !accounts["staging"] {
		t.Errorf("expected one alert per account, got %v", accounts)
	}

	// One account recovering must not clear or re-arm the other.
	clock.advance(time.Minute)
	mixed := []state.ProjectState{
		stuckPipelineProject("my-app", "prod", aggregator.StatusSucceeded),
		stuckPipelineProject("my-app", "staging", aggregator.StatusFailed),
	}
	if events := p.evaluateStuck(mixed); len(events) != 0 {
		t.Fatalf("expected no events, got %v", stuckReasons(events))
	}
	if len(p.stuck) != 1 {
		t.Fatalf("expected 1 tracked resource after recovery, got %d", len(p.stuck))
	}
	if _, ok := p.stuck[stuckKey(mixed[1], webhooks.ResourceStack, "my-app-pipeline")]; ok {
		t.Error("stack and pipeline keys must not collide")
	}
}

func TestStuckPipelineErrorIsNotStuck(t *testing.T) {
	clock := newStuckClock()
	p := newStuckPoller(t, stuckConfig(0), clock)
	projects := []state.ProjectState{stuckPipelineProject("my-app", "prod", aggregator.StatusFailed)}
	projects[0].Pipeline.Err = errors.New("expired credentials")

	if events := p.evaluateStuck(projects); len(events) != 0 {
		t.Fatalf("expected no alert for an errored fetch, got %v", stuckReasons(events))
	}
}

func TestStuckStackStatuses(t *testing.T) {
	cases := []struct {
		status string
		stuck  bool
		reason string
	}{
		{"CREATE_COMPLETE", false, ""},
		{"UPDATE_COMPLETE", false, ""},
		{"UPDATE_IN_PROGRESS", true, "stack_in_progress"},
		{"CREATE_IN_PROGRESS", true, "stack_in_progress"},
		{"UPDATE_ROLLBACK_IN_PROGRESS", true, "stack_in_progress"},
		{"CREATE_FAILED", true, "stack_failed"},
		{"UPDATE_ROLLBACK_FAILED", true, "stack_failed"},
		{"DELETE_FAILED", true, "stack_failed"},
	}
	for _, tc := range cases {
		stuck, reason := stackStuckReason(state.StackState{Name: "s", Status: tc.status})
		if stuck != tc.stuck || reason != tc.reason {
			t.Errorf("%s: got (%v, %q), want (%v, %q)", tc.status, stuck, reason, tc.stuck, tc.reason)
		}
	}
}

func TestStuckStackAndECSAlertOnce(t *testing.T) {
	clock := newStuckClock()
	p := newStuckPoller(t, stuckConfig(30), clock)
	projects := []state.ProjectState{{
		Name:    "my-app",
		Account: "prod",
		Region:  "us-east-1",
		Stacks: []state.StackState{
			{Name: "healthy-stack", Status: "UPDATE_COMPLETE"},
			{Name: "wedged-stack", Status: "UPDATE_IN_PROGRESS"},
		},
		ECSServices: []state.ECSServiceState{
			{Name: "web", Cluster: "app-cluster", RunningCount: 1, DesiredCount: 3},
			{Name: "worker", Cluster: "app-cluster", RunningCount: 2, DesiredCount: 2},
		},
	}}

	if events := p.evaluateStuck(projects); len(events) != 0 {
		t.Fatalf("expected no events on first detection, got %v", stuckReasons(events))
	}

	clock.advance(31 * time.Minute)
	events := p.evaluateStuck(projects)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %v", stuckReasons(events))
	}

	byType := map[string]webhooks.Event{}
	for _, e := range events {
		byType[e.ResourceType] = e
	}
	stack, ok := byType[webhooks.ResourceStack]
	if !ok {
		t.Fatal("expected a stack event")
	}
	if stack.Event != "stack_stuck" || stack.Reason != "stack_in_progress" || stack.Resource != "wedged-stack" {
		t.Errorf("unexpected stack event %+v", stack)
	}
	if stack.Status != "UPDATE_IN_PROGRESS" || stack.Region != "us-east-1" {
		t.Errorf("unexpected stack event detail %+v", stack)
	}

	svc, ok := byType[webhooks.ResourceECSService]
	if !ok {
		t.Fatal("expected an ECS event")
	}
	if svc.Event != "ecs_service_stuck" || svc.Reason != "ecs_count_mismatch" || svc.Resource != "web" {
		t.Errorf("unexpected ECS event %+v", svc)
	}
	if svc.Cluster != "app-cluster" || svc.Detail != "1 running / 3 desired (0 pending)" {
		t.Errorf("unexpected ECS event detail %+v", svc)
	}

	// Nothing repeats while the conditions persist.
	clock.advance(time.Hour)
	if events := p.evaluateStuck(projects); len(events) != 0 {
		t.Fatalf("expected no repeat events, got %v", stuckReasons(events))
	}
}

func TestStuckECSOverProvisionedCounts(t *testing.T) {
	// A service running more tasks than desired has not converged either.
	if stuck, reason := ecsStuckReason(state.ECSServiceState{RunningCount: 4, DesiredCount: 3}); !stuck || reason != "ecs_count_mismatch" {
		t.Errorf("got (%v, %q)", stuck, reason)
	}
	if stuck, _ := ecsStuckReason(state.ECSServiceState{RunningCount: 0, DesiredCount: 0}); stuck {
		t.Error("a scaled-to-zero service is not stuck")
	}
}

// stuckFakeFetcher returns a canned pipeline for the poll end-to-end test.
type stuckFakeFetcher struct {
	status aggregator.ExecutionStatus
}

func (f *stuckFakeFetcher) FetchPipeline(_ context.Context, name string) (awsclient.PipelineData, error) {
	return awsclient.PipelineData{
		Name: name,
		Stages: []awsclient.StageState{
			{Name: "Deploy", Status: f.status},
		},
	}, nil
}

func TestStuckPollDispatchesToWebhook(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	clock := newStuckClock()
	cfg := stuckConfig(30, config.Webhook{URL: srv.URL})
	cfg.Projects = []config.Project{{
		Name:     "my-app",
		Pipeline: config.Pipeline{Name: "my-app-pipeline"},
	}}

	fetcher := &stuckFakeFetcher{status: aggregator.StatusFailed}
	p := New(cfg, func(string, string) (Fetcher, error) { return fetcher, nil }, nil, nil)
	p.now = clock.now

	ch := make(chan state.Snapshot, 4)
	ctx := context.Background()

	p.poll(ctx, ch)
	mu.Lock()
	got := len(bodies)
	mu.Unlock()
	if got != 0 {
		t.Fatalf("expected no POST before the threshold, got %d", got)
	}

	clock.advance(31 * time.Minute)
	p.poll(ctx, ch)
	p.poll(ctx, ch)

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("expected exactly 1 POST, got %d", len(bodies))
	}
	var evt webhooks.Event
	if err := json.Unmarshal(bodies[0], &evt); err != nil {
		t.Fatalf("decoding payload: %v", err)
	}
	if evt.Event != "pipeline_stuck" || evt.Reason != "pipeline_failed" {
		t.Errorf("unexpected event %+v", evt)
	}
	if evt.Resource != "my-app-pipeline" {
		t.Errorf("unexpected resource %q", evt.Resource)
	}
}
