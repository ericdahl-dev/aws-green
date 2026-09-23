package ui

import (
	"context"
	"testing"

	"github.com/ericdahl-dev/aws-green/internal/aggregator"
	"github.com/ericdahl-dev/aws-green/internal/state"
)

// snapWith builds a one-project snapshot at the given stoplight, with stages
// so there is something to expand into.
func snapWith(light aggregator.Stoplight) state.Snapshot {
	return state.NewFromProjects([]state.ProjectState{{
		Name:    "annex-ims",
		Account: "libnd",
		Pipeline: state.PipelineState{
			Account:   "libnd",
			Name:      "annex-pipeline",
			Stoplight: light,
			Stages: []state.StageState{
				{Name: "Source", Status: aggregator.StatusSucceeded},
				{Name: "Deploy", Status: aggregator.StatusInProgress},
			},
		},
	}})
}

const projKey = "libnd/annex-ims"

func TestAutoExpandOnFirstSighting(t *testing.T) {
	for _, tc := range []struct {
		light aggregator.Stoplight
		want  bool
	}{
		{aggregator.StoplightRed, true},
		{aggregator.StoplightYellow, true},
		{aggregator.StoplightAwaitingApproval, true},
		{aggregator.StoplightGreen, false},
		{aggregator.StoplightGrey, false},
	} {
		d := NewDashboard(snapWith(tc.light), nil, context.Background())
		if got := d.expanded[projKey]; got != tc.want {
			t.Errorf("%v: expanded = %v, want %v", tc.light, got, tc.want)
		}
	}
}

func TestAutoExpandOnStatusChange(t *testing.T) {
	d := NewDashboard(snapWith(aggregator.StoplightGreen), nil, context.Background())
	if d.expanded[projKey] {
		t.Fatal("green project should start collapsed")
	}

	d, _ = d.Update(snapWith(aggregator.StoplightRed))
	if !d.expanded[projKey] {
		t.Error("going red should expand the row")
	}

	d, _ = d.Update(snapWith(aggregator.StoplightGreen))
	if d.expanded[projKey] {
		t.Error("recovering should collapse the row")
	}
}

// Polling must never fight the user: a hand-collapsed red row stays collapsed
// while its status is unchanged.
func TestManualCollapseSurvivesIdenticalSnapshots(t *testing.T) {
	d := NewDashboard(snapWith(aggregator.StoplightRed), nil, context.Background())
	d.expanded[projKey] = false

	for i := 0; i < 3; i++ {
		d, _ = d.Update(snapWith(aggregator.StoplightRed))
		if d.expanded[projKey] {
			t.Fatalf("poll %d re-expanded a row the user collapsed", i)
		}
	}
}

// ...but a genuine status change is a new event, so it expands again.
func TestStatusChangeOverridesManualCollapse(t *testing.T) {
	d := NewDashboard(snapWith(aggregator.StoplightYellow), nil, context.Background())
	d.expanded[projKey] = false

	d, _ = d.Update(snapWith(aggregator.StoplightRed))
	if !d.expanded[projKey] {
		t.Error("a new status should expand the row again")
	}
}

// Auto-expansion inserts rows above the cursor, so the selection has to land
// on the same logical row it was on before.
func TestCursorStaysOnSameRowWhenAProjectAboveExpands(t *testing.T) {
	two := func(first aggregator.Stoplight) state.Snapshot {
		mk := func(account string, light aggregator.Stoplight) state.ProjectState {
			return state.ProjectState{
				Name:    "annex-ims",
				Account: account,
				Pipeline: state.PipelineState{
					Account:   account,
					Name:      account + "-pipeline",
					Stoplight: light,
					Stages: []state.StageState{
						{Name: "Source", Status: aggregator.StatusSucceeded},
						{Name: "Deploy", Status: aggregator.StatusSucceeded},
					},
				},
			}
		}
		return state.NewFromProjects([]state.ProjectState{mk("libnd", first), mk("testlibnd", aggregator.StoplightGreen)})
	}

	d := NewDashboard(two(aggregator.StoplightGreen), nil, context.Background())
	// Park on the second project row.
	d.cursor = 1
	before := d.currentNavItem()
	if before == nil || before.projKey != "testlibnd/annex-ims" {
		t.Fatalf("setup: cursor on %+v, want the testlibnd row", before)
	}

	// The project above goes red and expands, pushing two stage rows in.
	d, _ = d.Update(two(aggregator.StoplightRed))

	after := d.currentNavItem()
	if after == nil || after.kind != navProject || after.projKey != "testlibnd/annex-ims" {
		t.Errorf("cursor moved to %+v, want the testlibnd project row", after)
	}
}

// A project that leaves the config and comes back is a first sighting again,
// not a stale status carried forward.
func TestRemovedProjectIsForgotten(t *testing.T) {
	d := NewDashboard(snapWith(aggregator.StoplightRed), nil, context.Background())
	d, _ = d.Update(state.NewFromProjects(nil))
	if len(d.lastStoplight) != 0 {
		t.Errorf("expected stoplight bookkeeping to be pruned, got %v", d.lastStoplight)
	}
}
