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
