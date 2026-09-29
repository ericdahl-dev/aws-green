package state_test

import (
	"testing"

	"github.com/ericdahl-dev/aws-green/internal/health"
	"github.com/ericdahl-dev/aws-green/internal/state"
)

func TestProjectState_stoplightFromPipeline(t *testing.T) {
	ps := state.PipelineState{
		Name:      "my-pipeline",
		Stoplight: health.StoplightGreen,
	}
	proj := state.ProjectState{
		Name:     "my-project",
		Pipeline: ps,
	}
	if proj.Stoplight() != health.StoplightGreen {
		t.Errorf("expected green, got %v", proj.Stoplight())
	}
}

func TestProjectState_stoplightGrey_noPipeline(t *testing.T) {
	proj := state.ProjectState{
		Name: "empty-project",
	}
	if proj.Stoplight() != health.StoplightGrey {
		t.Errorf("expected grey, got %v", proj.Stoplight())
	}
}

func TestSnapshot_projects(t *testing.T) {
	projects := []state.ProjectState{
		{Name: "a", Pipeline: state.PipelineState{Stoplight: health.StoplightRed}},
		{Name: "b", Pipeline: state.PipelineState{Stoplight: health.StoplightGreen}},
	}
	snap := state.NewFromProjects(projects)
	if len(snap.Projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(snap.Projects))
	}
	if snap.Projects[0].Name != "a" {
		t.Errorf("expected first project a, got %s", snap.Projects[0].Name)
	}
}

func TestProjectState_stoplightWorstCaseFromStacks(t *testing.T) {
	proj := state.ProjectState{
		Name:     "p",
		Pipeline: state.PipelineState{Stoplight: health.StoplightGreen},
		Stacks: []state.StackState{
			{Name: "s1", Stoplight: health.StoplightRed},
		},
	}
	if proj.Stoplight() != health.StoplightRed {
		t.Errorf("expected red (from stack), got %v", proj.Stoplight())
	}
}

func TestProjectState_stoplightWorstCaseFromECS(t *testing.T) {
	proj := state.ProjectState{
		Name:     "p",
		Pipeline: state.PipelineState{Stoplight: health.StoplightGreen},
		Stacks:   []state.StackState{{Stoplight: health.StoplightGreen}},
		ECSServices: []state.ECSServiceState{
			{Name: "web", Stoplight: health.StoplightYellow},
		},
	}
	if proj.Stoplight() != health.StoplightYellow {
		t.Errorf("expected yellow (from ECS), got %v", proj.Stoplight())
	}
}

func TestProjectState_stoplightAllGreen(t *testing.T) {
	proj := state.ProjectState{
		Name:     "p",
		Pipeline: state.PipelineState{Stoplight: health.StoplightGreen},
		Stacks:   []state.StackState{{Stoplight: health.StoplightGreen}},
		ECSServices: []state.ECSServiceState{
			{Name: "web", Stoplight: health.StoplightGreen},
		},
	}
	if proj.Stoplight() != health.StoplightGreen {
		t.Errorf("expected green, got %v", proj.Stoplight())
	}
}

func approvalStages(token string) []state.StageState {
	return []state.StageState{
		{Name: "Source", Status: health.StatusSucceeded, Actions: []state.ActionState{
			{Name: "Checkout", Status: health.StatusSucceeded},
		}},
		{Name: "Test", Status: health.StatusInProgress, Actions: []state.ActionState{
			{Name: "Approve", Status: health.StatusInProgress, ApprovalToken: token},
		}},
	}
}

func TestNewPipeline_pendingApprovalSetsStoplight(t *testing.T) {
	ps := state.NewPipeline("my-pipeline", approvalStages("tok-123"))
	if ps.Stoplight != health.StoplightAwaitingApproval {
		t.Errorf("stoplight = %v, want awaiting approval", ps.Stoplight)
	}
	pa := ps.PendingApproval()
	if pa == nil {
		t.Fatal("expected a pending approval")
	}
	if pa.StageName != "Test" || pa.ActionName != "Approve" || pa.Token != "tok-123" {
		t.Errorf("pending approval = %+v", *pa)
	}
	if !ps.Stages[1].Actions[0].AwaitingApproval() {
		t.Error("expected the action to report AwaitingApproval")
	}
}

func TestNewPipeline_noTokenIsPlainInProgress(t *testing.T) {
	ps := state.NewPipeline("my-pipeline", approvalStages(""))
	if ps.Stoplight != health.StoplightYellow {
		t.Errorf("stoplight = %v, want yellow", ps.Stoplight)
	}
	if ps.PendingApproval() != nil {
		t.Error("expected no pending approval")
	}
}

// Each resource kind has its own worst-of, shown as its own summary on the
// project row; the project's Stoplight is the worst of those.
func TestProjectState_perKindStoplights(t *testing.T) {
	proj := state.ProjectState{
		Pipeline: state.PipelineState{Stoplight: health.StoplightGreen},
		Stacks: []state.StackState{
			{Stoplight: health.StoplightGreen}, {Stoplight: health.StoplightRed},
		},
		ECSServices: []state.ECSServiceState{
			{Stoplight: health.StoplightYellow}, {Stoplight: health.StoplightGreen},
		},
	}
	if got := proj.StacksStoplight(); got != health.StoplightRed {
		t.Errorf("StacksStoplight() = %v, want red", got)
	}
	if got := proj.ECSStoplight(); got != health.StoplightYellow {
		t.Errorf("ECSStoplight() = %v, want yellow", got)
	}
	if got := (state.ProjectState{}).StacksStoplight(); got != health.StoplightGrey {
		t.Errorf("no stacks: StacksStoplight() = %v, want grey", got)
	}
}
