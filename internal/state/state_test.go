package state_test

import (
	"testing"

	"github.com/ericdahl-dev/aws-green/internal/aggregator"
	"github.com/ericdahl-dev/aws-green/internal/state"
)

func TestProjectState_stoplightFromPipeline(t *testing.T) {
	ps := state.PipelineState{
		Name:      "my-pipeline",
		Stoplight: aggregator.StoplightGreen,
	}
	proj := state.ProjectState{
		Name:     "my-project",
		Pipeline: ps,
	}
	if proj.Stoplight() != aggregator.StoplightGreen {
		t.Errorf("expected green, got %v", proj.Stoplight())
	}
}

func TestProjectState_stoplightGrey_noPipeline(t *testing.T) {
	proj := state.ProjectState{
		Name: "empty-project",
	}
	if proj.Stoplight() != aggregator.StoplightGrey {
		t.Errorf("expected grey, got %v", proj.Stoplight())
	}
}

func TestSnapshot_projects(t *testing.T) {
	projects := []state.ProjectState{
		{Name: "a", Pipeline: state.PipelineState{Stoplight: aggregator.StoplightRed}},
		{Name: "b", Pipeline: state.PipelineState{Stoplight: aggregator.StoplightGreen}},
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
		Pipeline: state.PipelineState{Stoplight: aggregator.StoplightGreen},
		Stacks: []state.StackState{
			{Name: "s1", Stoplight: aggregator.StoplightRed},
		},
	}
	if proj.Stoplight() != aggregator.StoplightRed {
		t.Errorf("expected red (from stack), got %v", proj.Stoplight())
	}
}

func TestProjectState_stoplightWorstCaseFromECS(t *testing.T) {
	proj := state.ProjectState{
		Name:     "p",
		Pipeline: state.PipelineState{Stoplight: aggregator.StoplightGreen},
		Stacks:   []state.StackState{{Stoplight: aggregator.StoplightGreen}},
		ECSServices: []state.ECSServiceState{
			{Name: "web", Stoplight: aggregator.StoplightYellow},
		},
	}
	if proj.Stoplight() != aggregator.StoplightYellow {
		t.Errorf("expected yellow (from ECS), got %v", proj.Stoplight())
	}
}

func TestProjectState_stoplightAllGreen(t *testing.T) {
	proj := state.ProjectState{
		Name:     "p",
		Pipeline: state.PipelineState{Stoplight: aggregator.StoplightGreen},
		Stacks:   []state.StackState{{Stoplight: aggregator.StoplightGreen}},
		ECSServices: []state.ECSServiceState{
			{Name: "web", Stoplight: aggregator.StoplightGreen},
		},
	}
	if proj.Stoplight() != aggregator.StoplightGreen {
		t.Errorf("expected green, got %v", proj.Stoplight())
	}
}
