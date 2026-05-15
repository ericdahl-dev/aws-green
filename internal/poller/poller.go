package poller

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ericdahl-dev/aws-green/internal/aggregator"
	awsclient "github.com/ericdahl-dev/aws-green/internal/aws"
	"github.com/ericdahl-dev/aws-green/internal/cfn"
	"github.com/ericdahl-dev/aws-green/internal/config"
	"github.com/ericdahl-dev/aws-green/internal/ecs"
	"github.com/ericdahl-dev/aws-green/internal/state"
)

// Fetcher is the interface the Poller uses to fetch pipeline state.
type Fetcher interface {
	FetchPipeline(ctx context.Context, name string) (awsclient.PipelineData, error)
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
}

// New creates a Poller with the given config and client factories.
func New(cfg *config.Config, factory ClientFactory, cfnFactory CFNClientFactory, ecsFactory ECSClientFactory) *Poller {
	projects := make([]state.ProjectState, len(cfg.Projects))
	for i, p := range cfg.Projects {
		projects[i] = state.ProjectState{
			Name:    p.Name,
			Account: p.Account,
			Pipeline: state.PipelineState{
				Account:   p.Account,
				Name:      p.Pipeline.Name,
				Stoplight: aggregator.StoplightGrey,
			},
		}
	}
	return &Poller{
		cfg:        cfg,
		factory:    factory,
		cfnFactory: cfnFactory,
		ecsFactory: ecsFactory,
		current:    projects,
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
	p.mu.Lock()
	p.cfg = cfg
	p.current = make([]state.ProjectState, len(cfg.Projects))
	for i, proj := range cfg.Projects {
		p.current[i] = state.ProjectState{
			Name:    proj.Name,
			Account: proj.Account,
			Pipeline: state.PipelineState{
				Account:   proj.Account,
				Name:      proj.Pipeline.Name,
				Stoplight: 0, // StoplightGrey
			},
		}
	}
	p.mu.Unlock()
	go p.poll(ctx, ch)
}

func (p *Poller) poll(ctx context.Context, ch chan<- state.Snapshot) {
	// Snapshot cfg and current state under the lock so concurrent
	// ReloadConfig calls cannot mutate p.cfg mid-cycle (data race fix).
	p.mu.Lock()
	cfg := p.cfg
	prev := make([]state.ProjectState, len(p.current))
	copy(prev, p.current)
	p.mu.Unlock()

	updated := make([]state.ProjectState, len(cfg.Projects))

	// Build per-account clients once per poll cycle.
	clients := make(map[string]Fetcher)
	cfnClients := make(map[string]cfn.Fetcher)
	ecsClients := make(map[string]ecs.Fetcher)
	for _, acct := range cfg.Accounts {
		if client, err := p.factory(acct.Profile, acct.Region); err == nil {
			clients[acct.Name] = client
		}
		if p.cfnFactory != nil {
			if client, err := p.cfnFactory(acct.Profile, acct.Region); err == nil {
				cfnClients[acct.Name] = client
			}
		}
		if p.ecsFactory != nil {
			if client, err := p.ecsFactory(acct.Profile, acct.Region); err == nil {
				ecsClients[acct.Name] = client
			}
		}
	}

	// Default clients (no account) use empty profile/region.
	defaultClient, _ := p.factory("", "")
	var defaultCFNClient cfn.Fetcher
	if p.cfnFactory != nil {
		defaultCFNClient, _ = p.cfnFactory("", "")
	}
	var defaultECSClient ecs.Fetcher
	if p.ecsFactory != nil {
		defaultECSClient, _ = p.ecsFactory("", "")
	}

	for i, proj := range cfg.Projects {
		var client Fetcher
		var cfnClient cfn.Fetcher
		var ecsClient ecs.Fetcher
		var profile, region string
		if proj.Account != "" {
			client = clients[proj.Account]
			cfnClient = cfnClients[proj.Account]
			ecsClient = ecsClients[proj.Account]
			if acct, ok := cfg.AccountFor(proj); ok {
				profile = acct.Profile
				region = acct.Region
			}
		} else {
			client = defaultClient
			cfnClient = defaultCFNClient
			ecsClient = defaultECSClient
		}

		updated[i] = state.ProjectState{
			Name:    proj.Name,
			Account: proj.Account,
			Profile: profile,
			Region:  region,
		}

		// Fetch pipeline.
		if client == nil {
			now := time.Now()
			pipe := prev[i].Pipeline
			pipe.StaleAt = &now
			pipe.Err = fmt.Errorf("no client available for account %q", proj.Account)
			updated[i].Pipeline = pipe
		} else if proj.Pipeline.Name == "" {
			updated[i].Pipeline = state.PipelineState{
				Account:   proj.Account,
				Stoplight: aggregator.StoplightGrey,
			}
		} else {
			data, err := client.FetchPipeline(ctx, proj.Pipeline.Name)
			if err != nil {
				now := time.Now()
				pipe := prev[i].Pipeline
				pipe.StaleAt = &now
				pipe.Err = err
				updated[i].Pipeline = pipe
			} else {
				updated[i].Pipeline = state.FromData(proj.Account, data)
			}
		}

		// Fetch CloudFormation stacks.
		if cfnClient != nil && len(proj.Stacks) > 0 {
			names := make([]string, len(proj.Stacks))
			for j, s := range proj.Stacks {
				names[j] = s.Name
			}
			stackData, err := cfnClient.FetchStacks(ctx, names)
			if err == nil {
				stacks := make([]state.StackState, len(stackData))
				for j, sd := range stackData {
					stacks[j] = state.StackStateFromData(sd)
				}
				updated[i].Stacks = stacks
			}
		}

		// Fetch ECS services.
		if ecsClient != nil && len(proj.ECS) > 0 {
			var allServices []state.ECSServiceState
			for _, ecsCfg := range proj.ECS {
				serviceData, err := ecsClient.FetchServices(ctx, ecsCfg.Cluster, ecsCfg.Services)
				if err == nil {
					for _, sd := range serviceData {
						allServices = append(allServices, state.ECSServiceStateFromData(ecsCfg.Cluster, sd))
					}
				}
			}
			updated[i].ECSServices = allServices
		}
	}

	p.mu.Lock()
	p.current = updated
	p.mu.Unlock()

	snap := state.NewFromProjects(updated)
	select {
	case ch <- snap:
	case <-ctx.Done():
	}
}
