package poller

import (
	"fmt"
	"time"

	"github.com/ericdahl-dev/aws-green/internal/health"
	"github.com/ericdahl-dev/aws-green/internal/state"
	"github.com/ericdahl-dev/aws-green/internal/webhooks"
)

// stuckEntry records when a single resource was first seen in a bad state and
// whether its webhook has already been fired, so a wedged resource alerts once
// rather than on every poll cycle.
type stuckEntry struct {
	since   time.Time
	reason  string
	alerted bool
}

// stuckTracker remembers, across poll cycles, when each resource was first
// seen stuck, so a Stuck resource alerts once when it crosses the threshold.
// Entries are keyed by account-qualified resource key (see stuckKey) because
// project names repeat across accounts and slice indices shift.
type stuckTracker struct {
	entries map[string]*stuckEntry
}

func newStuckTracker() *stuckTracker {
	return &stuckTracker{entries: make(map[string]*stuckEntry)}
}

// stuckKey builds the identity a stuck resource is tracked under. Project
// names repeat across accounts and slice indices shift as projects are
// enabled or disabled, so every key is account-qualified via ProjectState.Key.
func stuckKey(ps state.ProjectState, resourceType, resource string) string {
	return ps.Key() + "|" + resourceType + "|" + resource
}

// evaluate folds the freshly-polled state into the stuck bookkeeping and
// returns the webhook events to send. A resource that stays stuck keeps its
// original stuck-since timestamp and fires exactly once — on the cycle where
// it crosses the threshold — rather than on every poll. Recovering clears the
// entry, so a resource that goes bad again alerts again.
//
// Dispatching is left to the caller so the HTTP calls happen outside any lock.
func (t *stuckTracker) evaluate(projects []state.ProjectState, threshold time.Duration, now time.Time) []webhooks.Event {
	seen := make(map[string]struct{}, len(t.entries))
	var events []webhooks.Event

	// track records the stuck condition for one resource and, if this is the
	// cycle that crosses the threshold, queues evt stamped with when it began.
	track := func(key string, evt webhooks.Event) {
		seen[key] = struct{}{}
		entry, ok := t.entries[key]
		// A changed reason (an in-progress deploy turning into a failure) is a
		// new condition, so restart the clock and allow a fresh alert.
		if !ok || entry.reason != evt.Reason {
			entry = &stuckEntry{since: now, reason: evt.Reason}
			t.entries[key] = entry
		}
		if entry.alerted || now.Sub(entry.since) < threshold {
			return
		}
		entry.alerted = true
		evt.StuckSince = entry.since
		events = append(events, evt)
	}

	for _, ps := range projects {
		base := webhooks.Event{Project: ps.Name, Account: ps.Account, Region: ps.Region, Timestamp: now}

		// A fetch that keeps failing is its own kind of stuck. Every resource
		// status on screen is frozen at whatever it was when the credential
		// died, and the stuck checks below deliberately skip that data — so
		// without this, a dead fetch alerts nowhere at all.
		for _, ff := range failedFetches(ps) {
			evt := base
			evt.Event, evt.Reason = "fetch_failed", "fetch_failed"
			evt.ResourceType, evt.Resource = ff.resourceType, ff.resource
			evt.Detail = ff.err.Error()
			track(stuckKey(ps, "fetch:"+ff.resourceType, ff.resource), evt)
		}

		if stuck, reason, status, detail := pipelineStuckReason(ps.Pipeline); stuck {
			evt := base
			evt.Event, evt.Reason = "pipeline_stuck", reason
			evt.ResourceType, evt.Resource = webhooks.ResourcePipeline, ps.Pipeline.Name
			evt.Status, evt.Detail = status, detail
			track(stuckKey(ps, webhooks.ResourcePipeline, ps.Pipeline.Name), evt)
		}

		// Stacks and services whose fetch errored are showing carried-forward
		// data, so alerting on them would report an expired credential as a
		// wedged deploy — the same reason pipelineStuckReason skips on Err.
		for _, st := range stacksIfFresh(ps) {
			reason := st.Status.StuckReason()
			if reason == "" {
				continue
			}
			evt := base
			evt.Event, evt.Reason = "stack_stuck", reason
			evt.ResourceType, evt.Resource = webhooks.ResourceStack, st.Name
			evt.Status = string(st.Status)
			track(stuckKey(ps, webhooks.ResourceStack, st.Name), evt)
		}

		for _, sv := range servicesIfFresh(ps) {
			reason := sv.Health().StuckReason()
			if reason == "" {
				continue
			}
			evt := base
			evt.Event, evt.Reason = "ecs_service_stuck", reason
			evt.ResourceType, evt.Resource, evt.Cluster = webhooks.ResourceECSService, sv.Name, sv.Cluster
			evt.Detail = ecsStuckDetail(sv)
			track(stuckKey(ps, webhooks.ResourceECSService, sv.Cluster+"/"+sv.Name), evt)
		}
	}

	// Drop resources that recovered or left the config, so they can alert
	// again next time they go bad.
	for key := range t.entries {
		if _, ok := seen[key]; !ok {
			delete(t.entries, key)
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
		if st.Status == health.StatusFailed || st.Status == health.StatusStopped {
			return true, "pipeline_failed", string(st.Status), "stage " + st.Name
		}
	}
	// Short of a failure, waiting on a person is not a wedged deploy, and the
	// dashboard already flags it as awaiting approval.
	if ps.PendingApproval() != nil {
		return false, "", "", ""
	}
	for _, st := range ps.Stages {
		if st.Status == health.StatusInProgress {
			return true, "pipeline_in_progress", string(st.Status), "stage " + st.Name
		}
	}
	return false, "", "", ""
}

// ecsStuckDetail describes a stuck service for the webhook: its task counts,
// plus the Task detail when failing tasks are what make it stuck.
func ecsStuckDetail(sv state.ECSServiceState) string {
	d := fmt.Sprintf("%d running / %d desired (%d pending)", sv.RunningCount, sv.DesiredCount, sv.PendingCount)
	if sv.FailingTaskCount > 0 {
		d += fmt.Sprintf(", %d failing: %s", sv.FailingTaskCount, sv.StoppedReason)
	}
	return d
}
