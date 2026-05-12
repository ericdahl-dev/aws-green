package fix

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline"
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
