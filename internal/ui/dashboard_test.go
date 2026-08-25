package ui

import (
	"context"
	"testing"

	"github.com/ericdahl-dev/aws-green/internal/state"
)

// sameNameProjects returns the same project configured in two accounts, which
// is the normal case when a repo has a pipeline in both test and prod.
func sameNameProjects() state.Snapshot {
	mk := func(account string, stages ...string) state.ProjectState {
		ss := make([]state.StageState, len(stages))
		for i, name := range stages {
			ss[i] = state.StageState{Name: name}
		}
		return state.ProjectState{
			Name:     "annex-ims",
			Account:  account,
			Pipeline: state.PipelineState{Account: account, Name: account + "-pipeline", Stages: ss},
		}
	}
	return state.NewFromProjects([]state.ProjectState{
		mk("libnd", "Source", "Test", "Production"),
		mk("testlibnd", "Source", "Test", "Production"),
	})
}

func newTestDashboard() Dashboard {
	return NewDashboard(sameNameProjects(), nil, context.Background())
}

func TestExpandingOneProjectLeavesItsTwinCollapsed(t *testing.T) {
	d := newTestDashboard()
	if got := len(d.buildNavList()); got != 2 {
		t.Fatalf("collapsed nav list = %d rows, want 2", got)
	}

	d.expanded[d.snapshot.Projects[0].Key()] = true

	items := d.buildNavList()
	// 2 project rows + 3 stages for the expanded one only.
	if len(items) != 5 {
		t.Fatalf("nav list = %d rows, want 5 (both twins expanded?)", len(items))
	}
	for _, it := range items {
		if it.kind == navStage && it.projKey != "libnd/annex-ims" {
			t.Errorf("stage row belongs to %q, want only libnd/annex-ims", it.projKey)
		}
	}
}

func TestSelectedProjectDistinguishesAccounts(t *testing.T) {
	d := newTestDashboard()

	seen := map[string]bool{}
	for cursor := 0; cursor < 2; cursor++ {
		d.cursor = cursor
		proj := d.selectedProject()
		if proj == nil {
			t.Fatalf("cursor %d: no selected project", cursor)
		}
		if seen[proj.Account] {
			t.Fatalf("cursor %d resolved to account %q again — rows are not distinguishable", cursor, proj.Account)
		}
		seen[proj.Account] = true
	}
	if !seen["libnd"] || !seen["testlibnd"] {
		t.Errorf("expected both accounts to be selectable, got %v", seen)
	}
}

func TestProjectKeyIsAccountQualified(t *testing.T) {
	p := state.ProjectState{Name: "annex-ims", Account: "libnd"}
	if got, want := p.Key(), "libnd/annex-ims"; got != want {
		t.Errorf("Key() = %q, want %q", got, want)
	}
	bare := state.ProjectState{Name: "annex-ims"}
	if got, want := bare.Key(), "annex-ims"; got != want {
		t.Errorf("Key() with no account = %q, want %q", got, want)
	}
}
