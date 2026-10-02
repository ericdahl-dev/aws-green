package cfn

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/smithy-go"
	"github.com/ericdahl-dev/aws-green/internal/awscfg"
	"github.com/ericdahl-dev/aws-green/internal/health"
	"github.com/ericdahl-dev/aws-green/internal/state"
)

// Fetcher is the interface for fetching CloudFormation stack state.
type Fetcher interface {
	FetchStacks(ctx context.Context, names []string) ([]state.StackState, error)
}

// describeStacksAPI is the one CloudFormation call Client makes, so tests can
// stand in for it.
type describeStacksAPI interface {
	DescribeStacks(ctx context.Context, in *cloudformation.DescribeStacksInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error)
}

// Client fetches CloudFormation stack state from AWS.
type Client struct {
	svc describeStacksAPI
}

// New creates a Client using the named AWS profile and region.
func New(profile, region string) (*Client, error) {
	cfg, err := awscfg.Load(context.Background(), profile, region)
	if err != nil {
		return nil, err
	}
	return &Client{svc: cloudformation.NewFromConfig(cfg)}, nil
}

// FetchStacks fetches the current state of the named CloudFormation stacks.
func (c *Client) FetchStacks(ctx context.Context, names []string) ([]state.StackState, error) {
	result := make([]state.StackState, 0, len(names))
	for _, name := range names {
		out, err := c.svc.DescribeStacks(ctx, &cloudformation.DescribeStacksInput{
			StackName: aws.String(name),
		})
		if err != nil && !isStackNotFound(err) {
			return nil, fmt.Errorf("describe stack %s: %w", name, err)
		}
		if err != nil || len(out.Stacks) == 0 {
			result = append(result, state.StackState{
				Name:      name,
				Status:    "NOT_FOUND",
				Stoplight: health.StoplightGray,
			})
			continue
		}
		s := out.Stacks[0]
		status := health.StackStatus(s.StackStatus)
		sd := state.StackState{
			Name:      name,
			Status:    status,
			Stoplight: status.Stoplight(),
		}
		if s.LastUpdatedTime != nil {
			sd.StartedAt = s.LastUpdatedTime
		} else if s.CreationTime != nil {
			sd.StartedAt = s.CreationTime
		}
		result = append(result, sd)
	}
	return result, nil
}

// isStackNotFound reports whether err is CloudFormation saying the stack
// doesn't exist. AWS has no dedicated error code for it: it is a
// ValidationError whose message names the missing stack.
func isStackNotFound(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) &&
		apiErr.ErrorCode() == "ValidationError" &&
		strings.Contains(apiErr.ErrorMessage(), "does not exist")
}
