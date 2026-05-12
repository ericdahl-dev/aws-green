package cfn

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/ericdahl-dev/aws-green/internal/aggregator"
)

// StackData holds the fetched state for a single CloudFormation stack.
type StackData struct {
	Name       string
	Status     string
	Stoplight  aggregator.Stoplight
	StartedAt  *time.Time
}

// Fetcher is the interface for fetching CloudFormation stack state.
type Fetcher interface {
	FetchStacks(ctx context.Context, names []string) ([]StackData, error)
}

// Client fetches CloudFormation stack state from AWS.
type Client struct {
	svc *cloudformation.Client
}

// New creates a Client using the named AWS profile and region.
func New(profile, region string) (*Client, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
	}
	if profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config (profile=%q region=%q): %w", profile, region, err)
	}
	return &Client{svc: cloudformation.NewFromConfig(cfg)}, nil
}

// FetchStacks fetches the current state of the named CloudFormation stacks.
func (c *Client) FetchStacks(ctx context.Context, names []string) ([]StackData, error) {
	result := make([]StackData, 0, len(names))
	for _, name := range names {
		out, err := c.svc.DescribeStacks(ctx, &cloudformation.DescribeStacksInput{
			StackName: aws.String(name),
		})
		if err != nil {
			result = append(result, StackData{
				Name:      name,
				Status:    "UNKNOWN",
				Stoplight: aggregator.StoplightGrey,
			})
			continue
		}
		if len(out.Stacks) == 0 {
			result = append(result, StackData{
				Name:      name,
				Status:    "NOT_FOUND",
				Stoplight: aggregator.StoplightGrey,
			})
			continue
		}
		s := out.Stacks[0]
		status := string(s.StackStatus)
		sd := StackData{
			Name:      name,
			Status:    status,
			Stoplight: StackStatusToStoplight(status),
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

// StackStatusToStoplight maps a CloudFormation stack status string to a Stoplight.
func StackStatusToStoplight(status string) aggregator.Stoplight {
	switch {
	case strings.HasSuffix(status, "_COMPLETE") &&
		!strings.HasPrefix(status, "DELETE") &&
		!strings.HasPrefix(status, "ROLLBACK_COMPLETE"):
		return aggregator.StoplightGreen
	case status == "ROLLBACK_COMPLETE":
		return aggregator.StoplightGreen
	case strings.HasSuffix(status, "_FAILED"),
		strings.HasPrefix(status, "ROLLBACK_") && !strings.HasSuffix(status, "_COMPLETE"),
		strings.HasPrefix(status, "DELETE_"):
		return aggregator.StoplightRed
	case status == "REVIEW_IN_PROGRESS":
		return aggregator.StoplightGrey
	case strings.HasSuffix(status, "_IN_PROGRESS"):
		return aggregator.StoplightYellow
	default:
		return aggregator.StoplightGrey
	}
}
