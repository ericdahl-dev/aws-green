package fix

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline"
	cptypes "github.com/aws/aws-sdk-go-v2/service/codepipeline/types"
	awsecs "github.com/aws/aws-sdk-go-v2/service/ecs"
)

// AWSActioner implements Actioner using the real AWS SDK.
type AWSActioner struct {
	pipeline *codepipeline.Client
	cfn      *cloudformation.Client
	ecs      *awsecs.Client
}

// NewAWSActioner creates an AWSActioner using the given AWS profile and region.
func NewAWSActioner(profile, region string) (*AWSActioner, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
	}
	if profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return &AWSActioner{
		pipeline: codepipeline.NewFromConfig(cfg),
		cfn:      cloudformation.NewFromConfig(cfg),
		ecs:      awsecs.NewFromConfig(cfg),
	}, nil
}

func (a *AWSActioner) RestartPipeline(ctx context.Context, name string) error {
	_, err := a.pipeline.StartPipelineExecution(ctx, &codepipeline.StartPipelineExecutionInput{
		Name: aws.String(name),
	})
	return err
}

func (a *AWSActioner) ContinueRollback(ctx context.Context, stackName string) error {
	_, err := a.cfn.ContinueUpdateRollback(ctx, &cloudformation.ContinueUpdateRollbackInput{
		StackName: aws.String(stackName),
	})
	return err
}

func (a *AWSActioner) CancelStackUpdate(ctx context.Context, stackName string) error {
	_, err := a.cfn.CancelUpdateStack(ctx, &cloudformation.CancelUpdateStackInput{
		StackName: aws.String(stackName),
	})
	return err
}

func (a *AWSActioner) ForceDeployECS(ctx context.Context, cluster, service string) error {
	_, err := a.ecs.UpdateService(ctx, &awsecs.UpdateServiceInput{
		Cluster:            aws.String(cluster),
		Service:            aws.String(service),
		ForceNewDeployment: true,
	})
	return err
}

func (a *AWSActioner) PutApprovalResult(ctx context.Context, pipeline, stage, action, token string, approved bool, summary string) error {
	status := cptypes.ApprovalStatusApproved
	if !approved {
		status = cptypes.ApprovalStatusRejected
	}
	_, err := a.pipeline.PutApprovalResult(ctx, &codepipeline.PutApprovalResultInput{
		PipelineName: aws.String(pipeline),
		StageName:    aws.String(stage),
		ActionName:   aws.String(action),
		Token:        aws.String(token),
		Result: &cptypes.ApprovalResult{
			Status:  status,
			Summary: aws.String(summary),
		},
	})
	return approvalError(err)
}

// approvalError folds AWS's two "someone already decided" errors into
// ErrApprovalAlreadyDecided so the UI can tell a stale token from a failure,
// and access denied into ErrApprovalNotPermitted so it can name the profile.
func approvalError(err error) error {
	if err == nil {
		return nil
	}
	var invalid *cptypes.InvalidApprovalTokenException
	var completed *cptypes.ApprovalAlreadyCompletedException
	if errors.As(err, &invalid) || errors.As(err, &completed) {
		return fmt.Errorf("%w: %v", ErrApprovalAlreadyDecided, err)
	}
	var apiErr interface{ ErrorCode() string }
	if errors.As(err, &apiErr) && strings.HasPrefix(apiErr.ErrorCode(), "AccessDenied") {
		return fmt.Errorf("%w: %v", ErrApprovalNotPermitted, err)
	}
	return err
}
