package poller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ericdahl-dev/aws-green/internal/aggregator"
	awsclient "github.com/ericdahl-dev/aws-green/internal/aws"
	"github.com/ericdahl-dev/aws-green/internal/cfn"
	"github.com/ericdahl-dev/aws-green/internal/config"
	"github.com/ericdahl-dev/aws-green/internal/ecs"
	"github.com/ericdahl-dev/aws-green/internal/state"
	"github.com/ericdahl-dev/aws-green/internal/webhooks"
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

// stuckEntry records when a single resource was first seen in a bad state and
// whether its webhook has already been fired, so a wedged resource alerts once
// rather than on every poll cycle.
type stuckEntry struct {
	since   time.Time
	reason  string
	alerted bool
}

// Poller orchestrates periodic fetches across all configured projects.
type Poller struct {
	cfg        *config.Config
	factory    ClientFactory
	cfnFactory CFNClientFactory
	ecsFactory ECSClientFactory
	mu         sync.Mutex
	current    []state.ProjectState
	dispatcher *webhooks.Dispatcher
	// stuck is keyed by account-qualified resource key (see stuckKey) because
	// project names repeat across accounts and slice indices shift.
	stuck map[string]*stuckEntry
	// now is swappable in tests so threshold crossings can be exercised
	// without waiting on the wall clock.
	now func() time.Time
}

// New creates a Poller with the given config and client factories.
func New(cfg *config.Config, factory ClientFactory, cfnFactory CFNClientFactory, ecsFactory ECSClientFactory) *Poller {
	enabled := cfg.EnabledProjects()
	projects := make([]state.ProjectState, len(enabled))
	for i, p := range enabled {
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
		dispatcher: webhooks.New(cfg.Webhooks),
		stuck:      make(map[string]*stuckEntry),
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
	p.mu.Lock()
	p.cfg = cfg
	p.dispatcher = webhooks.New(cfg.Webhooks)
	enabled := cfg.EnabledProjects()
	p.current = make([]state.ProjectState, len(enabled))
	for i, proj := range enabled {
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

// prevPipeline returns the last known pipeline state for a project by name.
// Matching by name rather than by index keeps carried-forward state correct
// when a project is enabled or disabled and the positions shift.
func prevPipeline(prev []state.ProjectState, name string) state.PipelineState {
	for _, ps := range prev {
		if ps.Name == name {
			return ps.Pipeline
		}
	}
	return state.PipelineState{}
}

func (p *Poller) poll(ctx context.Context, ch chan<- state.Snapshot) {
	// Snapshot cfg and current state under the lock so concurrent
	// ReloadConfig calls cannot mutate p.cfg mid-cycle (data race fix).
	p.mu.Lock()
	cfg := p.cfg
	prev := make([]state.ProjectState, len(p.current))
	copy(prev, p.current)
	p.mu.Unlock()

	projects := cfg.EnabledProjects()
	updated := make([]state.ProjectState, len(projects))

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

	for i, proj := range projects {
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
			pipe := prevPipeline(prev, proj.Name)
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
				pipe := prevPipeline(prev, proj.Name)
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
	events := p.evaluateStuck(updated)
	p.mu.Unlock()

	// Dispatch outside the lock: a slow or hanging webhook endpoint must not
	// block Snapshot() and stall the dashboard.
	for _, evt := range events {
		p.dispatcher.Dispatch(evt)
	}

	snap := state.NewFromProjects(updated)
	select {
	case ch <- snap:
	case <-ctx.Done():
	}
}

// stuckKey builds the identity a stuck resource is tracked under. Project
// names repeat across accounts and slice indices shift as projects are
// enabled or disabled, so every key is account-qualified via ProjectState.Key.
func stuckKey(ps state.ProjectState, resourceType, resource string) string {
	return ps.Key() + "|" + resourceType + "|" + resource
}

// evaluateStuck folds the freshly-polled state into the stuck bookkeeping and
// returns the webhook events to send. A resource that stays stuck keeps its
// original stuck-since timestamp and fires exactly once — on the cycle where
// it crosses the threshold — rather than on every poll. Recovering clears the
// entry, so a resource that goes bad again alerts again.
//
// Callers must hold p.mu. Dispatching is left to the caller so the HTTP calls
// happen outside the lock.
func (p *Poller) evaluateStuck(projects []state.ProjectState) []webhooks.Event {
	threshold := time.Duration(p.cfg.Settings.StuckThresholdMinutes) * time.Minute
	now := p.now()
	seen := make(map[string]struct{}, len(p.stuck))
	var events []webhooks.Event

	// track records the stuck condition for one resource and appends an event
	// if this is the cycle that crosses the threshold.
	track := func(key, reason string, build func(since time.Time) webhooks.Event) {
		seen[key] = struct{}{}
		entry, ok := p.stuck[key]
		// A changed reason (an in-progress deploy turning into a failure) is a
		// new condition, so restart the clock and allow a fresh alert.
		if !ok || entry.reason != reason {
			entry = &stuckEntry{since: now, reason: reason}
			p.stuck[key] = entry
		}
		if entry.alerted || now.Sub(entry.since) < threshold {
			return
		}
		entry.alerted = true
		events = append(events, build(entry.since))
	}

	for _, proj := range projects {
		ps := proj

		if stuck, reason, status, detail := pipelineStuckReason(ps.Pipeline); stuck {
			key := stuckKey(ps, webhooks.ResourcePipeline, ps.Pipeline.Name)
			track(key, reason, func(since time.Time) webhooks.Event {
				return webhooks.Event{
					Event:        "pipeline_stuck",
					Reason:       reason,
					Project:      ps.Name,
					Account:      ps.Account,
					Region:       ps.Region,
					ResourceType: webhooks.ResourcePipeline,
					Resource:     ps.Pipeline.Name,
					Status:       status,
					Detail:       detail,
					StuckSince:   since,
					Timestamp:    now,
				}
			})
		}

		for _, stack := range ps.Stacks {
			st := stack
			stuck, reason := stackStuckReason(st)
			if !stuck {
				continue
			}
			key := stuckKey(ps, webhooks.ResourceStack, st.Name)
			track(key, reason, func(since time.Time) webhooks.Event {
				return webhooks.Event{
					Event:        "stack_stuck",
					Reason:       reason,
					Project:      ps.Name,
					Account:      ps.Account,
					Region:       ps.Region,
					ResourceType: webhooks.ResourceStack,
					Resource:     st.Name,
					Status:       st.Status,
					StuckSince:   since,
					Timestamp:    now,
				}
			})
		}

		for _, service := range ps.ECSServices {
			sv := service
			stuck, reason := ecsStuckReason(sv)
			if !stuck {
				continue
			}
			key := stuckKey(ps, webhooks.ResourceECSService, sv.Cluster+"/"+sv.Name)
			track(key, reason, func(since time.Time) webhooks.Event {
				return webhooks.Event{
					Event:        "ecs_service_stuck",
					Reason:       reason,
					Project:      ps.Name,
					Account:      ps.Account,
					Region:       ps.Region,
					ResourceType: webhooks.ResourceECSService,
					Resource:     sv.Name,
					Cluster:      sv.Cluster,
					Detail: fmt.Sprintf("%d running / %d desired (%d pending)",
						sv.RunningCount, sv.DesiredCount, sv.PendingCount),
					StuckSince: since,
					Timestamp:  now,
				}
			})
		}
	}

	// Drop resources that recovered or left the config, so they can alert
	// again next time they go bad.
	for key := range p.stuck {
		if _, ok := seen[key]; !ok {
			delete(p.stuck, key)
		}
	}

	return events
}

// pipelineStuckReason reports whether the latest pipeline execution is wedged,
// returning the reason, the offending stage status, and a human detail string.
// Pipelines whose fetch errored are skipped: what is displayed for them is
// carried-forward stale data, so alerting on it would report an expired
// credential as a broken deploy.
func pipelineStuckReason(ps state.PipelineState) (bool, string, string, string) {
	if ps.Err != nil || ps.Name == "" {
		return false, "", "", ""
	}
	// A failure anywhere outranks a still-running stage.
	for _, st := range ps.Stages {
		if st.Status == aggregator.StatusFailed || st.Status == aggregator.StatusStopped {
			return true, "pipeline_failed", string(st.Status), "stage " + st.Name
		}
	}
	for _, st := range ps.Stages {
		if st.Status == aggregator.StatusInProgress {
			return true, "pipeline_in_progress", string(st.Status), "stage " + st.Name
		}
	}
	return false, "", "", ""
}

// stackStuckReason reports whether a CloudFormation stack is wedged. Any
// *_IN_PROGRESS status that outlives the threshold counts, as does any
// *_FAILED status.
func stackStuckReason(st state.StackState) (bool, string) {
	switch {
	case strings.HasSuffix(st.Status, "_FAILED"):
		return true, "stack_failed"
	case strings.HasSuffix(st.Status, "_IN_PROGRESS"):
		return true, "stack_in_progress"
	}
	return false, ""
}

// ecsStuckReason reports whether an ECS service has failed to converge on its
// desired task count.
func ecsStuckReason(sv state.ECSServiceState) (bool, string) {
	if sv.RunningCount != sv.DesiredCount {
		return true, "ecs_count_mismatch"
	}
	return false, ""
}
