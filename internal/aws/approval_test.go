package aws_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline/types"
	awsclient "github.com/ericdahl-dev/aws-green/internal/aws"
	"github.com/ericdahl-dev/aws-green/internal/health"
)

// GetPipelineState only fills in an action's token while an approval request
// is open. Anything else (a finished approval, a build action) has none.
func TestPipelineFromState_capturesOpenApprovalToken(t *testing.T) {
	out := &codepipeline.GetPipelineStateOutput{
		StageStates: []types.StageState{
			{
				StageName:       aws.String("Source"),
				LatestExecution: &types.StageExecution{Status: types.StageExecutionStatusSucceeded},
				ActionStates: []types.ActionState{{
					ActionName:      aws.String("Checkout"),
					LatestExecution: &types.ActionExecution{Status: types.ActionExecutionStatusSucceeded},
				}},
			},
			{
				StageName:       aws.String("Test"),
				LatestExecution: &types.StageExecution{Status: types.StageExecutionStatusInProgress},
				ActionStates: []types.ActionState{{
					ActionName: aws.String("Approve"),
					LatestExecution: &types.ActionExecution{
						Status: types.ActionExecutionStatusInProgress,
						Token:  aws.String("tok-123"),
					},
				}},
			},
		},
	}

	d := awsclient.PipelineFromState("my-pipeline", out)

	if got := d.Stages[0].Actions[0].ApprovalToken; got != "" {
		t.Errorf("non-approval action token = %q, want empty", got)
	}
	a := d.Stages[1].Actions[0]
	if a.ApprovalToken != "tok-123" {
		t.Errorf("approval token = %q, want tok-123", a.ApprovalToken)
	}
	if a.Status != health.StatusInProgress {
		t.Errorf("approval status = %q, want InProgress", a.Status)
	}
}

// A token on an action that is no longer in progress is a decided approval;
// acting on it would only fail.
func TestPipelineFromState_ignoresTokenOnFinishedAction(t *testing.T) {
	out := &codepipeline.GetPipelineStateOutput{
		StageStates: []types.StageState{{
			StageName:       aws.String("Test"),
			LatestExecution: &types.StageExecution{Status: types.StageExecutionStatusSucceeded},
			ActionStates: []types.ActionState{{
				ActionName: aws.String("Approve"),
				LatestExecution: &types.ActionExecution{
					Status: types.ActionExecutionStatusSucceeded,
					Token:  aws.String("old-tok"),
				},
			}},
		}},
	}

	d := awsclient.PipelineFromState("my-pipeline", out)
	if got := d.Stages[0].Actions[0].ApprovalToken; got != "" {
		t.Errorf("finished approval token = %q, want empty", got)
	}
}
