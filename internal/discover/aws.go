package discover

import (
	"context"
	"errors"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline"
	"github.com/aws/smithy-go"
	"github.com/ericdahl-dev/aws-green/internal/awscfg"
)

// Run discovers pipeline's stacks and ECS services in the given profile and
// region.
func Run(ctx context.Context, profile, region, pipeline string) (Result, error) {
	cfg, err := awscfg.Load(ctx, profile, region)
	if err != nil {
		return Result{}, err
	}
	return Discover(ctx, sdkAWS{
		pipelines: codepipeline.NewFromConfig(cfg),
		stacks:    cloudformation.NewFromConfig(cfg),
	}, pipeline)
}

// sdkAWS answers discovery's questions with the AWS SDK. Every call is
// read-only.
type sdkAWS struct {
	pipelines *codepipeline.Client
	stacks    *cloudformation.Client
}

func (a sdkAWS) PipelineTags(ctx context.Context, pipeline string) (map[string]string, error) {
	out, err := a.pipelines.GetPipeline(ctx, &codepipeline.GetPipelineInput{Name: aws.String(pipeline)})
	if err != nil {
		return nil, err
	}
	tags := map[string]string{}
	pages := codepipeline.NewListTagsForResourcePaginator(a.pipelines, &codepipeline.ListTagsForResourceInput{
		ResourceArn: out.Metadata.PipelineArn,
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, t := range page.Tags {
			tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
		}
	}
	return tags, nil
}

func (a sdkAWS) PipelineStack(ctx context.Context, pipeline string) (string, error) {
	out, err := a.stacks.DescribeStackResources(ctx, &cloudformation.DescribeStackResourcesInput{
		PhysicalResourceId: aws.String(pipeline),
	})
	if err != nil {
		// A pipeline created outside CloudFormation has no stack; that is
		// an answer, not a failure.
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "ValidationError" &&
			strings.Contains(apiErr.ErrorMessage(), "does not exist") {
			return "", nil
		}
		return "", err
	}
	if len(out.StackResources) == 0 {
		return "", nil
	}
	return aws.ToString(out.StackResources[0].StackName), nil
}

func (a sdkAWS) Stacks(ctx context.Context) ([]Stack, error) {
	var out []Stack
	pages := cloudformation.NewDescribeStacksPaginator(a.stacks, &cloudformation.DescribeStacksInput{})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, s := range page.Stacks {
			tags := make(map[string]string, len(s.Tags))
			for _, t := range s.Tags {
				tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
			}
			out = append(out, Stack{Name: aws.ToString(s.StackName), Tags: tags})
		}
	}
	return out, nil
}

func (a sdkAWS) ECSServiceARNs(ctx context.Context, stack string) ([]string, error) {
	var arns []string
	pages := cloudformation.NewListStackResourcesPaginator(a.stacks, &cloudformation.ListStackResourcesInput{
		StackName: aws.String(stack),
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range page.StackResourceSummaries {
			if aws.ToString(r.ResourceType) == "AWS::ECS::Service" && r.PhysicalResourceId != nil {
				arns = append(arns, aws.ToString(r.PhysicalResourceId))
			}
		}
	}
	return arns, nil
}
