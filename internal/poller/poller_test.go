package poller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ericdahl-dev/aws-green/internal/aggregator"
	awsclient "github.com/ericdahl-dev/aws-green/internal/aws"
	"github.com/ericdahl-dev/aws-green/internal/cfn"
	"github.com/ericdahl-dev/aws-green/internal/config"
	"github.com/ericdahl-dev/aws-green/internal/ecs"
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
	data  map[string]awsclient.PipelineData
	err   error
}

func (f *fakePipelineFetcher) FetchPipeline(_ context.Context, name string) (awsclient.PipelineData, error) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	err := f.err
	data := f.data[name]
	f.mu.Unlock()
	if err != nil {
		return awsclient.PipelineData{}, err
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
	data  []cfn.StackData
	err   error
}

func (f *fakeCFNFetcher) FetchStacks(_ context.Context, names []string) ([]cfn.StackData, error) {
	f.mu.Lock()
	f.calls = append(f.calls, names)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
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
	data     []ecs.ServiceData
	err      error
}

func (f *fakeECSFetcher) FetchServices(_ context.Context, cluster string, _ []string) ([]ecs.ServiceData, error) {
	f.mu.Lock()
	f.clusters = append(f.clusters, cluster)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.data, nil
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

func (g *gatedFetcher) FetchPipeline(_ context.Context, name string) (awsclient.PipelineData, error) {
	<-g.release
	return awsclient.PipelineData{Name: name}, nil
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

// pipelineData builds a PipelineData with one stage per status, which is
// enough for the aggregator to produce a predictable stoplight.
func pipelineData(name string, statuses ...aggregator.ExecutionStatus) awsclient.PipelineData {
	stages := make([]awsclient.StageState, len(statuses))
	for i, s := range statuses {
		stages[i] = awsclient.StageState{Name: "stage", Status: s}
	}
	return awsclient.PipelineData{Name: name, Stages: stages}
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
	if snap.Projects[0].Pipeline.Stoplight != aggregator.StoplightGrey {
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
	pipes := &fakePipelineFetcher{data: map[string]awsclient.PipelineData{
		"beta-pipeline": pipelineData("beta-pipeline", aggregator.StatusSucceeded),
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

func TestPollCarriesForwardPipelineStateOnFetchError(t *testing.T) {
	cfg := loadConfig(t, twoProjectConfig)
	pipes := &fakePipelineFetcher{data: map[string]awsclient.PipelineData{
		"alpha-pipeline": pipelineData("alpha-pipeline", aggregator.StatusSucceeded),
		"beta-pipeline":  pipelineData("beta-pipeline", aggregator.StatusFailed),
	}}
	p := New(cfg, pipelineFactory(pipes), nil, nil)

	first := pollOnce(t, p)
	if first.Projects[0].Pipeline.Stoplight != aggregator.StoplightGreen {
		t.Fatalf("expected alpha green on first poll, got %v", first.Projects[0].Pipeline.Stoplight)
	}
	if first.Projects[0].Pipeline.IsStale() {
		t.Fatal("expected a successful fetch to be fresh")
	}

	fetchErr := errors.New("codepipeline unavailable")
	pipes.setErr(fetchErr)
	second := pollOnce(t, p)

	alpha := second.Projects[0].Pipeline
	if alpha.Stoplight != aggregator.StoplightGreen {
		t.Errorf("expected last known green to carry forward, got %v", alpha.Stoplight)
	}
	if alpha.Name != "alpha-pipeline" {
		t.Errorf("expected carried-forward pipeline name, got %q", alpha.Name)
	}
	if len(alpha.Stages) != 1 {
		t.Errorf("expected carried-forward stages, got %d", len(alpha.Stages))
	}
	if !alpha.IsStale() {
		t.Error("expected StaleAt to be set on fetch error")
	}
	if !errors.Is(alpha.Err, fetchErr) {
		t.Errorf("expected the fetch error to be recorded, got %v", alpha.Err)
	}
}

func TestPollClearsStaleAfterRecovery(t *testing.T) {
	cfg := loadConfig(t, twoProjectConfig)
	pipes := &fakePipelineFetcher{data: map[string]awsclient.PipelineData{
		"alpha-pipeline": pipelineData("alpha-pipeline", aggregator.StatusSucceeded),
		"beta-pipeline":  pipelineData("beta-pipeline", aggregator.StatusSucceeded),
	}}
	p := New(cfg, pipelineFactory(pipes), nil, nil)

	pipes.setErr(errors.New("transient"))
	stale := pollOnce(t, p)
	if !stale.Projects[0].Pipeline.IsStale() {
		t.Fatal("expected stale after a failed fetch")
	}

	pipes.setErr(nil)
	fresh := pollOnce(t, p)
	if fresh.Projects[0].Pipeline.IsStale() {
		t.Error("expected staleness cleared after a successful fetch")
	}
	if fresh.Projects[0].Pipeline.Err != nil {
		t.Errorf("expected error cleared after a successful fetch, got %v", fresh.Projects[0].Pipeline.Err)
	}
}

func TestPollCarriesForwardByNameWhenEnabledSetShifts(t *testing.T) {
	cfg := loadConfig(t, twoProjectConfig)
	pipes := &fakePipelineFetcher{data: map[string]awsclient.PipelineData{
		"alpha-pipeline": pipelineData("alpha-pipeline", aggregator.StatusSucceeded),
		"beta-pipeline":  pipelineData("beta-pipeline", aggregator.StatusFailed),
	}}
	p := New(cfg, pipelineFactory(pipes), nil, nil)

	first := pollOnce(t, p)
	if first.Projects[0].Pipeline.Stoplight != aggregator.StoplightGreen {
		t.Fatalf("expected alpha green, got %v", first.Projects[0].Pipeline.Stoplight)
	}
	if first.Projects[1].Pipeline.Stoplight != aggregator.StoplightRed {
		t.Fatalf("expected beta red, got %v", first.Projects[1].Pipeline.Stoplight)
	}

	// Disable alpha so beta moves from index 1 to index 0, then fail the fetch.
	// Carrying forward by index would hand beta alpha's green state.
	cfg.Projects[0].Enabled = boolPtr(false)
	pipes.setErr(errors.New("codepipeline unavailable"))

	second := pollOnce(t, p)
	if len(second.Projects) != 1 {
		t.Fatalf("expected only beta to remain, got %d projects", len(second.Projects))
	}
	beta := second.Projects[0]
	if beta.Name != "beta" {
		t.Fatalf("expected beta, got %q", beta.Name)
	}
	if beta.Pipeline.Stoplight != aggregator.StoplightRed {
		t.Errorf("expected beta's own red to carry forward, got %v", beta.Pipeline.Stoplight)
	}
	if beta.Pipeline.Name != "beta-pipeline" {
		t.Errorf("expected beta-pipeline carried forward, got %q", beta.Pipeline.Name)
	}
}

func TestPrevPipelineMatchesByName(t *testing.T) {
	prev := []state.ProjectState{
		{Name: "alpha", Pipeline: state.PipelineState{Name: "alpha-pipeline", Stoplight: aggregator.StoplightGreen}},
		{Name: "beta", Pipeline: state.PipelineState{Name: "beta-pipeline", Stoplight: aggregator.StoplightRed}},
	}

	if got := prevPipeline(prev, "beta"); got.Name != "beta-pipeline" || got.Stoplight != aggregator.StoplightRed {
		t.Errorf("expected beta's pipeline, got %+v", got)
	}
	if got := prevPipeline(prev, "gamma"); got.Name != "" || got.Stoplight != aggregator.StoplightGrey {
		t.Errorf("expected zero PipelineState for an unknown project, got %+v", got)
	}
	if got := prevPipeline(nil, "alpha"); got.Name != "" {
		t.Errorf("expected zero PipelineState for empty previous state, got %+v", got)
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
	if pipe.Err.Error() != `no client available for account "prod"` {
		t.Errorf("unexpected error: %v", pipe.Err)
	}
	if !pipe.IsStale() {
		t.Error("expected StaleAt set when no client is available")
	}
}

func TestPollResolvesProfileAndRegionFromAccount(t *testing.T) {
	cfg := loadConfig(t, twoProjectConfig)
	pipes := &fakePipelineFetcher{data: map[string]awsclient.PipelineData{
		"alpha-pipeline": pipelineData("alpha-pipeline", aggregator.StatusSucceeded),
		"beta-pipeline":  pipelineData("beta-pipeline", aggregator.StatusSucceeded),
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
	pipes := &fakePipelineFetcher{data: map[string]awsclient.PipelineData{
		"alpha-pipeline": pipelineData("alpha-pipeline", aggregator.StatusSucceeded),
	}}
	p := New(cfg, pipelineFactory(pipes), nil, nil)

	snap := pollOnce(t, p)
	if snap.Projects[0].Pipeline.Stoplight != aggregator.StoplightGreen {
		t.Errorf("expected green via the default client, got %v", snap.Projects[0].Pipeline.Stoplight)
	}
	if snap.Projects[0].Profile != "" || snap.Projects[0].Region != "" {
		t.Errorf("expected empty profile/region for an accountless project, got %q/%q",
			snap.Projects[0].Profile, snap.Projects[0].Region)
	}
}

func TestPollProjectWithoutPipelineIsGrey(t *testing.T) {
	cfg := loadConfig(t, `
[[accounts]]
name = "prod"
profile = "prod-profile"
region = "us-east-1"

[[projects]]
name = "alpha"
account = "prod"
[[projects.stacks]]
name = "alpha-stack"
`)
	pipes := &fakePipelineFetcher{}
	stacks := &fakeCFNFetcher{data: []cfn.StackData{
		{Name: "alpha-stack", Status: "CREATE_COMPLETE", Stoplight: aggregator.StoplightGreen},
	}}
	p := New(cfg, pipelineFactory(pipes), cfnFactory(stacks), nil)

	snap := pollOnce(t, p)
	if len(pipes.fetched()) != 0 {
		t.Errorf("expected no pipeline fetch when no pipeline is configured, got %v", pipes.fetched())
	}
	pipe := snap.Projects[0].Pipeline
	if pipe.Stoplight != aggregator.StoplightGrey {
		t.Errorf("expected grey pipeline placeholder, got %v", pipe.Stoplight)
	}
	if pipe.Account != "prod" {
		t.Errorf("expected the account carried onto the placeholder, got %q", pipe.Account)
	}
	if len(snap.Projects[0].Stacks) != 1 {
		t.Fatalf("expected 1 stack, got %d", len(snap.Projects[0].Stacks))
	}
	// The stack is still green, so the project as a whole is green.
	if snap.Projects[0].Stoplight() != aggregator.StoplightGreen {
		t.Errorf("expected project green from its stack, got %v", snap.Projects[0].Stoplight())
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
	pipes := &fakePipelineFetcher{data: map[string]awsclient.PipelineData{
		"alpha-pipeline": pipelineData("alpha-pipeline", aggregator.StatusSucceeded),
	}}
	stacks := &fakeCFNFetcher{data: []cfn.StackData{
		{Name: "alpha-stack", Status: "UPDATE_ROLLBACK_COMPLETE", Stoplight: aggregator.StoplightRed},
	}}
	services := &fakeECSFetcher{data: []ecs.ServiceData{
		{Name: "web", RunningCount: 2, DesiredCount: 2, Stoplight: aggregator.StoplightGreen},
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
	if proj.Stoplight() != aggregator.StoplightRed {
		t.Errorf("expected the project to take the worst stoplight, got %v", proj.Stoplight())
	}
}

func TestPollSurvivesStackAndServiceFetchErrors(t *testing.T) {
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
	pipes := &fakePipelineFetcher{data: map[string]awsclient.PipelineData{
		"alpha-pipeline": pipelineData("alpha-pipeline", aggregator.StatusSucceeded),
	}}
	stacks := &fakeCFNFetcher{err: errors.New("cfn down")}
	services := &fakeECSFetcher{err: errors.New("ecs down")}
	p := New(cfg, pipelineFactory(pipes), cfnFactory(stacks), ecsFactory(services))

	snap := pollOnce(t, p)
	proj := snap.Projects[0]
	// Documented current behaviour: stack and ECS fetch errors are swallowed
	// and the rows simply go empty, unlike the pipeline's carry-forward path.
	if len(proj.Stacks) != 0 {
		t.Errorf("expected no stacks after a failed CFN fetch, got %+v", proj.Stacks)
	}
	if len(proj.ECSServices) != 0 {
		t.Errorf("expected no services after a failed ECS fetch, got %+v", proj.ECSServices)
	}
	if proj.Pipeline.Stoplight != aggregator.StoplightGreen {
		t.Errorf("expected the pipeline to still report green, got %v", proj.Pipeline.Stoplight)
	}
}

func TestPollWithNilCFNAndECSFactories(t *testing.T) {
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
	pipes := &fakePipelineFetcher{data: map[string]awsclient.PipelineData{
		"alpha-pipeline": pipelineData("alpha-pipeline", aggregator.StatusSucceeded),
	}}
	p := New(cfg, pipelineFactory(pipes), nil, nil)

	snap := pollOnce(t, p)
	if len(snap.Projects[0].Stacks) != 0 || len(snap.Projects[0].ECSServices) != 0 {
		t.Errorf("expected no stacks or services without factories, got %+v", snap.Projects[0])
	}
	if snap.Projects[0].Pipeline.Stoplight != aggregator.StoplightGreen {
		t.Errorf("expected green, got %v", snap.Projects[0].Pipeline.Stoplight)
	}
}

func TestReloadConfigRebuildsCurrentFromNewEnabledSet(t *testing.T) {
	cfg := loadConfig(t, twoProjectConfig)
	pipes := &fakePipelineFetcher{data: map[string]awsclient.PipelineData{
		"alpha-pipeline": pipelineData("alpha-pipeline", aggregator.StatusSucceeded),
		"beta-pipeline":  pipelineData("beta-pipeline", aggregator.StatusSucceeded),
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
	// Reload resets to grey rather than carrying the previous run's colours.
	if got.Pipeline.Stoplight != aggregator.StoplightGrey {
		t.Errorf("expected grey after reload, got %v", got.Pipeline.Stoplight)
	}
}

func TestStartPollsImmediatelyAndStops(t *testing.T) {
	cfg := loadConfig(t, twoProjectConfig)
	cfg.Settings.PollInterval = 1
	pipes := &fakePipelineFetcher{data: map[string]awsclient.PipelineData{
		"alpha-pipeline": pipelineData("alpha-pipeline", aggregator.StatusSucceeded),
		"beta-pipeline":  pipelineData("beta-pipeline", aggregator.StatusInProgress),
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
		if snap.Projects[0].Pipeline.Stoplight != aggregator.StoplightGreen {
			t.Errorf("expected alpha green, got %v", snap.Projects[0].Pipeline.Stoplight)
		}
		if snap.Projects[1].Pipeline.Stoplight != aggregator.StoplightYellow {
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
