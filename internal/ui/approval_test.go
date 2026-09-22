package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericdahl-dev/aws-green/internal/aggregator"
	"github.com/ericdahl-dev/aws-green/internal/fix"
	"github.com/ericdahl-dev/aws-green/internal/state"
)

func approvalSnapshot(token string) state.Snapshot {
	return state.NewFromProjects([]state.ProjectState{{
		Name:    "annex-ims",
		Account: "libnd",
		Pipeline: state.PipelineState{
			Account:   "libnd",
			Name:      "annex-pipeline",
			Stoplight: aggregator.StoplightAwaitingApproval,
			Stages: []state.StageState{
				{Name: "Test", Status: aggregator.StatusInProgress, Actions: []state.ActionState{
					{Name: "Approve", Status: aggregator.StatusInProgress, ApprovalToken: token},
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

// A stale token means someone got there first. That is not a failure; say so
// and re-poll so the dashboard catches up.
func TestApprovalAlreadyDecidedIsNotAFailure(t *testing.T) {
	d := NewDashboard(approvalSnapshot("tok"), nil, context.Background())
	d, _ = d.Update(key("a"))
	d, cmd := d.Update(fixDoneMsg{err: fmt.Errorf("put: %w", fix.ErrApprovalAlreadyDecided)})
	if d.fixErr {
		t.Error("already-decided should not render as an error")
	}
	if !strings.Contains(d.fixResultMsg, "already decided") {
		t.Errorf("result = %q, want it to say already decided", d.fixResultMsg)
	}
	if cmd == nil {
		t.Fatal("expected a re-poll command")
	}
}

func TestStageRowMarksAwaitingApproval(t *testing.T) {
	d := NewDashboard(approvalSnapshot("tok"), nil, context.Background())
	proj := d.snapshot.Projects[0]
	if out := d.renderStages(proj, d.buildNavList(), -1); !strings.Contains(out, "⏸") {
		t.Errorf("expected ⏸ on the waiting stage, got %q", out)
	}
}
