package fix_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ericdahl-dev/aws-green/internal/aggregator"
	"github.com/ericdahl-dev/aws-green/internal/fix"
	"github.com/ericdahl-dev/aws-green/internal/state"
)

func approvalProject(token string) state.ProjectState {
	return state.ProjectState{
		Name:    "my-app",
		Profile: "prod-profile",
		Region:  "us-east-1",
		Pipeline: state.PipelineState{
			Name:      "my-pipeline",
			Stoplight: aggregator.StoplightAwaitingApproval,
			Stages: []state.StageState{
				{Name: "Source", Status: aggregator.StatusSucceeded},
				{Name: "Test", Status: aggregator.StatusInProgress, Actions: []state.ActionState{
					{Name: "Approve", Status: aggregator.StatusInProgress, ApprovalToken: token},
				}},
			},
		},
	}
}

func TestPlanApproval_nilWithoutPendingApproval(t *testing.T) {
	if plan := fix.PlanApproval(approvalProject(""), true); plan != nil {
		t.Errorf("expected nil plan, got %+v", plan)
	}
}

func TestPlanApproval_approve(t *testing.T) {
	plan := fix.PlanApproval(approvalProject("tok-123"), true)
	if plan == nil {
		t.Fatal("expected a plan")
	}
	if plan.Kind != fix.KindApprove {
		t.Errorf("kind = %v, want approve", plan.Kind)
	}
	if plan.PipelineName != "my-pipeline" || plan.StageName != "Test" || plan.ActionName != "Approve" || plan.ApprovalToken != "tok-123" {
		t.Errorf("plan = %+v", plan)
	}
	if plan.Profile != "prod-profile" || plan.Region != "us-east-1" {
		t.Errorf("credentials not carried: %+v", plan)
	}
	if plan.Description == "" {
		t.Error("expected a description")
	}
}

func TestPlanApproval_reject(t *testing.T) {
	plan := fix.PlanApproval(approvalProject("tok-123"), false)
	if plan == nil || plan.Kind != fix.KindReject {
		t.Fatalf("expected reject plan, got %+v", plan)
	}
}

// Approvals are driven by their own keys; the smart fix must not approve
// anything on the user's behalf.
func TestPlan_doesNotApprove(t *testing.T) {
	if plan := fix.Plan(approvalProject("tok-123")); plan != nil {
		t.Errorf("expected no smart fix for a pending approval, got %+v", plan)
	}
}

type fakeActioner struct {
	pipeline, stage, action, token string
	approved                       bool
	summary                        string
	err                            error
}

func (f *fakeActioner) RestartPipeline(context.Context, string) error   { return nil }
func (f *fakeActioner) ContinueRollback(context.Context, string) error  { return nil }
func (f *fakeActioner) CancelStackUpdate(context.Context, string) error { return nil }
func (f *fakeActioner) ForceDeployECS(context.Context, string, string) error {
	return nil
}
func (f *fakeActioner) PutApprovalResult(_ context.Context, pipeline, stage, action, token string, approved bool, summary string) error {
	f.pipeline, f.stage, f.action, f.token, f.approved, f.summary = pipeline, stage, action, token, approved, summary
	return f.err
}

func TestExecute_approvalPassesIdentityAndDefaultSummary(t *testing.T) {
	for _, tc := range []struct {
		approve bool
		summary string
	}{
		{true, "Approved via aws-green"},
		{false, "Rejected via aws-green"},
	} {
		a := &fakeActioner{}
		plan := fix.PlanApproval(approvalProject("tok-123"), tc.approve)
		if err := fix.Execute(context.Background(), plan, a); err != nil {
			t.Fatalf("execute: %v", err)
		}
		if a.pipeline != "my-pipeline" || a.stage != "Test" || a.action != "Approve" || a.token != "tok-123" {
			t.Errorf("wrong identity sent: %+v", a)
		}
		if a.approved != tc.approve || a.summary != tc.summary {
			t.Errorf("approved=%v summary=%q, want %v %q", a.approved, a.summary, tc.approve, tc.summary)
		}
	}
}

func TestExecute_approvalAlreadyDecidedPassesThrough(t *testing.T) {
	a := &fakeActioner{err: fix.ErrApprovalAlreadyDecided}
	err := fix.Execute(context.Background(), fix.PlanApproval(approvalProject("tok"), true), a)
	if !errors.Is(err, fix.ErrApprovalAlreadyDecided) {
		t.Errorf("err = %v, want ErrApprovalAlreadyDecided", err)
	}
}
