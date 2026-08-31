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
		prevProj := prevProject(prev, proj.Name, proj.Account)

		// Fetch pipeline.
		if client == nil {
			now := time.Now()
			pipe := prevPipeline(prev, proj.Name, proj.Account)
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
				pipe := prevPipeline(prev, proj.Name, proj.Account)
				pipe.StaleAt = &now
				pipe.Err = err
				updated[i].Pipeline = pipe
			} else {
				updated[i].Pipeline = state.FromData(proj.Account, data)
			}
		}

		// Fetch CloudFormation stacks. Failures mirror the pipeline path:
		// keep showing the last known stacks, marked stale, so a broken call
		// is distinguishable from a project that has no stacks configured.
		if len(proj.Stacks) > 0 {
			switch {
			case cfnClient == nil && p.cfnFactory == nil:
				// No CFN support wired up at all; nothing to report.
			case cfnClient == nil:
				now := time.Now()
				updated[i].Stacks = prevProj.Stacks
				updated[i].StacksFetch = state.FetchStatus{
					StaleAt: &now,
					Err:     fmt.Errorf("no CloudFormation client available for account %q", proj.Account),
				}
			default:
				names := make([]string, len(proj.Stacks))
				for j, s := range proj.Stacks {
					names[j] = s.Name
				}
				stackData, err := cfnClient.FetchStacks(ctx, names)
				if err != nil {
					now := time.Now()
					updated[i].Stacks = prevProj.Stacks
					updated[i].StacksFetch = state.FetchStatus{StaleAt: &now, Err: err}
				} else {
					stacks := make([]state.StackState, len(stackData))
					for j, sd := range stackData {
						stacks[j] = state.StackStateFromData(sd)
					}
					updated[i].Stacks = stacks
				}
			}
		}

		// Fetch ECS services, one call per cluster.
		if len(proj.ECS) > 0 {
			switch {
			case ecsClient == nil && p.ecsFactory == nil:
				// No ECS support wired up at all; nothing to report.
			case ecsClient == nil:
				now := time.Now()
				updated[i].ECSServices = prevProj.ECSServices
				updated[i].ECSFetch = state.FetchStatus{
					StaleAt: &now,
					Err:     fmt.Errorf("no ECS client available for account %q", proj.Account),
				}
			default:
				var allServices []state.ECSServiceState
				var firstErr error
				for _, ecsCfg := range proj.ECS {
					serviceData, err := ecsClient.FetchServices(ctx, ecsCfg.Cluster, ecsCfg.Services)
					if err != nil {
						if firstErr == nil {
							firstErr = err
						}
						allServices = append(allServices, servicesForCluster(prevProj.ECSServices, ecsCfg.Cluster)...)
						continue
					}
					for _, sd := range serviceData {
						allServices = append(allServices, state.ECSServiceStateFromData(ecsCfg.Cluster, sd))
					}
				}
				updated[i].ECSServices = allServices
				if firstErr != nil {
					now := time.Now()
					updated[i].ECSFetch = state.FetchStatus{StaleAt: &now, Err: firstErr}
				}
			}
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

		// A fetch that keeps failing is its own kind of stuck. Every resource
		// status on screen is frozen at whatever it was when the credential
		// died, and the stuck checks below deliberately skip that data — so
		// without this, a dead fetch alerts nowhere at all.
		for _, f := range failedFetches(ps) {
			ff := f
			key := stuckKey(ps, "fetch:"+ff.resourceType, ff.resource)
			track(key, "fetch_failed", func(since time.Time) webhooks.Event {
				return webhooks.Event{
					Event:        "fetch_failed",
					Reason:       "fetch_failed",
					Project:      ps.Name,
					Account:      ps.Account,
					Region:       ps.Region,
					ResourceType: ff.resourceType,
					Resource:     ff.resource,
					Detail:       ff.err.Error(),
					StuckSince:   since,
					Timestamp:    now,
				}
			})
		}

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

		// Stacks and services whose fetch errored are showing carried-forward
		// data, so alerting on them would report an expired credential as a
		// wedged deploy — the same reason pipelineStuckReason skips on Err.
		for _, stack := range stacksIfFresh(ps) {
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

		for _, service := range servicesIfFresh(ps) {
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

// failedFetch describes one of a project's three fetches that is currently
// erroring.
type failedFetch struct {
	resourceType string
	resource     string
	err          error
}

// failedFetches lists the project's erroring fetches. Stack and ECS fetches
// cover a whole group rather than one named resource, so they report the
// project as the resource; the error itself rides along in Detail.
func failedFetches(ps state.ProjectState) []failedFetch {
	var out []failedFetch
	if ps.Pipeline.Err != nil {
		out = append(out, failedFetch{webhooks.ResourcePipeline, ps.Pipeline.Name, ps.Pipeline.Err})
	}
	if ps.StacksFetch.Err != nil {
		out = append(out, failedFetch{webhooks.ResourceStack, ps.Name, ps.StacksFetch.Err})
	}
	if ps.ECSFetch.Err != nil {
		out = append(out, failedFetch{webhooks.ResourceECSService, ps.Name, ps.ECSFetch.Err})
	}
	return out
}

// stacksIfFresh returns a project's stacks only when the last fetch succeeded.
func stacksIfFresh(ps state.ProjectState) []state.StackState {
	if ps.StacksFetch.Err != nil {
		return nil
	}
	return ps.Stacks
}

// servicesIfFresh returns a project's services only when the last fetch
// succeeded.
func servicesIfFresh(ps state.ProjectState) []state.ECSServiceState {
	if ps.ECSFetch.Err != nil {
		return nil
	}
	return ps.ECSServices
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
