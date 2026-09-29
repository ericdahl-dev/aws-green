package poller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ericdahl-dev/aws-green/internal/cfn"
	"github.com/ericdahl-dev/aws-green/internal/config"
	"github.com/ericdahl-dev/aws-green/internal/ecs"
	"github.com/ericdahl-dev/aws-green/internal/health"
	"github.com/ericdahl-dev/aws-green/internal/state"
)

// The poller is exercised through its unexported poll() rather than Start(),
// so every test drives exactly one fetch cycle and never depends on a timer.
// No test here reaches AWS: all three client factories are substituted.

// fakePipelineFetcher records the pipeline names it was asked for and replays
// canned data (or a canned error) instead of calling CodePipeline.
type fakePipelineFetcher struct {
	mu    sync.Mutex
	calls []string
	data  map[string]state.PipelineState
	err   error
}

func (f *fakePipelineFetcher) FetchPipeline(_ context.Context, name string) (state.PipelineState, error) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	err := f.err
	data := f.data[name]
	f.mu.Unlock()
	if err != nil {
		return state.PipelineState{}, err
	}
	return data, nil
}

func (f *fakePipelineFetcher) fetched() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakePipelineFetcher) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

type fakeCFNFetcher struct {
	mu    sync.Mutex
	calls [][]string
	data  []state.StackState
	err   error
}

func (f *fakeCFNFetcher) FetchStacks(_ context.Context, names []string) ([]state.StackState, error) {
	f.mu.Lock()
	f.calls = append(f.calls, names)
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return f.data, nil
}

func (f *fakeCFNFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fakeECSFetcher struct {
	mu       sync.Mutex
	clusters []string
	data     []state.ECSServiceState
	err      error
	// errByCluster fails only the named clusters, so a partial outage can be
	// exercised alongside clusters that still answer.
	errByCluster map[string]error
}

func (f *fakeECSFetcher) FetchServices(_ context.Context, cluster string, _ []string) ([]state.ECSServiceState, error) {
	f.mu.Lock()
	f.clusters = append(f.clusters, cluster)
	err := f.err
	if err == nil {
		err = f.errByCluster[cluster]
	}
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	// Like the real adapter, stamp the cluster onto each service.
	out := make([]state.ECSServiceState, len(f.data))
	for i, sv := range f.data {
		sv.Cluster = cluster
		out[i] = sv
	}
	return out, nil
}

func (f *fakeECSFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.clusters)
}

// gatedFetcher blocks in FetchPipeline until release is closed, so a test can
// observe poller state at a point where a poll cycle is known to be in flight.
type gatedFetcher struct {
	release chan struct{}
}

func (g *gatedFetcher) FetchPipeline(_ context.Context, name string) (state.PipelineState, error) {
	<-g.release
	return state.PipelineState{Name: name}, nil
}

func pipelineFactory(f Fetcher) ClientFactory {
	return func(_, _ string) (Fetcher, error) { return f, nil }
}

func failingPipelineFactory(err error) ClientFactory {
	return func(_, _ string) (Fetcher, error) { return nil, err }
}

func cfnFactory(f cfn.Fetcher) CFNClientFactory {
	return func(_, _ string) (cfn.Fetcher, error) { return f, nil }
}

func ecsFactory(f ecs.Fetcher) ECSClientFactory {
	return func(_, _ string) (ecs.Fetcher, error) { return f, nil }
}

func loadConfig(t *testing.T, content string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// pipelineData builds a PipelineState with one stage per status, which is
// enough for health.Aggregate to produce a predictable stoplight.
func pipelineData(name string, statuses ...health.ExecutionStatus) state.PipelineState {
	stages := make([]state.StageState, len(statuses))
	for i, s := range statuses {
		stages[i] = state.StageState{Name: "stage", Status: s}
	}
	return state.NewPipeline(name, stages)
}

// pollOnce runs a single poll cycle synchronously and returns the snapshot the
// poller published.
func pollOnce(t *testing.T, p *Poller) state.Snapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch := make(chan state.Snapshot, 1)
	p.poll(ctx, ch)
	select {
	case snap := <-ch:
		return snap
	default:
		t.Fatal("poll did not publish a snapshot")
		return state.Snapshot{}
	}
}

func boolPtr(b bool) *bool { return &b }

const twoProjectConfig = `
[settings]
poll_interval_seconds = 30

[[accounts]]
name = "prod"
profile = "prod-profile"
region = "us-east-1"

[[projects]]
name = "alpha"
account = "prod"
[projects.pipeline]
name = "alpha-pipeline"

[[projects]]
name = "beta"
account = "prod"
[projects.pipeline]
name = "beta-pipeline"
`

func TestNewSeedsOnlyEnabledProjects(t *testing.T) {
	cfg := loadConfig(t, `
[[accounts]]
name = "prod"
profile = "prod-profile"
region = "us-east-1"

[[projects]]
name = "alpha"
account = "prod"
enabled = false
[projects.pipeline]
name = "alpha-pipeline"

[[projects]]
name = "beta"
account = "prod"
[projects.pipeline]
name = "beta-pipeline"
`)
	p := New(cfg, pipelineFactory(&fakePipelineFetcher{}), nil, nil)

	snap := p.Snapshot()
	if len(snap.Projects) != 1 {
		t.Fatalf("expected 1 seeded project, got %d", len(snap.Projects))
	}
	if snap.Projects[0].Name != "beta" {
		t.Errorf("expected beta, got %q", snap.Projects[0].Name)
	}
	if snap.Projects[0].Pipeline.Stoplight != health.StoplightGrey {
		t.Errorf("expected seeded state to be grey, got %v", snap.Projects[0].Pipeline.Stoplight)
	}
}

func TestPollSkipsDisabledProjects(t *testing.T) {
	cfg := loadConfig(t, `
[[accounts]]
name = "prod"
profile = "prod-profile"
region = "us-east-1"

[[projects]]
name = "alpha"
account = "prod"
enabled = false
[projects.pipeline]
name = "alpha-pipeline"
[[projects.stacks]]
name = "alpha-stack"
[[projects.ecs]]
cluster = "alpha-cluster"
services = ["web"]

[[projects]]
name = "beta"
account = "prod"
[projects.pipeline]
name = "beta-pipeline"
`)
	pipes := &fakePipelineFetcher{data: map[string]state.PipelineState{
		"beta-pipeline": pipelineData("beta-pipeline", health.StatusSucceeded),
	}}
	stacks := &fakeCFNFetcher{}
	services := &fakeECSFetcher{}

	p := New(cfg, pipelineFactory(pipes), cfnFactory(stacks), ecsFactory(services))
	snap := pollOnce(t, p)

	if len(snap.Projects) != 1 || snap.Projects[0].Name != "beta" {
		t.Fatalf("expected only beta in snapshot, got %+v", snap.Projects)
	}
	// The disabled project must produce no fetches of any kind, not merely be
	// filtered out of the rendered rows.
	got := pipes.fetched()
	if len(got) != 1 || got[0] != "beta-pipeline" {
		t.Errorf("expected only beta-pipeline fetched, got %v", got)
	}
	if stacks.callCount() != 0 {
		t.Errorf("expected no stack fetches for the disabled project, got %d", stacks.callCount())
	}
	if services.callCount() != 0 {
		t.Errorf("expected no ECS fetches for the disabled project, got %d", services.callCount())
	}
}

func TestPollCarriesForwardByNameWhenEnabledSetShifts(t *testing.T) {
	cfg := loadConfig(t, twoProjectConfig)
	pipes := &fakePipelineFetcher{data: map[string]state.PipelineState{
		"alpha-pipeline": pipelineData("alpha-pipeline", health.StatusSucceeded),
		"beta-pipeline":  pipelineData("beta-pipeline", health.StatusFailed),
	}}
	p := New(cfg, pipelineFactory(pipes), nil, nil)

	first := pollOnce(t, p)
	if first.Projects[0].Pipeline.Stoplight != health.StoplightGreen {
		t.Fatalf("expected alpha green, got %v", first.Projects[0].Pipeline.Stoplight)
	}
	if first.Projects[1].Pipeline.Stoplight != health.StoplightRed {
		t.Fatalf("expected beta red, got %v", first.Projects[1].Pipeline.Stoplight)
	}

	// Disable alpha so beta moves from index 1 to index 0, then fail the fetch.
	// Carrying forward by index would hand beta alpha's green state.
	cfg.Projects[0].Enabled = boolPtr(false)
	p.mu.Lock()
	p.cfg = cfg.Clone()
	p.mu.Unlock()
	pipes.setErr(errors.New("codepipeline unavailable"))

	second := pollOnce(t, p)
	if len(second.Projects) != 1 {
		t.Fatalf("expected only beta to remain, got %d projects", len(second.Projects))
	}
	beta := second.Projects[0]
	if beta.Name != "beta" {
		t.Fatalf("expected beta, got %q", beta.Name)
	}
	if beta.Pipeline.Stoplight != health.StoplightRed {
		t.Errorf("expected beta's own red to carry forward, got %v", beta.Pipeline.Stoplight)
	}
	if beta.Pipeline.Name != "beta-pipeline" {
		t.Errorf("expected beta-pipeline carried forward, got %q", beta.Pipeline.Name)
	}
}

func TestPrevPipelineMatchesByAccountAndName(t *testing.T) {
	prev := []state.ProjectState{
		{Name: "alpha", Account: "prod", Pipeline: state.PipelineState{Name: "alpha-pipeline", Stoplight: health.StoplightGreen}},
		{Name: "beta", Account: "prod", Pipeline: state.PipelineState{Name: "beta-pipeline", Stoplight: health.StoplightRed}},
	}

	if got := prevPipeline(prev, "beta", "prod"); got.Name != "beta-pipeline" || got.Stoplight != health.StoplightRed {
		t.Errorf("expected beta's pipeline, got %+v", got)
	}
	if got := prevPipeline(prev, "gamma", "prod"); got.Name != "" || got.Stoplight != health.StoplightGrey {
		t.Errorf("expected zero PipelineState for an unknown project, got %+v", got)
	}
	if got := prevPipeline(nil, "alpha", "prod"); got.Name != "" {
		t.Errorf("expected zero PipelineState for empty previous state, got %+v", got)
	}
}

// The same project name in two accounts is the normal case for this tool, and
// matching on name alone would carry one account's health into the other's row.
func TestPrevPipelineDoesNotCrossAccounts(t *testing.T) {
	prev := []state.ProjectState{
		{Name: "annex-ims", Account: "libnd", Pipeline: state.PipelineState{Name: "libnd-pipeline", Stoplight: health.StoplightRed}},
		{Name: "annex-ims", Account: "testlibnd", Pipeline: state.PipelineState{Name: "test-pipeline", Stoplight: health.StoplightGreen}},
	}

	if got := prevPipeline(prev, "annex-ims", "testlibnd"); got.Name != "test-pipeline" {
		t.Errorf("expected testlibnd's own pipeline, got %+v", got)
	}
	if got := prevPipeline(prev, "annex-ims", "staging"); got.Name != "" {
		t.Errorf("expected nothing for an account with no previous state, got %+v", got)
	}
}

func TestPollMissingClientForAccountReportsError(t *testing.T) {
	cfg := loadConfig(t, `
[[accounts]]
name = "prod"
profile = "prod-profile"
region = "us-east-1"

[[projects]]
name = "alpha"
account = "prod"
[projects.pipeline]
name = "alpha-pipeline"
`)
	// Every client construction fails, so no client is registered for "prod".
	p := New(cfg, failingPipelineFactory(errors.New("no credentials")), nil, nil)

	snap := pollOnce(t, p)
	if len(snap.Projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(snap.Projects))
	}
	pipe := snap.Projects[0].Pipeline
	if pipe.Err == nil {
		t.Fatal("expected an error when no client is available")
	}
	if pipe.Err.Error() != `no client available for account "prod": no credentials` {
		t.Errorf("unexpected error: %v", pipe.Err)
	}
	if !pipe.IsStale() {
		t.Error("expected StaleAt set when no client is available")
	}
}

func TestPollResolvesProfileAndRegionFromAccount(t *testing.T) {
	cfg := loadConfig(t, twoProjectConfig)
	pipes := &fakePipelineFetcher{data: map[string]state.PipelineState{
		"alpha-pipeline": pipelineData("alpha-pipeline", health.StatusSucceeded),
		"beta-pipeline":  pipelineData("beta-pipeline", health.StatusSucceeded),
	}}
	p := New(cfg, pipelineFactory(pipes), nil, nil)

	snap := pollOnce(t, p)
	if snap.Projects[0].Profile != "prod-profile" {
		t.Errorf("expected profile prod-profile, got %q", snap.Projects[0].Profile)
	}
	if snap.Projects[0].Region != "us-east-1" {
		t.Errorf("expected region us-east-1, got %q", snap.Projects[0].Region)
	}
}

func TestPollWithoutAccountUsesDefaultClient(t *testing.T) {
	cfg := loadConfig(t, `
[[projects]]
name = "alpha"
[projects.pipeline]
name = "alpha-pipeline"
`)
	pipes := &fakePipelineFetcher{data: map[string]state.PipelineState{
		"alpha-pipeline": pipelineData("alpha-pipeline", health.StatusSucceeded),
	}}
	p := New(cfg, pipelineFactory(pipes), nil, nil)

	snap := pollOnce(t, p)
	if snap.Projects[0].Pipeline.Stoplight != health.StoplightGreen {
		t.Errorf("expected green via the default client, got %v", snap.Projects[0].Pipeline.Stoplight)
	}
	if snap.Projects[0].Profile != "" || snap.Projects[0].Region != "" {
		t.Errorf("expected empty profile/region for an accountless project, got %q/%q",
			snap.Projects[0].Profile, snap.Projects[0].Region)
	}
}

func TestPollPopulatesStacksAndECSServices(t *testing.T) {
	cfg := loadConfig(t, `
[[accounts]]
name = "prod"
profile = "prod-profile"
region = "us-east-1"

[[projects]]
name = "alpha"
account = "prod"
[projects.pipeline]
name = "alpha-pipeline"
[[projects.stacks]]
name = "alpha-stack"
[[projects.ecs]]
cluster = "alpha-cluster"
services = ["web"]
`)
	pipes := &fakePipelineFetcher{data: map[string]state.PipelineState{
		"alpha-pipeline": pipelineData("alpha-pipeline", health.StatusSucceeded),
	}}
	stacks := &fakeCFNFetcher{data: []state.StackState{
		{Name: "alpha-stack", Status: "UPDATE_ROLLBACK_COMPLETE", Stoplight: health.StoplightRed},
	}}
	services := &fakeECSFetcher{data: []state.ECSServiceState{
		{Name: "web", RunningCount: 2, DesiredCount: 2, Stoplight: health.StoplightGreen},
	}}
	p := New(cfg, pipelineFactory(pipes), cfnFactory(stacks), ecsFactory(services))

	snap := pollOnce(t, p)
	proj := snap.Projects[0]
	if len(proj.Stacks) != 1 || proj.Stacks[0].Name != "alpha-stack" {
		t.Fatalf("expected alpha-stack, got %+v", proj.Stacks)
	}
	if len(proj.ECSServices) != 1 || proj.ECSServices[0].Cluster != "alpha-cluster" {
		t.Fatalf("expected the cluster stamped onto the service, got %+v", proj.ECSServices)
	}
	// A red stack outranks a green pipeline and a green service.
	if proj.Stoplight() != health.StoplightRed {
		t.Errorf("expected the project to take the worst stoplight, got %v", proj.Stoplight())
	}
}

func TestReloadConfigRebuildsCurrentFromNewEnabledSet(t *testing.T) {
	cfg := loadConfig(t, twoProjectConfig)
	pipes := &fakePipelineFetcher{data: map[string]state.PipelineState{
		"alpha-pipeline": pipelineData("alpha-pipeline", health.StatusSucceeded),
		"beta-pipeline":  pipelineData("beta-pipeline", health.StatusSucceeded),
	}}
	p := New(cfg, pipelineFactory(pipes), nil, nil)

	if snap := pollOnce(t, p); len(snap.Projects) != 2 {
		t.Fatalf("expected 2 projects before reload, got %d", len(snap.Projects))
	}

	// Swap in a config with a different, single enabled project and swap the
	// factory for one that blocks, so the reload's background poll cannot race
	// ahead and overwrite the state we are asserting on.
	gate := &gatedFetcher{release: make(chan struct{})}
	defer close(gate.release)
	p.factory = pipelineFactory(gate)

	newCfg := loadConfig(t, `
[[accounts]]
name = "staging"
profile = "staging-profile"
region = "eu-west-1"

[[projects]]
name = "gamma"
account = "staging"
[projects.pipeline]
name = "gamma-pipeline"

[[projects]]
name = "delta"
account = "staging"
enabled = false
[projects.pipeline]
name = "delta-pipeline"
`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan state.Snapshot, 1)
	p.ReloadConfig(newCfg, ctx, ch)

	snap := p.Snapshot()
	if len(snap.Projects) != 1 {
		t.Fatalf("expected 1 project after reload, got %d", len(snap.Projects))
	}
	got := snap.Projects[0]
	if got.Name != "gamma" {
		t.Errorf("expected gamma, got %q", got.Name)
	}
	if got.Account != "staging" {
		t.Errorf("expected account staging, got %q", got.Account)
	}
	if got.Pipeline.Name != "gamma-pipeline" {
		t.Errorf("expected gamma-pipeline, got %q", got.Pipeline.Name)
	}
	// gamma is new to the config, so there is nothing to carry and grey is
	// the honest starting colour.
	if got.Pipeline.Stoplight != health.StoplightGrey {
		t.Errorf("expected grey for a project new to the config, got %v", got.Pipeline.Stoplight)
	}
}

// Editing config must not blank the health of every project that the edit did
// not touch.
func TestReloadConfigCarriesKnownProjectsForward(t *testing.T) {
	cfg := loadConfig(t, twoProjectConfig)
	pipes := &fakePipelineFetcher{data: map[string]state.PipelineState{
		"alpha-pipeline": pipelineData("alpha-pipeline", health.StatusSucceeded),
		"beta-pipeline":  pipelineData("beta-pipeline", health.StatusFailed),
	}}
	p := New(cfg, pipelineFactory(pipes), nil, nil)
	if snap := pollOnce(t, p); len(snap.Projects) != 2 {
		t.Fatalf("expected 2 projects before reload, got %d", len(snap.Projects))
	}

	// Block the reload's background poll so the assertions see the state the
	// reload itself produced.
	gate := &gatedFetcher{release: make(chan struct{})}
	defer close(gate.release)
	p.factory = pipelineFactory(gate)

	// Same two projects, plus a third.
	newCfg := loadConfig(t, twoProjectConfig+`
[[projects]]
name = "gamma"
account = "prod"
[projects.pipeline]
name = "gamma-pipeline"
`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.ReloadConfig(newCfg, ctx, make(chan state.Snapshot, 1))

	byName := map[string]state.ProjectState{}
	for _, proj := range p.Snapshot().Projects {
		byName[proj.Name] = proj
	}
	if got := byName["alpha"].Pipeline.Stoplight; got != health.StoplightGreen {
		t.Errorf("expected alpha's green to survive the reload, got %v", got)
	}
	if got := byName["beta"].Pipeline.Stoplight; got != health.StoplightRed {
		t.Errorf("expected beta's red to survive the reload, got %v", got)
	}
	if got := byName["gamma"].Pipeline.Stoplight; got != health.StoplightGrey {
		t.Errorf("expected the new project to start grey, got %v", got)
	}
}

// Carrying state forward must not outlive the edit that invalidated it: a
// renamed pipeline, a deleted stack, and a deleted service all have to
// disappear immediately rather than linger until the next poll.
func TestReloadConfigDropsStateInvalidatedByTheEdit(t *testing.T) {
	cfg := loadConfig(t, `
[[accounts]]
name = "prod"
profile = "prod-profile"
region = "us-east-1"

[[projects]]
name = "alpha"
account = "prod"
[projects.pipeline]
name = "alpha-pipeline"
[[projects.stacks]]
name = "keep-stack"
[[projects.stacks]]
name = "drop-stack"
[[projects.ecs]]
cluster = "alpha-cluster"
services = ["keep-svc", "drop-svc"]
`)
	pipes := &fakePipelineFetcher{data: map[string]state.PipelineState{
		"alpha-pipeline": pipelineData("alpha-pipeline", health.StatusSucceeded),
	}}
	stacks := &fakeCFNFetcher{data: []state.StackState{
		{Name: "keep-stack", Status: "UPDATE_COMPLETE", Stoplight: health.StoplightGreen},
		{Name: "drop-stack", Status: "CREATE_FAILED", Stoplight: health.StoplightRed},
	}}
	services := &fakeECSFetcher{data: []state.ECSServiceState{
		{Name: "keep-svc", RunningCount: 1, DesiredCount: 1, Stoplight: health.StoplightGreen},
		{Name: "drop-svc", RunningCount: 0, DesiredCount: 1, Stoplight: health.StoplightRed},
	}}
	p := New(cfg, pipelineFactory(pipes), cfnFactory(stacks), ecsFactory(services))
	if first := pollOnce(t, p).Projects[0]; len(first.Stacks) != 2 || len(first.ECSServices) != 2 {
		t.Fatalf("expected both stacks and both services first, got %+v", first)
	}

	gate := &gatedFetcher{release: make(chan struct{})}
	defer close(gate.release)
	p.factory = pipelineFactory(gate)

	newCfg := loadConfig(t, `
[[accounts]]
name = "prod"
profile = "prod-profile"
region = "us-east-1"

[[projects]]
name = "alpha"
account = "prod"
[projects.pipeline]
name = "renamed-pipeline"
[[projects.stacks]]
name = "keep-stack"
[[projects.ecs]]
cluster = "alpha-cluster"
services = ["keep-svc"]
`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.ReloadConfig(newCfg, ctx, make(chan state.Snapshot, 1))

	got := p.Snapshot().Projects[0]
	if got.Pipeline.Name != "renamed-pipeline" || len(got.Pipeline.Stages) != 0 {
		t.Errorf("expected the renamed pipeline to start over, got %+v", got.Pipeline)
	}
	if got.Pipeline.Stoplight != health.StoplightGrey {
		t.Errorf("expected the renamed pipeline grey, got %v", got.Pipeline.Stoplight)
	}
	if len(got.Stacks) != 1 || got.Stacks[0].Name != "keep-stack" {
		t.Errorf("expected only the still-configured stack, got %+v", got.Stacks)
	}
	if len(got.ECSServices) != 1 || got.ECSServices[0].Name != "keep-svc" {
		t.Errorf("expected only the still-configured service, got %+v", got.ECSServices)
	}
	// The deleted red stack and service must not still be dragging the
	// project's stoplight down.
	if got.Stoplight() != health.StoplightGreen {
		t.Errorf("expected the deleted red resources gone from the aggregate, got %v", got.Stoplight())
	}
}

func TestStartPollsImmediatelyAndStops(t *testing.T) {
	cfg := loadConfig(t, twoProjectConfig)
	cfg.Settings.PollInterval = 1
	pipes := &fakePipelineFetcher{data: map[string]state.PipelineState{
		"alpha-pipeline": pipelineData("alpha-pipeline", health.StatusSucceeded),
		"beta-pipeline":  pipelineData("beta-pipeline", health.StatusInProgress),
	}}
	p := New(cfg, pipelineFactory(pipes), nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, stop := p.Start(ctx)

	select {
	case snap := <-ch:
		if len(snap.Projects) != 2 {
			t.Fatalf("expected 2 projects, got %d", len(snap.Projects))
		}
		if snap.Projects[0].Pipeline.Stoplight != health.StoplightGreen {
			t.Errorf("expected alpha green, got %v", snap.Projects[0].Pipeline.Stoplight)
		}
		if snap.Projects[1].Pipeline.Stoplight != health.StoplightYellow {
			t.Errorf("expected beta yellow, got %v", snap.Projects[1].Pipeline.Stoplight)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for the first snapshot")
	}

	stop()
	// Stopping closes the channel once the polling goroutine returns.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("channel was not closed after stop")
		}
	}
}

// The manage screen edits its Config in place on the UI goroutine. The poller
// must work from its own copy, changed only through ReloadConfig, or a poll
// reads the projects while they're being rewritten.
func TestPollerOwnsItsConfig(t *testing.T) {
	cfg := loadConfig(t, twoProjectConfig)
	pipes := &fakePipelineFetcher{}
	p := New(cfg, pipelineFactory(pipes), nil, nil)

	cfg.Projects[0].Pipeline.Name = "edited-pipeline"
	cfg.Projects = append(cfg.Projects, config.Project{Name: "gamma", Pipeline: config.Pipeline{Name: "gamma-pipeline"}})
	pollOnce(t, p)

	got := pipes.fetched()
	if len(got) != 2 || got[0] != "alpha-pipeline" || got[1] != "beta-pipeline" {
		t.Errorf("poll saw an edit made outside ReloadConfig: fetched %v", got)
	}
}
