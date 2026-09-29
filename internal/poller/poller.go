package poller

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ericdahl-dev/aws-green/internal/cfn"
	"github.com/ericdahl-dev/aws-green/internal/config"
	"github.com/ericdahl-dev/aws-green/internal/ecs"
	"github.com/ericdahl-dev/aws-green/internal/health"
	"github.com/ericdahl-dev/aws-green/internal/state"
	"github.com/ericdahl-dev/aws-green/internal/webhooks"
)

// Fetcher is the interface the Poller uses to fetch pipeline state.
type Fetcher interface {
	FetchPipeline(ctx context.Context, name string) (state.PipelineState, error)
}

// ClientFactory creates a Fetcher for a given AWS profile and region.
type ClientFactory func(profile, region string) (Fetcher, error)

// CFNClientFactory creates a cfn.Fetcher for a given AWS profile and region.
type CFNClientFactory func(profile, region string) (cfn.Fetcher, error)

// ECSClientFactory creates an ecs.Fetcher for a given AWS profile and region.
type ECSClientFactory func(profile, region string) (ecs.Fetcher, error)

// Poller orchestrates periodic fetches across all configured projects.
type Poller struct {
	cfg        *config.Config
	factory    ClientFactory
	cfnFactory CFNClientFactory
	ecsFactory ECSClientFactory
	mu         sync.Mutex
	current    []state.ProjectState
	dispatcher *webhooks.Dispatcher
	stuck      *stuckTracker
	// clients is keyed by profile and region; see clientsFor.
	clients map[string]accountClients
	// now is swappable in tests so threshold crossings can be exercised
	// without waiting on the wall clock.
	now func() time.Time
}

// New creates a Poller with the given config and client factories.
func New(cfg *config.Config, factory ClientFactory, cfnFactory CFNClientFactory, ecsFactory ECSClientFactory) *Poller {
	cfg = cfg.Clone()
	enabled := cfg.EnabledProjects()
	projects := make([]state.ProjectState, len(enabled))
	for i, p := range enabled {
		projects[i] = state.ProjectState{
			Name:    p.Name,
			Account: p.Account,
			Pipeline: state.PipelineState{
				Account:   p.Account,
				Name:      p.Pipeline.Name,
				Stoplight: health.StoplightGrey,
			},
		}
	}
	return &Poller{
		cfg:        cfg,
		factory:    factory,
		cfnFactory: cfnFactory,
		ecsFactory: ecsFactory,
		current:    projects,
		dispatcher: webhooks.New(cfg.Webhooks),
		stuck:      newStuckTracker(),
		clients:    make(map[string]accountClients),
		now:        time.Now,
	}
}

// Snapshot returns an immutable view of the current state.
func (p *Poller) Snapshot() state.Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return state.NewFromProjects(p.current)
}

// Start begins polling on the configured interval, sending Snapshots to the returned channel.
// Call the returned cancel func to stop.
func (p *Poller) Start(ctx context.Context) (<-chan state.Snapshot, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	ch := make(chan state.Snapshot, 4)

	go func() {
		defer close(ch)
		p.poll(ctx, ch)
		p.mu.Lock()
		interval := time.Duration(p.cfg.Settings.PollInterval) * time.Second
		p.mu.Unlock()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.poll(ctx, ch)
			}
		}
	}()

	return ch, cancel
}

// ForceRefresh triggers an immediate poll outside the normal interval.
func (p *Poller) ForceRefresh(ctx context.Context, ch chan<- state.Snapshot) {
	go p.poll(ctx, ch)
}

// ReloadConfig replaces the config (e.g. after CRUD edits) and triggers an
// immediate poll so the dashboard reflects the new project list.
func (p *Poller) ReloadConfig(cfg *config.Config, ctx context.Context, ch chan<- state.Snapshot) {
	cfg = cfg.Clone()
	p.mu.Lock()
	prev := p.current
	p.cfg = cfg
	p.dispatcher = webhooks.New(cfg.Webhooks)
	enabled := cfg.EnabledProjects()
	current := make([]state.ProjectState, len(enabled))
	for i, proj := range enabled {
		current[i] = carryForward(prevProject(prev, proj.Name, proj.Account), proj)
	}
	p.current = current
	p.mu.Unlock()
	go p.poll(ctx, ch)
}

// clientsFor returns the fetchers for one profile and region, building each
// once and reusing it across cycles: the SDK's adaptive retry limiter learns
// from throttling inside a client, and that is lost if the client is rebuilt
// every tick. A client that failed to build is retried next cycle, since the
// fix (an SSO login, a new profile) happens outside the app. A kind whose
// factory isn't wired up is left unsupported rather than failed.
func (p *Poller) clientsFor(profile, region string) accountClients {
	key := profile + "\x00" + region
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.clients[key]
	if c.pipeline == nil {
		c.pipeline, c.pipelineErr = p.factory(profile, region)
	}
	if c.cfn == nil && p.cfnFactory != nil {
		c.cfn, c.cfnErr = p.cfnFactory(profile, region)
	}
	if c.ecs == nil && p.ecsFactory != nil {
		c.ecs, c.ecsErr = p.ecsFactory(profile, region)
	}
	p.clients[key] = c
	return c
}

// prevProject returns the last known state for a project, matched on the
// account-qualified identity. Matching by identity rather than by index keeps
// carried-forward state correct when a project is enabled or disabled and the
// positions shift; including the account stops the same project name in two
// accounts from carrying the other one's data forward.
func prevProject(prev []state.ProjectState, name, account string) state.ProjectState {
	for _, ps := range prev {
		if ps.Name == name && ps.Account == account {
			return ps
		}
	}
	return state.ProjectState{}
}

// prevPipeline returns the last known pipeline state for a project.
func prevPipeline(prev []state.ProjectState, name, account string) state.PipelineState {
	return prevProject(prev, name, account).Pipeline
}

// servicesForCluster filters carried-forward services down to one cluster, so
// a single failing cluster does not blank the ones that answered.
func servicesForCluster(services []state.ECSServiceState, cluster string) []state.ECSServiceState {
	var out []state.ECSServiceState
	for _, sv := range services {
		if sv.Cluster == cluster {
			out = append(out, sv)
		}
	}
	return out
}

// carryForward rebases a project's last known state onto its new config entry.
// Rebuilding every row from zero on a config edit would blank the health of
// every project until the next poll returns — the same "grey means both
// 'unknown' and 'was fine a second ago'" ambiguity that carried-forward fetches
// exist to avoid. State the edit invalidated is dropped rather than carried, so
// the edit still shows up immediately.
func carryForward(carried state.ProjectState, proj config.Project) state.ProjectState {
	carried.Name = proj.Name
	carried.Account = proj.Account

	// A renamed pipeline's stages describe the old pipeline, so start it over.
	if carried.Pipeline.Name != proj.Pipeline.Name {
		carried.Pipeline = state.PipelineState{Account: proj.Account, Name: proj.Pipeline.Name}
	}
	carried.Pipeline.Account = proj.Account

	carried.Stacks = configuredStacks(carried.Stacks, proj.Stacks)
	carried.ECSServices = configuredServices(carried.ECSServices, proj.ECS)
	return carried
}

// configuredStacks drops carried-forward stacks that are no longer configured,
// so deleting one does not leave it — and its stoplight — on screen.
func configuredStacks(carried []state.StackState, cfgStacks []config.Stack) []state.StackState {
	want := make(map[string]bool, len(cfgStacks))
	for _, s := range cfgStacks {
		want[s.Name] = true
	}
	var out []state.StackState
	for _, s := range carried {
		if want[s.Name] {
			out = append(out, s)
		}
	}
	return out
}

// configuredServices does the same for ECS services, keyed by cluster and name
// because the same service name is commonly deployed to several clusters.
func configuredServices(carried []state.ECSServiceState, cfgECS []config.ECSConfig) []state.ECSServiceState {
	want := make(map[string]bool)
	for _, e := range cfgECS {
		for _, svc := range e.Services {
			want[e.Cluster+"/"+svc] = true
		}
	}
	var out []state.ECSServiceState
	for _, sv := range carried {
		if want[sv.Cluster+"/"+sv.Name] {
			out = append(out, sv)
		}
	}
	return out
}

func (p *Poller) poll(ctx context.Context, ch chan<- state.Snapshot) {
	// Take cfg and current state under the lock. p.cfg is the poller's own copy,
	// which ReloadConfig replaces wholesale rather than edits, so it is safe to
	// read for the rest of the cycle.
	p.mu.Lock()
	cfg := p.cfg
	prev := make([]state.ProjectState, len(p.current))
	copy(prev, p.current)
	p.mu.Unlock()

	projects := cfg.EnabledProjects()
	updated := make([]state.ProjectState, len(projects))
	now := p.now()

	// Projects without an Account use the default credential chain.
	clients := map[string]accountClients{"": p.clientsFor("", "")}
	for _, acct := range cfg.Accounts {
		clients[acct.Name] = p.clientsFor(acct.Profile, acct.Region)
	}

	for i, proj := range projects {
		c, ok := clients[proj.Account]
		if !ok {
			err := fmt.Errorf("account %q is not configured", proj.Account)
			c = accountClients{pipelineErr: err, cfnErr: err, ecsErr: err}
		}
		updated[i] = fetchProject(ctx, proj, c, prevProject(prev, proj.Name, proj.Account), now)
		if acct, ok := cfg.AccountFor(proj); ok {
			updated[i].Profile = acct.Profile
			updated[i].Region = acct.Region
		}
	}

	p.mu.Lock()
	p.current = updated
	events := p.stuck.evaluate(updated, cfg.Settings.StuckThreshold(), now)
	dispatcher := p.dispatcher
	p.mu.Unlock()

	snap := state.NewFromProjects(updated)
	select {
	case ch <- snap:
	case <-ctx.Done():
	}

	// Dispatch outside the lock and after the snapshot is out: a slow or
	// hanging webhook endpoint must not stall the dashboard.
	for _, evt := range events {
		dispatcher.Dispatch(evt)
	}
}
