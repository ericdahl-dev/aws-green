package fix_test

import (
	"testing"
	"time"

	"github.com/ericdahl-dev/aws-green/internal/aggregator"
	"github.com/ericdahl-dev/aws-green/internal/fix"
	"github.com/ericdahl-dev/aws-green/internal/state"
)

func proj(pipeline aggregator.Stoplight, stacks []state.StackState, ecs []state.ECSServiceState) state.ProjectState {
	return state.ProjectState{
		Name:        "my-app",
		Pipeline:    state.PipelineState{Name: "my-pipeline", Stoplight: pipeline},
		Stacks:      stacks,
		ECSServices: ecs,
	}
}

func TestPlan_noActionWhenGreen(t *testing.T) {
	p := proj(aggregator.StoplightGreen, nil, nil)
	plan := fix.Plan(p)
	if plan != nil {
		t.Errorf("expected nil plan for green project, got %v", plan)
	}
}

func TestPlan_noActionWhenGrey(t *testing.T) {
	p := proj(aggregator.StoplightGrey, nil, nil)
	plan := fix.Plan(p)
	if plan != nil {
		t.Errorf("expected nil plan for grey project, got %v", plan)
	}
}

func TestPlan_restartFailedPipeline(t *testing.T) {
	p := proj(aggregator.StoplightRed, nil, nil)
	plan := fix.Plan(p)
	if plan == nil {
		t.Fatal("expected plan for red pipeline")
	}
	if plan.Kind != fix.KindRestartPipeline {
		t.Errorf("expected KindRestartPipeline, got %v", plan.Kind)
	}
	if plan.Description == "" {
		t.Error("expected non-empty description")
	}
}

func TestPlan_forceDeployDownECS(t *testing.T) {
	p := proj(aggregator.StoplightGreen, nil, []state.ECSServiceState{
		{Name: "web", Cluster: "my-cluster", RunningCount: 0, DesiredCount: 3, Stoplight: aggregator.StoplightRed},
	})
	plan := fix.Plan(p)
	if plan == nil {
		t.Fatal("expected plan for red ECS service")
	}
	if plan.Kind != fix.KindForceDeployECS {
		t.Errorf("expected KindForceDeployECS, got %v", plan.Kind)
	}
}

func TestPlan_forceDeployStalledECS(t *testing.T) {
	p := proj(aggregator.StoplightGreen, nil, []state.ECSServiceState{
		{Name: "web", Cluster: "my-cluster", ActiveDeployment: true, RunningCount: 2, DesiredCount: 2, Stoplight: aggregator.StoplightYellow},
	})
	plan := fix.Plan(p)
	if plan == nil {
		t.Fatal("expected plan for yellow ECS service")
	}
	if plan.Kind != fix.KindForceDeployECS {
		t.Errorf("expected KindForceDeployECS, got %v", plan.Kind)
	}
}

func TestPlan_continueRollbackStack(t *testing.T) {
	p := proj(aggregator.StoplightGreen, []state.StackState{
		{Name: "my-stack", Status: "UPDATE_ROLLBACK_FAILED", Stoplight: aggregator.StoplightRed},
	}, nil)
	plan := fix.Plan(p)
	if plan == nil {
		t.Fatal("expected plan for failed rollback stack")
	}
	if plan.Kind != fix.KindContinueRollback {
		t.Errorf("expected KindContinueRollback, got %v", plan.Kind)
	}
}

func TestPlan_cancelStalledStack(t *testing.T) {
	stale := time.Now().Add(-35 * time.Minute)
	p := proj(aggregator.StoplightGreen, []state.StackState{
		{Name: "my-stack", Status: "UPDATE_IN_PROGRESS", StartedAt: &stale, Stoplight: aggregator.StoplightYellow},
	}, nil)
	plan := fix.Plan(p)
	if plan == nil {
		t.Fatal("expected plan for stalled stack")
	}
	if plan.Kind != fix.KindCancelStackUpdate {
		t.Errorf("expected KindCancelStackUpdate, got %v", plan.Kind)
	}
}

func TestPlan_pipelineTakesPrecedenceOverECS(t *testing.T) {
	p := proj(aggregator.StoplightRed, nil, []state.ECSServiceState{
		{Name: "web", Cluster: "c", RunningCount: 0, DesiredCount: 1, Stoplight: aggregator.StoplightRed},
	})
	plan := fix.Plan(p)
	if plan == nil {
		t.Fatal("expected plan")
	}
	if plan.Kind != fix.KindRestartPipeline {
		t.Errorf("pipeline should take precedence, got %v", plan.Kind)
	}
}

func TestPlan_credentialsFromProject(t *testing.T) {
	p := state.ProjectState{
		Name:    "my-app",
		Profile: "prod-profile",
		Region:  "us-east-1",
		Pipeline: state.PipelineState{
			Name:      "my-pipeline",
			Stoplight: aggregator.StoplightRed,
		},
	}
	plan := fix.Plan(p)
	if plan == nil {
		t.Fatal("expected plan for red pipeline")
	}
	if plan.Profile != "prod-profile" {
		t.Errorf("expected Profile=prod-profile, got %q", plan.Profile)
	}
	if plan.Region != "us-east-1" {
		t.Errorf("expected Region=us-east-1, got %q", plan.Region)
	}
}
