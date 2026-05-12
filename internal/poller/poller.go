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

// Poller orchestrates periodic fetches across all configured pipelines.
type Poller struct {
	cfg     *config.Config
	factory ClientFactory
	mu      sync.Mutex
	current []state.PipelineState
}

// New creates a Poller with the given config and client factory.
func New(cfg *config.Config, factory ClientFactory) *Poller {
	pipelines := make([]state.PipelineState, len(cfg.Pipelines))
	for i, p := range cfg.Pipelines {
		pipelines[i] = state.PipelineState{
			Account:   p.Account,
			Name:      p.Name,
			Stoplight: aggregator.StoplightGrey,
		}
	}
	return &Poller{
		cfg:     cfg,
		factory: factory,
		current: pipelines,
	}
}

// Snapshot returns an immutable view of the current state.
func (p *Poller) Snapshot() state.Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return state.New(p.current)
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
	updated := make([]state.PipelineState, len(p.cfg.Pipelines))

	// Build per-account clients once per poll cycle.
	clients := make(map[string]Fetcher)
	for _, acct := range p.cfg.Accounts {
		key := acct.Name
		client, err := p.factory(acct.Profile, acct.Region)
		if err != nil {
			// Store the error; pipelines referencing this account will show stale.
			_ = err
			continue
		}
		clients[key] = client
	}

	// Default client (no account) uses empty profile/region.
	defaultClient, _ := p.factory("", "")

	p.mu.Lock()
	prev := make([]state.PipelineState, len(p.current))
	copy(prev, p.current)
	p.mu.Unlock()

	for i, pipe := range p.cfg.Pipelines {
		var client Fetcher
		if pipe.Account != "" {
			client = clients[pipe.Account]
		} else {
			client = defaultClient
		}

		if client == nil {
			now := time.Now()
			updated[i] = prev[i]
			updated[i].StaleAt = &now
			updated[i].Err = fmt.Errorf("no client available for account %q", pipe.Account)
			continue
		}

		data, err := client.FetchPipeline(ctx, pipe.Name)
		if err != nil {
			now := time.Now()
			updated[i] = prev[i]
			updated[i].StaleAt = &now
			updated[i].Err = err
			continue
		}

		updated[i] = state.FromData(pipe.Account, data)
	}

	p.mu.Lock()
	p.current = updated
	p.mu.Unlock()

	snap := state.New(updated)
	select {
	case ch <- snap:
	case <-ctx.Done():
	}
}
