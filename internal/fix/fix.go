package fix

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ericdahl-dev/aws-green/internal/health"
	"github.com/ericdahl-dev/aws-green/internal/state"
)

// Kind identifies what type of fix action will be taken.
type Kind int

const (
	KindRestartPipeline   Kind = iota // StartPipelineExecution
	KindContinueRollback              // ContinueUpdateRollback on a stack
	KindCancelStackUpdate             // CancelUpdateStack on a stuck stack
	KindForceDeployECS                // UpdateService forceNewDeployment=true
	KindApprove                       // PutApprovalResult Approved
	KindReject                        // PutApprovalResult Rejected
)

// ErrApprovalAlreadyDecided means the approval token went stale between the
// last poll and the decision: someone approved or rejected in the console, or
// the request timed out. It is not a failure of ours.
var ErrApprovalAlreadyDecided = errors.New("approval already decided")

// ErrApprovalNotPermitted means the profile can see the approval but not
// decide it — typically a read-only role.
var ErrApprovalNotPermitted = errors.New("profile not permitted to decide approvals")

func (k Kind) String() string {
	switch k {
	case KindRestartPipeline:
		return "restart pipeline"
	case KindContinueRollback:
		return "continue rollback"
	case KindCancelStackUpdate:
		return "cancel stack update"
	case KindForceDeployECS:
		return "force ECS deployment"
	case KindApprove:
		return "approve"
	case KindReject:
		return "reject"
	default:
		return "unknown"
	}
}

// FixPlan describes what action will be taken and carries the parameters needed to execute it.
type FixPlan struct {
	Kind        Kind
	Description string // plain-English confirmation text shown to user

	// AWS credentials for the target project
	Profile string
	Region  string

	// Pipeline fix
	PipelineName string

	// Approval — stage, action, and the token from the last poll
	StageName     string
	ActionName    string
	ApprovalToken string

	// Stack fix
	StackName string

	// ECS fix
	ECSCluster string
	ECSService string
}

// Plan inspects a ProjectState and returns the highest-priority FixPlan, or nil if nothing to fix.
// Precedence: rollback-failed stacks → pipeline → other stacks → ECS.
// Rollback-failed stacks rank above pipeline because restarting the pipeline would fail
// immediately until the stack is recovered.
func Plan(proj state.ProjectState) *FixPlan {
	var plan *FixPlan

	// 1. Rollback-failed stacks — must be resolved before any pipeline restart can succeed.
	for _, s := range proj.Stacks {
		if s.Status.RollbackFailed() {
			if p := planStack(s); p != nil {
				plan = p
				break
			}
		}
	}

	// 2. Pipeline
	if plan == nil {
		plan = planPipeline(proj.Pipeline)
	}

	// 3. Other stacks (stalled in-progress)
	if plan == nil {
		for _, s := range proj.Stacks {
			if p := planStack(s); p != nil {
				plan = p
				break
			}
		}
	}

	// 4. ECS
	if plan == nil {
		for _, s := range proj.ECSServices {
			if p := planECS(s); p != nil {
				plan = p
				break
			}
		}
	}

	if plan != nil {
		plan.Profile = proj.Profile
		plan.Region = proj.Region
	}
	return plan
}

func planPipeline(p state.PipelineState) *FixPlan {
	if p.Stoplight != health.StoplightRed {
		return nil
	}
	return &FixPlan{
		Kind:         KindRestartPipeline,
		PipelineName: p.Name,
		Description:  fmt.Sprintf("restart pipeline  %s", p.Name),
	}
}

// PlanApproval returns a plan to approve (or reject) the pipeline's pending
// manual approval, or nil if none is waiting. It is separate from Plan because
// deciding an approval is the user's call, never a "smart fix".
func PlanApproval(proj state.ProjectState, approve bool) *FixPlan {
	pa := proj.Pipeline.PendingApproval()
	if pa == nil {
		return nil
	}
	kind, verb := KindApprove, "approve"
	if !approve {
		kind, verb = KindReject, "reject"
	}
	return &FixPlan{
		Kind:          kind,
		Description:   fmt.Sprintf("%s  %s / %s / %s", verb, proj.Pipeline.Name, pa.StageName, pa.ActionName),
		Profile:       proj.Profile,
		Region:        proj.Region,
		PipelineName:  proj.Pipeline.Name,
		StageName:     pa.StageName,
		ActionName:    pa.ActionName,
		ApprovalToken: pa.Token,
	}
}

const stalledStackThreshold = 30 * time.Minute

func planStack(s state.StackState) *FixPlan {
	// Stuck rollback — ContinueUpdateRollback
	if s.Status.RollbackFailed() {
		return &FixPlan{
			Kind:        KindContinueRollback,
			StackName:   s.Name,
			Description: fmt.Sprintf("continue rollback: %s  (%s)", s.Name, s.Status),
		}
	}

	// In-progress > 30 min — CancelUpdateStack
	if s.StartedAt != nil && s.Status.Cancellable() && time.Since(*s.StartedAt) > stalledStackThreshold {
		elapsed := time.Since(*s.StartedAt).Round(time.Second)
		return &FixPlan{
			Kind:        KindCancelStackUpdate,
			StackName:   s.Name,
			Description: fmt.Sprintf("cancel stalled update: %s  (%s, running %s)", s.Name, s.Status, elapsed),
		}
	}

	return nil
}

func planECS(s state.ECSServiceState) *FixPlan {
	if s.Stoplight != health.StoplightRed && s.Stoplight != health.StoplightYellow {
		return nil
	}
	desc := fmt.Sprintf("force new deployment: %s  (%d/%d running)", s.Name, s.RunningCount, s.DesiredCount)
	return &FixPlan{
		Kind:        KindForceDeployECS,
		ECSCluster:  s.Cluster,
		ECSService:  s.Name,
		Description: desc,
	}
}

// Actioner executes a FixPlan against AWS.
type Actioner interface {
	RestartPipeline(ctx context.Context, name string) error
	ContinueRollback(ctx context.Context, stackName string) error
	CancelStackUpdate(ctx context.Context, stackName string) error
	ForceDeployECS(ctx context.Context, cluster, service string) error
	PutApprovalResult(ctx context.Context, pipeline, stage, action, token string, approved bool, summary string) error
}

// ActionerFactory builds an Actioner for the given AWS profile and region.
type ActionerFactory func(profile, region string) (Actioner, error)

// Outcome is what applying a plan came to, ready to show the user.
type Outcome struct {
	Message string
	// Failed marks an outcome to show as an error.
	Failed bool
	// Refresh asks for an immediate re-poll so the row catches up with what
	// changed in AWS.
	Refresh bool
}

// Apply runs the plan against the account it targets and says what happened.
// A stale approval token is not a failure — someone decided in the console
// since the last poll — so it re-polls instead of reporting an error.
func Apply(ctx context.Context, plan *FixPlan, newActioner ActionerFactory) Outcome {
	a, err := newActioner(plan.Profile, plan.Region)
	if err != nil {
		return Outcome{Message: fmt.Sprintf("fix failed: build actioner: %v", err), Failed: true}
	}
	err = execute(ctx, plan, a)
	switch {
	case err == nil:
		return Outcome{Message: fmt.Sprintf("✓ %s", plan.Kind), Refresh: true}
	case errors.Is(err, ErrApprovalAlreadyDecided):
		return Outcome{Message: "approval already decided elsewhere — refreshing", Refresh: true}
	case errors.Is(err, ErrApprovalNotPermitted):
		who := "these credentials"
		if plan.Profile != "" {
			who = "profile " + plan.Profile
		}
		return Outcome{Message: fmt.Sprintf("%s can't %s — it needs codepipeline:PutApprovalResult", who, plan.Kind), Failed: true}
	default:
		return Outcome{Message: fmt.Sprintf("fix failed: %v", err), Failed: true}
	}
}

// execute runs the plan using the provided Actioner.
func execute(ctx context.Context, plan *FixPlan, a Actioner) error {
	switch plan.Kind {
	case KindRestartPipeline:
		return a.RestartPipeline(ctx, plan.PipelineName)
	case KindContinueRollback:
		return a.ContinueRollback(ctx, plan.StackName)
	case KindCancelStackUpdate:
		return a.CancelStackUpdate(ctx, plan.StackName)
	case KindForceDeployECS:
		return a.ForceDeployECS(ctx, plan.ECSCluster, plan.ECSService)
	case KindApprove:
		return a.PutApprovalResult(ctx, plan.PipelineName, plan.StageName, plan.ActionName, plan.ApprovalToken, true, "Approved via aws-green")
	case KindReject:
		return a.PutApprovalResult(ctx, plan.PipelineName, plan.StageName, plan.ActionName, plan.ApprovalToken, false, "Rejected via aws-green")
	default:
		return fmt.Errorf("unknown fix kind: %v", plan.Kind)
	}
}
