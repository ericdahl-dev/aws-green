package fix_test

import (
	"testing"
	"time"

	"github.com/ericdahl-dev/aws-green/internal/fix"
	"github.com/ericdahl-dev/aws-green/internal/health"
	"github.com/ericdahl-dev/aws-green/internal/state"
)

func proj(pipeline health.Stoplight, stacks []state.StackState, ecs []state.ECSServiceState) state.ProjectState {
	return state.ProjectState{
		Name:        "my-app",
		Pipeline:    state.PipelineState{Name: "my-pipeline", Stoplight: pipeline},
		Stacks:      stacks,
		ECSServices: ecs,
	}
}

func TestPlan_noActionWhenGreen(t *testing.T) {
	p := proj(health.StoplightGreen, nil, nil)
	plan := fix.Plan(p, 30*time.Minute)
	if plan != nil {
		t.Errorf("expected nil plan for green project, got %v", plan)
	}
}

func TestPlan_noActionWhenGray(t *testing.T) {
	p := proj(health.StoplightGray, nil, nil)
	plan := fix.Plan(p, 30*time.Minute)
	if plan != nil {
		t.Errorf("expected nil plan for gray project, got %v", plan)
	}
}

func TestPlan_restartFailedPipeline(t *testing.T) {
	p := proj(health.StoplightRed, nil, nil)
	plan := fix.Plan(p, 30*time.Minute)
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
	p := proj(health.StoplightGreen, nil, []state.ECSServiceState{
		{Name: "web", Cluster: "my-cluster", RunningCount: 0, DesiredCount: 3, Stoplight: health.StoplightRed},
	})
	plan := fix.Plan(p, 30*time.Minute)
	if plan == nil {
		t.Fatal("expected plan for red ECS service")
	}
	if plan.Kind != fix.KindForceDeployECS {
		t.Errorf("expected KindForceDeployECS, got %v", plan.Kind)
	}
}

func TestPlan_forceDeployStalledECS(t *testing.T) {
	p := proj(health.StoplightGreen, nil, []state.ECSServiceState{
		{Name: "web", Cluster: "my-cluster", ActiveDeployment: true, RunningCount: 2, DesiredCount: 2, Stoplight: health.StoplightYellow},
	})
	plan := fix.Plan(p, 30*time.Minute)
	if plan == nil {
		t.Fatal("expected plan for yellow ECS service")
	}
	if plan.Kind != fix.KindForceDeployECS {
		t.Errorf("expected KindForceDeployECS, got %v", plan.Kind)
	}
}

func TestPlan_continueRollbackStack(t *testing.T) {
	p := proj(health.StoplightGreen, []state.StackState{
		{Name: "my-stack", Status: "UPDATE_ROLLBACK_FAILED", Stoplight: health.StoplightRed},
	}, nil)
	plan := fix.Plan(p, 30*time.Minute)
	if plan == nil {
		t.Fatal("expected plan for failed rollback stack")
	}
	if plan.Kind != fix.KindContinueRollback {
		t.Errorf("expected KindContinueRollback, got %v", plan.Kind)
	}
}

func TestPlan_cancelStalledStack(t *testing.T) {
	stale := time.Now().Add(-35 * time.Minute)
	p := proj(health.StoplightGreen, []state.StackState{
		{Name: "my-stack", Status: "UPDATE_IN_PROGRESS", StartedAt: &stale, Stoplight: health.StoplightYellow},
	}, nil)
	plan := fix.Plan(p, 30*time.Minute)
	if plan == nil {
		t.Fatal("expected plan for stalled stack")
	}
	if plan.Kind != fix.KindCancelStackUpdate {
		t.Errorf("expected KindCancelStackUpdate, got %v", plan.Kind)
	}
}

// CancelUpdateStack only accepts UPDATE_IN_PROGRESS, so any other long-running
// operation has no cancel to offer.
func TestPlan_noCancelForStalledNonUpdate(t *testing.T) {
	stale := time.Now().Add(-35 * time.Minute)
	for _, status := range []health.StackStatus{"CREATE_IN_PROGRESS", "UPDATE_ROLLBACK_IN_PROGRESS", "REVIEW_IN_PROGRESS"} {
		p := proj(health.StoplightGreen, []state.StackState{
			{Name: "my-stack", Status: status, StartedAt: &stale, Stoplight: status.Stoplight()},
		}, nil)
		if plan := fix.Plan(p, 30*time.Minute); plan != nil {
			t.Errorf("%s: expected no plan, got %v", status, plan.Kind)
		}
	}
}

func TestPlan_pipelineTakesPrecedenceOverECS(t *testing.T) {
	p := proj(health.StoplightRed, nil, []state.ECSServiceState{
		{Name: "web", Cluster: "c", RunningCount: 0, DesiredCount: 1, Stoplight: health.StoplightRed},
	})
	plan := fix.Plan(p, 30*time.Minute)
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
			Stoplight: health.StoplightRed,
		},
	}
	plan := fix.Plan(p, 30*time.Minute)
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

// The cancel fix is offered at the same threshold that makes the stack
// Stuck — the user's stuck_threshold_minutes — not a fixed 30 minutes.
func TestPlan_stalledStackUsesTheGivenThreshold(t *testing.T) {
	started := time.Now().Add(-12 * time.Minute)
	p := proj(health.StoplightGreen, []state.StackState{
		{Name: "my-stack", Status: "UPDATE_IN_PROGRESS", StartedAt: &started, Stoplight: health.StoplightYellow},
	}, nil)
	if plan := fix.Plan(p, 10*time.Minute); plan == nil || plan.Kind != fix.KindCancelStackUpdate {
		t.Errorf("12m into a 10m threshold: got %+v, want a cancel plan", plan)
	}
	if plan := fix.Plan(p, 15*time.Minute); plan != nil {
		t.Errorf("12m into a 15m threshold: got %+v, want no plan", plan)
	}
}
