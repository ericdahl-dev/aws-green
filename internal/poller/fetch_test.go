package poller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ericdahl-dev/aws-green/internal/config"
	"github.com/ericdahl-dev/aws-green/internal/health"
	"github.com/ericdahl-dev/aws-green/internal/state"
)

// fetchProject is the whole per-project rule set of a poll cycle — fetch,
// or carry the last known state forward marked stale — so it is tested with
// plain values: no config file, no factories, no channel.

var cycleTime = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

func appProject() config.Project {
	return config.Project{
		Name:     "app",
		Account:  "prod",
		Pipeline: config.Pipeline{Name: "app-pipeline"},
		Stacks:   []config.Stack{{Name: "app-stack"}},
		ECS:      []config.ECSConfig{{Cluster: "c1", Services: []string{"web"}}},
	}
}

func healthyApp() state.ProjectState {
	return state.ProjectState{
		Name:     "app",
		Account:  "prod",
		Pipeline: state.PipelineState{Account: "prod", Name: "app-pipeline", Stoplight: health.StoplightGreen},
		Stacks:   []state.StackState{{Name: "app-stack", Status: "UPDATE_COMPLETE", Stoplight: health.StoplightGreen}},
		ECSServices: []state.ECSServiceState{
			{Name: "web", Cluster: "c1", RunningCount: 1, DesiredCount: 1, Stoplight: health.StoplightGreen},
		},
	}
}

// Every failed fetch keeps what was on screen and stamps it stale at the
// cycle's time — one clock, so stale ages and stuck alerts agree.
func TestFetchProjectCarriesEveryKindForwardOnError(t *testing.T) {
	boom := errors.New("sso token expired")
	clients := accountClients{
		pipeline: &fakePipelineFetcher{err: boom},
		cfn:      &fakeCFNFetcher{err: boom},
		ecs:      &fakeECSFetcher{err: boom},
	}

	got := fetchProject(context.Background(), appProject(), clients, healthyApp(), cycleTime)

	if got.Pipeline.Stoplight != health.StoplightGreen || got.Pipeline.Err != boom {
		t.Errorf("pipeline not carried forward: %+v", got.Pipeline)
	}
	if got.StacksStoplight() != health.StoplightGreen || got.StacksFetch.Err != boom {
		t.Errorf("stacks not carried forward: %+v / %+v", got.Stacks, got.StacksFetch)
	}
	if got.ECSStoplight() != health.StoplightGreen || got.ECSFetch.Err != boom {
		t.Errorf("services not carried forward: %+v / %+v", got.ECSServices, got.ECSFetch)
	}
	for name, at := range map[string]*time.Time{
		"pipeline": got.Pipeline.StaleAt, "stacks": got.StacksFetch.StaleAt, "ecs": got.ECSFetch.StaleAt,
	} {
		if at == nil || !at.Equal(cycleTime) {
			t.Errorf("%s StaleAt = %v, want the cycle time %v", name, at, cycleTime)
		}
	}
}

// A kind whose client couldn't be built is reported as its own fetch error,
// with the reason, rather than silently showing nothing.
func TestFetchProjectReportsMissingClients(t *testing.T) {
	clients := accountClients{
		pipelineErr: errors.New("profile not found"),
		cfnErr:      errors.New("profile not found"),
		ecsErr:      errors.New("profile not found"),
	}

	got := fetchProject(context.Background(), appProject(), clients, healthyApp(), cycleTime)

	for name, err := range map[string]error{
		"pipeline": got.Pipeline.Err, "stacks": got.StacksFetch.Err, "ecs": got.ECSFetch.Err,
	} {
		if err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if got.Pipeline.Err.Error() != `no client available for account "prod": profile not found` {
		t.Errorf("pipeline error = %q", got.Pipeline.Err)
	}
}

// With no stack or ECS support wired up at all there is nothing to report,
// not an error.
func TestFetchProjectWithoutStackOrECSSupport(t *testing.T) {
	clients := accountClients{pipeline: &fakePipelineFetcher{}}

	got := fetchProject(context.Background(), appProject(), clients, state.ProjectState{}, cycleTime)

	if got.StacksFetch.Err != nil || got.ECSFetch.Err != nil {
		t.Errorf("expected no fetch errors, got %v / %v", got.StacksFetch.Err, got.ECSFetch.Err)
	}
}

// Fresh data replaces what was there, and clears staleness.
func TestFetchProjectUsesFreshData(t *testing.T) {
	prev := healthyApp()
	prev.Pipeline.FetchStatus = state.FetchStatus{StaleAt: &cycleTime, Err: errors.New("old")}
	prev.StacksFetch = state.FetchStatus{StaleAt: &cycleTime, Err: errors.New("old")}
	clients := accountClients{
		pipeline: &fakePipelineFetcher{},
		cfn:      &fakeCFNFetcher{data: []state.StackState{{Name: "app-stack", Status: "UPDATE_FAILED", Stoplight: health.StoplightRed}}},
		ecs:      &fakeECSFetcher{data: []state.ECSServiceState{{Name: "web", RunningCount: 0, DesiredCount: 1, Stoplight: health.StoplightRed}}},
	}

	got := fetchProject(context.Background(), appProject(), clients, prev, cycleTime)

	if got.Pipeline.IsStale() {
		t.Errorf("pipeline still stale after a good fetch: %+v", got.Pipeline)
	}
	if got.StacksFetch.IsStale() || got.StacksStoplight() != health.StoplightRed {
		t.Errorf("stacks not refreshed: %+v / %+v", got.Stacks, got.StacksFetch)
	}
	if got.ECSServices[0].Cluster != "c1" || got.ECSStoplight() != health.StoplightRed {
		t.Errorf("services not refreshed: %+v", got.ECSServices)
	}
}

// One failing cluster must not blank the clusters that answered.
func TestFetchProjectKeepsHealthyClusterWhenAnotherFails(t *testing.T) {
	proj := appProject()
	proj.ECS = []config.ECSConfig{
		{Cluster: "good", Services: []string{"web"}},
		{Cluster: "bad", Services: []string{"worker"}},
	}
	prev := state.ProjectState{ECSServices: []state.ECSServiceState{
		{Name: "web", Cluster: "good", Stoplight: health.StoplightRed},
		{Name: "worker", Cluster: "bad", Stoplight: health.StoplightGreen},
	}}
	clients := accountClients{
		pipeline: &fakePipelineFetcher{},
		ecs: &fakeECSFetcher{
			data:         []state.ECSServiceState{{Name: "web", RunningCount: 1, DesiredCount: 1, Stoplight: health.StoplightGreen}},
			errByCluster: map[string]error{"bad": errors.New("cluster gone")},
		},
	}

	got := fetchProject(context.Background(), proj, clients, prev, cycleTime)

	if len(got.ECSServices) != 2 {
		t.Fatalf("expected the fresh cluster plus the carried-forward one, got %+v", got.ECSServices)
	}
	if web := got.ECSServices[0]; web.Cluster != "good" || web.Stoplight != health.StoplightGreen {
		t.Errorf("expected fresh good-cluster service, got %+v", web)
	}
	if worker := got.ECSServices[1]; worker.Cluster != "bad" || worker.Name != "worker" {
		t.Errorf("expected carried-forward bad-cluster service, got %+v", worker)
	}
	if got.ECSFetch.Err == nil {
		t.Error("expected the partial failure recorded on the project")
	}
}

// On the first cycle there is nothing to carry forward, but the failure is
// still recorded, so the dashboard says so rather than rendering a blank that
// looks like "nothing configured".
func TestFetchProjectRecordsFailureWithNothingToCarry(t *testing.T) {
	boom := errors.New("down")
	clients := accountClients{pipeline: &fakePipelineFetcher{}, cfn: &fakeCFNFetcher{err: boom}, ecs: &fakeECSFetcher{err: boom}}

	got := fetchProject(context.Background(), appProject(), clients, state.ProjectState{}, cycleTime)

	if len(got.Stacks) != 0 || len(got.ECSServices) != 0 {
		t.Errorf("expected nothing shown, got %+v / %+v", got.Stacks, got.ECSServices)
	}
	if !got.StacksFetch.IsStale() || !got.ECSFetch.IsStale() {
		t.Errorf("expected both failures recorded, got %+v / %+v", got.StacksFetch, got.ECSFetch)
	}
}

// A project with only stacks gets a gray pipeline placeholder and no
// pipeline call.
func TestFetchProjectWithoutPipeline(t *testing.T) {
	proj := appProject()
	proj.Pipeline = config.Pipeline{}
	pipes := &fakePipelineFetcher{}

	got := fetchProject(context.Background(), proj, accountClients{pipeline: pipes}, healthyApp(), cycleTime)

	if len(pipes.fetched()) != 0 {
		t.Errorf("expected no pipeline fetch, got %v", pipes.fetched())
	}
	if got.Pipeline.Stoplight != health.StoplightGray || got.Pipeline.Account != "prod" {
		t.Errorf("expected a gray placeholder for prod, got %+v", got.Pipeline)
	}
}
