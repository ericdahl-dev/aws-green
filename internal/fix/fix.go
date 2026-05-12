package fix

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ericdahl-dev/aws-green/internal/aggregator"
	"github.com/ericdahl-dev/aws-green/internal/state"
)

// Kind identifies what type of fix action will be taken.
type Kind int

const (
	KindRestartPipeline  Kind = iota // StartPipelineExecution
	KindContinueRollback             // ContinueUpdateRollback on a stack
	KindCancelStackUpdate            // CancelUpdateStack on a stuck stack
	KindForceDeployECS               // UpdateService forceNewDeployment=true
)

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
	default:
		return "unknown"
	}
}

// FixPlan describes what action will be taken and carries the parameters needed to execute it.
type FixPlan struct {
	Kind        Kind
	Description string // plain-English confirmation text shown to user

	// Pipeline fix
	PipelineName string

	// Stack fix
	StackName string

	// ECS fix
	ECSCluster  string
	ECSService  string
}

// Plan inspects a ProjectState and returns the highest-priority FixPlan, or nil if nothing to fix.
// Precedence: pipeline → stacks → ECS.
func Plan(proj state.ProjectState) *FixPlan {
	// 1. Pipeline
	if plan := planPipeline(proj.Pipeline); plan != nil {
		return plan
	}

	// 2. Stacks — worst first (red before yellow)
	for _, s := range proj.Stacks {
		if plan := planStack(s); plan != nil {
			return plan
		}
	}

	// 3. ECS
	for _, s := range proj.ECSServices {
		if plan := planECS(s); plan != nil {
			return plan
		}
	}

	return nil
}

func planPipeline(p state.PipelineState) *FixPlan {
	if p.Stoplight != aggregator.StoplightRed {
		return nil
	}
	return &FixPlan{
		Kind:         KindRestartPipeline,
		PipelineName: p.Name,
		Description:  fmt.Sprintf("restart pipeline  %s", p.Name),
	}
}

const stalledStackThreshold = 30 * time.Minute

func planStack(s state.StackState) *FixPlan {
	// Stuck rollback — ContinueUpdateRollback
	if strings.HasSuffix(s.Status, "_ROLLBACK_FAILED") || s.Status == "UPDATE_ROLLBACK_FAILED" {
		return &FixPlan{
			Kind:        KindContinueRollback,
			StackName:   s.Name,
			Description: fmt.Sprintf("continue rollback: %s  (%s)", s.Name, s.Status),
		}
	}

	// In-progress > 30 min — CancelUpdateStack
	if s.StartedAt != nil && isInProgress(s.Status) && time.Since(*s.StartedAt) > stalledStackThreshold {
		elapsed := time.Since(*s.StartedAt).Round(time.Second)
		return &FixPlan{
			Kind:        KindCancelStackUpdate,
			StackName:   s.Name,
			Description: fmt.Sprintf("cancel stalled update: %s  (%s, running %s)", s.Name, s.Status, elapsed),
		}
	}

	return nil
}

func isInProgress(status string) bool {
	return strings.HasSuffix(status, "_IN_PROGRESS")
}

func planECS(s state.ECSServiceState) *FixPlan {
	if s.Stoplight != aggregator.StoplightRed && s.Stoplight != aggregator.StoplightYellow {
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
}

// Execute runs the plan using the provided Actioner.
func Execute(ctx context.Context, plan *FixPlan, a Actioner) error {
	switch plan.Kind {
	case KindRestartPipeline:
		return a.RestartPipeline(ctx, plan.PipelineName)
	case KindContinueRollback:
		return a.ContinueRollback(ctx, plan.StackName)
	case KindCancelStackUpdate:
		return a.CancelStackUpdate(ctx, plan.StackName)
	case KindForceDeployECS:
		return a.ForceDeployECS(ctx, plan.ECSCluster, plan.ECSService)
	default:
		return fmt.Errorf("unknown fix kind: %v", plan.Kind)
	}
}
