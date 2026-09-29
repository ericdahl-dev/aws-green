package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ericdahl-dev/aws-green/internal/health"
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

// A failed stack fetch must not look like "this project has no stacks". The
// section still renders, carrying the error and — for a credential failure —
// the login hint.
func TestStacksSectionSurfacesFetchError(t *testing.T) {
	staleAt := time.Now()
	proj := state.ProjectState{
		Name:        "annex-ims",
		Account:     "libnd",
		Profile:     "libnd-view",
		StacksFetch: state.FetchStatus{StaleAt: &staleAt, Err: errors.New("sso session has expired")},
	}

	out := renderStacksSection(proj)
	if out == "" {
		t.Fatal("expected the stacks section to render despite having no stacks")
	}
	if !strings.Contains(out, "sso session has expired") {
		t.Errorf("expected the error in the section, got %q", out)
	}
	if !strings.Contains(out, "aws sso login --profile libnd-view") {
		t.Errorf("expected the login hint for an auth error, got %q", out)
	}
}

func TestECSSectionSurfacesFetchError(t *testing.T) {
	staleAt := time.Now()
	proj := state.ProjectState{
		Name:     "annex-ims",
		Account:  "libnd",
		ECSFetch: state.FetchStatus{StaleAt: &staleAt, Err: errors.New("ClusterNotFoundException")},
	}

	out := renderECSSection(proj)
	if !strings.Contains(out, "ClusterNotFoundException") {
		t.Errorf("expected the error in the section, got %q", out)
	}
	if strings.Contains(out, "aws sso login") {
		t.Errorf("expected no login hint for a non-auth error, got %q", out)
	}
}

// Without a marker on the collapsed row there is nothing to tell the user the
// numbers they are reading are stale.
func TestProjectRowMarksStaleStackAndECSFetches(t *testing.T) {
	staleAt := time.Now()
	proj := state.ProjectState{
		Name:        "annex-ims",
		Account:     "libnd",
		Stacks:      []state.StackState{{Name: "annex-ims-stack", Status: "UPDATE_COMPLETE"}},
		StacksFetch: state.FetchStatus{StaleAt: &staleAt, Err: errors.New("throttled")},
		ECSServices: []state.ECSServiceState{{Name: "web", Cluster: "prod", RunningCount: 2, DesiredCount: 2}},
		ECSFetch:    state.FetchStatus{StaleAt: &staleAt, Err: errors.New("sso token expired")},
	}

	row := projectRow(proj)
	if strings.Count(row, "⚠ stale") != 2 {
		t.Errorf("expected both sections marked stale, got %q", row)
	}
	// The pipeline fetch was fine; the auth hint has to come from the ECS error.
	if !strings.Contains(row, "auth error") {
		t.Errorf("expected the auth hint from the ECS failure, got %q", row)
	}
}

// The hint is a single per-row nudge, not one per failing fetch.
func TestProjectRowShowsOneAuthHint(t *testing.T) {
	staleAt := time.Now()
	proj := state.ProjectState{
		Name:        "annex-ims",
		Account:     "libnd",
		Pipeline:    state.PipelineState{FetchStatus: state.FetchStatus{StaleAt: &staleAt, Err: errors.New("sso token expired")}},
		StacksFetch: state.FetchStatus{StaleAt: &staleAt, Err: errors.New("sso token expired")},
		ECSFetch:    state.FetchStatus{StaleAt: &staleAt, Err: errors.New("sso token expired")},
	}

	if got := strings.Count(projectRow(proj), "auth error"); got != 1 {
		t.Errorf("expected exactly 1 auth hint, got %d in %q", got, projectRow(proj))
	}
}

// The collapsed row calls out the first stack that failed or is rolling back,
// so a broken stack is visible without expanding the project.
func TestProjectRowFlagsStackTrouble(t *testing.T) {
	cases := []struct {
		status health.StackStatus
		want   string
	}{
		{"UPDATE_ROLLBACK_FAILED", "✗ rollback failed"},
		{"CREATE_FAILED", "✗ create failed"},
		{"ROLLBACK_IN_PROGRESS", "⚠ rolling back"},
		{"UPDATE_ROLLBACK_IN_PROGRESS", "⚠ rolling back"},
	}
	for _, tc := range cases {
		proj := state.ProjectState{
			Name:   "annex-ims",
			Stacks: []state.StackState{{Name: "s", Status: tc.status, Stoplight: tc.status.Stoplight()}},
		}
		row := projectRow(proj)
		if !strings.Contains(row, tc.want) {
			t.Errorf("%s: expected %q in %q", tc.status, tc.want, row)
		}
		if !strings.Contains(row, "Stacks "+tc.status.Stoplight().String()) {
			t.Errorf("%s: expected stack summary %s in %q", tc.status, tc.status.Stoplight(), row)
		}
	}
}

// The login hint names the credential profile, not the Account: they differ
// (Account libnd uses profile libnd-view), and the Account name isn't a
// profile aws can log in to.
func TestLoginHintNamesTheProfile(t *testing.T) {
	staleAt := time.Now()
	expired := state.FetchStatus{StaleAt: &staleAt, Err: errors.New("the SSO session has expired")}
	proj := state.ProjectState{
		Name:        "annex-ims",
		Account:     "libnd",
		Profile:     "libnd-view",
		Pipeline:    state.PipelineState{FetchStatus: expired},
		StacksFetch: expired,
		ECSFetch:    expired,
	}
	d := NewDashboard(state.NewFromProjects([]state.ProjectState{proj}), nil, context.Background())

	for name, out := range map[string]string{
		"pipeline": d.renderStages(proj, d.buildNavList(), -1),
		"stacks":   renderStacksSection(proj),
		"ecs":      renderECSSection(proj),
	} {
		if !strings.Contains(out, "aws sso login --profile libnd-view") {
			t.Errorf("%s: expected the libnd-view profile in the hint, got %q", name, out)
		}
	}

	// A project with no Account uses the default credential chain.
	proj.Account, proj.Profile = "", ""
	if out := renderStacksSection(proj); !strings.Contains(out, "aws sso login") || strings.Contains(out, "--profile") {
		t.Errorf("default credentials: expected a plain aws sso login, got %q", out)
	}
}
