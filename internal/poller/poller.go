package poller

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ericdahl-dev/aws-green/internal/aggregator"
	awsclient "github.com/ericdahl-dev/aws-green/internal/aws"
	"github.com/ericdahl-dev/aws-green/internal/config"
	"github.com/ericdahl-dev/aws-green/internal/state"
)

// Fetcher is the interface the Poller uses to fetch pipeline state.
type Fetcher interface {
	FetchPipeline(ctx context.Context, name string) (awsclient.PipelineData, error)
}

// ClientFactory creates a Fetcher for a given AWS profile and region.
type ClientFactory func(profile, region string) (Fetcher, error)

// Poller orchestrates periodic fetches across all configured projects.
type Poller struct {
	cfg     *config.Config
	factory ClientFactory
	mu      sync.Mutex
	current []state.ProjectState
}

// New creates a Poller with the given config and client factory.
func New(cfg *config.Config, factory ClientFactory) *Poller {
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
		cfg:     cfg,
		factory: factory,
		current: projects,
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
		ticker := time.NewTicker(time.Duration(p.cfg.Settings.PollInterval) * time.Second)
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

func (p *Poller) poll(ctx context.Context, ch chan<- state.Snapshot) {
	updated := make([]state.ProjectState, len(p.cfg.Projects))

	// Build per-account clients once per poll cycle.
	clients := make(map[string]Fetcher)
	for _, acct := range p.cfg.Accounts {
		client, err := p.factory(acct.Profile, acct.Region)
		if err != nil {
			continue
		}
		clients[acct.Name] = client
	}

	// Default client (no account) uses empty profile/region.
	defaultClient, _ := p.factory("", "")

	p.mu.Lock()
	prev := make([]state.ProjectState, len(p.current))
	copy(prev, p.current)
	p.mu.Unlock()

	for i, proj := range p.cfg.Projects {
		var client Fetcher
		if proj.Account != "" {
			client = clients[proj.Account]
		} else {
			client = defaultClient
		}

		updated[i] = state.ProjectState{
			Name:    proj.Name,
			Account: proj.Account,
		}

		if client == nil {
			now := time.Now()
			pipe := prev[i].Pipeline
			pipe.StaleAt = &now
			pipe.Err = fmt.Errorf("no client available for account %q", proj.Account)
			updated[i].Pipeline = pipe
			continue
		}

		if proj.Pipeline.Name == "" {
			updated[i].Pipeline = state.PipelineState{
				Account:   proj.Account,
				Stoplight: aggregator.StoplightGrey,
			}
			continue
		}

		data, err := client.FetchPipeline(ctx, proj.Pipeline.Name)
		if err != nil {
			now := time.Now()
			pipe := prev[i].Pipeline
			pipe.StaleAt = &now
			pipe.Err = err
			updated[i].Pipeline = pipe
			continue
		}

		updated[i].Pipeline = state.FromData(proj.Account, data)
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
