package aws

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline/types"
	"github.com/ericdahl-dev/aws-green/internal/aggregator"
)

// StageState holds the current status of a single Pipeline stage.
type StageState struct {
	Name      string
	Status    aggregator.ExecutionStatus
	StartedAt *time.Time // non-nil when stage has started
	EndedAt   *time.Time // non-nil when stage has finished
}

// PipelineData is the result of a single fetch for one Pipeline.
type PipelineData struct {
	Name       string
	Stages     []StageState
	ConsoleURL string
}

// PipelineQuery identifies a pipeline to fetch.
type PipelineQuery struct {
	Name    string
	Region  string
	Profile string
}

// Client fetches pipeline state from AWS CodePipeline.
type Client struct {
	region  string
	profile string
	svc     *codepipeline.Client
}

// New creates a Client using the named AWS profile and region.
func New(profile, region string) (*Client, error) {
	opts := []func(*config.LoadOptions) error{
		config.WithRegion(region),
	}
	if profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}

	cfg, err := config.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config (profile=%q region=%q): %w", profile, region, err)
	}

	return &Client{
		region:  region,
		profile: profile,
		svc:     codepipeline.NewFromConfig(cfg),
	}, nil
}

// FetchPipeline fetches the current state of a named pipeline.
func (c *Client) FetchPipeline(ctx context.Context, name string) (PipelineData, error) {
	out, err := c.svc.GetPipelineState(ctx, &codepipeline.GetPipelineStateInput{
		Name: aws.String(name),
	})
	if err != nil {
		return PipelineData{}, fmt.Errorf("GetPipelineState(%q): %w", name, err)
	}

	data := PipelineData{
		Name:       name,
		ConsoleURL: consoleURL(c.region, name),
	}

	for _, stage := range out.StageStates {
		ss := StageState{Name: aws.ToString(stage.StageName)}
		if stage.LatestExecution != nil {
			ss.Status = mapStageStatus(stage.LatestExecution.Status)
		}
		// Pull timing from the latest action that has started.
		for _, action := range stage.ActionStates {
			if action.LatestExecution == nil {
				continue
			}
			if action.LatestExecution.LastStatusChange != nil {
				t := *action.LatestExecution.LastStatusChange
				if ss.StartedAt == nil || t.Before(*ss.StartedAt) {
					ss.StartedAt = &t
				}
				if ss.Status != aggregator.StatusInProgress {
					if ss.EndedAt == nil || t.After(*ss.EndedAt) {
						ss.EndedAt = &t
					}
				}
			}
		}
		data.Stages = append(data.Stages, ss)
	}

	return data, nil
}

func mapStageStatus(s types.StageExecutionStatus) aggregator.ExecutionStatus {
	switch s {
	case types.StageExecutionStatusSucceeded:
		return aggregator.StatusSucceeded
	case types.StageExecutionStatusFailed:
		return aggregator.StatusFailed
	case types.StageExecutionStatusStopped, types.StageExecutionStatusStopping:
		return aggregator.StatusStopped
	case types.StageExecutionStatusInProgress:
		return aggregator.StatusInProgress
	default:
		return aggregator.StatusSuperseded
	}
}

func consoleURL(region, pipeline string) string {
	return fmt.Sprintf("https://%s.console.aws.amazon.com/codesuite/codepipeline/pipelines/%s/view", region, pipeline)
}
