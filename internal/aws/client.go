package aws

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline/types"
	"github.com/ericdahl-dev/aws-green/internal/awscfg"
	"github.com/ericdahl-dev/aws-green/internal/health"
	"github.com/ericdahl-dev/aws-green/internal/state"
)

// Client fetches pipeline state from AWS CodePipeline.
type Client struct {
	svc *codepipeline.Client
}

// New creates a Client using the named AWS profile and region.
func New(profile, region string) (*Client, error) {
	cfg, err := awscfg.Load(context.Background(), profile, region)
	if err != nil {
		return nil, err
	}

	return &Client{svc: codepipeline.NewFromConfig(cfg)}, nil
}

// FetchPipeline fetches the current state of a named pipeline.
func (c *Client) FetchPipeline(ctx context.Context, name string) (state.PipelineState, error) {
	out, err := c.svc.GetPipelineState(ctx, &codepipeline.GetPipelineStateInput{
		Name: aws.String(name),
	})
	if err != nil {
		return state.PipelineState{}, fmt.Errorf("GetPipelineState(%q): %w", name, err)
	}

	return PipelineFromState(name, out), nil
}

// PipelineFromState converts a GetPipelineState response into a
// PipelineState.
func PipelineFromState(name string, out *codepipeline.GetPipelineStateOutput) state.PipelineState {
	var stages []state.StageState

	for _, stage := range out.StageStates {
		ss := state.StageState{Name: aws.ToString(stage.StageName)}
		if stage.LatestExecution != nil {
			ss.Status = mapStageStatus(stage.LatestExecution.Status)
		}
		// Pull timing and action details from the latest action executions.
		for _, action := range stage.ActionStates {
			ad := state.ActionState{Name: aws.ToString(action.ActionName)}
			if action.LatestExecution != nil {
				ad.Status = mapActionStatus(action.LatestExecution.Status)
				if ad.Status == health.StatusInProgress {
					ad.ApprovalToken = aws.ToString(action.LatestExecution.Token)
				}
				if action.LatestExecution.LastStatusChange != nil {
					t := *action.LatestExecution.LastStatusChange
					if ss.StartedAt == nil || t.Before(*ss.StartedAt) {
						ss.StartedAt = &t
					}
					if ss.Status != health.StatusInProgress {
						if ss.EndedAt == nil || t.After(*ss.EndedAt) {
							ss.EndedAt = &t
						}
					}
				}
			}
			ss.Actions = append(ss.Actions, ad)
		}
		stages = append(stages, ss)
	}

	return state.NewPipeline(name, stages)
}

func mapActionStatus(s types.ActionExecutionStatus) health.ExecutionStatus {
	switch s {
	case types.ActionExecutionStatusSucceeded:
		return health.StatusSucceeded
	case types.ActionExecutionStatusFailed:
		return health.StatusFailed
	case types.ActionExecutionStatusInProgress:
		return health.StatusInProgress
	case types.ActionExecutionStatusAbandoned:
		return health.StatusStopped
	default:
		return health.StatusSuperseded
	}
}

func mapStageStatus(s types.StageExecutionStatus) health.ExecutionStatus {
	switch s {
	case types.StageExecutionStatusSucceeded:
		return health.StatusSucceeded
	case types.StageExecutionStatusFailed:
		return health.StatusFailed
	case types.StageExecutionStatusStopped, types.StageExecutionStatusStopping:
		return health.StatusStopped
	case types.StageExecutionStatusInProgress:
		return health.StatusInProgress
	default:
		return health.StatusSuperseded
	}
}
