package ui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericdahl-dev/aws-green/internal/fix"
	"github.com/ericdahl-dev/aws-green/internal/health"
	"github.com/ericdahl-dev/aws-green/internal/state"
)

func approvalSnapshot(token string) state.Snapshot {
	return state.NewFromProjects([]state.ProjectState{{
		Name:    "annex-ims",
		Account: "libnd",
		Pipeline: state.PipelineState{
			Account:   "libnd",
			Name:      "annex-pipeline",
			Stoplight: health.StoplightAwaitingApproval,
			Stages: []state.StageState{
				{Name: "Test", Status: health.StatusInProgress, Actions: []state.ActionState{
					{Name: "Approve", Status: health.StatusInProgress, ApprovalToken: token},
				}},
			},
		},
	}})
}

func key(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestApproveKeyStartsConfirm(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want fix.Kind
	}{{"a", fix.KindApprove}, {"x", fix.KindReject}} {
		d := NewDashboard(approvalSnapshot("tok"), nil, context.Background())
		d, _ = d.Update(key(tc.key))
		if d.fixStatus != fixConfirming || d.fixPlan == nil || d.fixPlan.Kind != tc.want {
			t.Errorf("%q: status=%v plan=%+v, want confirming %v", tc.key, d.fixStatus, d.fixPlan, tc.want)
		}
	}
}

func TestApproveKeyIgnoredWithoutPendingApproval(t *testing.T) {
	d := NewDashboard(approvalSnapshot(""), nil, context.Background())
	d, _ = d.Update(key("a"))
	if d.fixStatus != fixIdle {
		t.Errorf("status = %v, want idle", d.fixStatus)
	}
}

func TestStageRowMarksAwaitingApproval(t *testing.T) {
	d := NewDashboard(approvalSnapshot("tok"), nil, context.Background())
	proj := d.snapshot.Projects[0]
	if out := d.renderStages(proj, d.buildNavList(), -1); !strings.Contains(out, "⏸") {
		t.Errorf("expected ⏸ on the waiting stage, got %q", out)
	}
}

// A failed outcome renders as an error and doesn't re-poll.
func TestFailedFixRendersAsError(t *testing.T) {
	d := NewDashboard(approvalSnapshot("tok"), nil, context.Background())
	d, _ = d.Update(key("a"))
	d, cmd := d.Update(fixDoneMsg{fix.Outcome{Message: "fix failed: throttled", Failed: true}})
	if !d.fixErr || d.fixResultMsg != "fix failed: throttled" {
		t.Errorf("result = %q (err=%v), want the failure shown as an error", d.fixResultMsg, d.fixErr)
	}
	for _, m := range runCmd(cmd, 0) {
		if _, ok := m.(FixAppliedMsg); ok {
			t.Error("a failed fix should not re-poll")
		}
	}
}

// recordingActioner records the approval decision it was asked to send.
type recordingActioner struct {
	fix.Actioner
	approved *bool
	token    string
}

func (r *recordingActioner) PutApprovalResult(_ context.Context, _, _, _, token string, approved bool, _ string) error {
	r.token, r.approved = token, &approved
	return nil
}

// Confirming a decision sends it to AWS for the project's account, shows the
// result, and asks for a re-poll.
func TestConfirmApprovalSendsItAndRefreshes(t *testing.T) {
	rec := &recordingActioner{}
	var gotProfile string
	factory := func(profile, _ string) (fix.Actioner, error) {
		gotProfile = profile
		return rec, nil
	}
	snap := approvalSnapshot("tok")
	snap.Projects[0].Profile = "libnd-admin"
	d := NewDashboard(snap, factory, context.Background())

	d, _ = d.Update(key("a"))
	d, cmd := d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	var msgs []tea.Msg
	for _, m := range runCmd(cmd, 0) {
		var next tea.Cmd
		d, next = d.Update(m)
		msgs = append(msgs, runCmd(next, 0)...)
	}

	if rec.approved == nil || !*rec.approved || rec.token != "tok" || gotProfile != "libnd-admin" {
		t.Fatalf("approval not sent as expected: approved=%v token=%q profile=%q", rec.approved, rec.token, gotProfile)
	}
	if d.fixErr || d.fixResultMsg != "✓ approve" {
		t.Errorf("result = %q (err=%v), want ✓ approve", d.fixResultMsg, d.fixErr)
	}
	refreshed := false
	for _, m := range msgs {
		if _, ok := m.(FixAppliedMsg); ok {
			refreshed = true
		}
	}
	if !refreshed {
		t.Errorf("expected a FixAppliedMsg re-poll, got %v", msgs)
	}
}
