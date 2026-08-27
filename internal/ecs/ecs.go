package ecs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsecs "github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/ericdahl-dev/aws-green/internal/aggregator"
	"github.com/ericdahl-dev/aws-green/internal/awscfg"
)

// ServiceData holds the fetched state for a single ECS service.
type ServiceData struct {
	Name             string
	RunningCount     int32
	DesiredCount     int32
	PendingCount     int32
	ActiveDeployment bool
	Stoplight        aggregator.Stoplight

	// Task-level detail
	FailingTaskCount int    // tasks stopped with a non-zero exit / error
	StoppedReason    string // reason from the most recently stopped failing task
}

// Fetcher is the interface for fetching ECS service state.
type Fetcher interface {
	FetchServices(ctx context.Context, cluster string, services []string) ([]ServiceData, error)
}

// Client fetches ECS service state from AWS.
type Client struct {
	svc *awsecs.Client
}

// New creates a Client using the named AWS profile and region.
func New(profile, region string) (*Client, error) {
	cfg, err := awscfg.Load(context.Background(), profile, region)
	if err != nil {
		return nil, err
	}
	return &Client{svc: awsecs.NewFromConfig(cfg)}, nil
}

// FetchServices fetches the current state of the named ECS services in the given cluster.
func (c *Client) FetchServices(ctx context.Context, cluster string, services []string) ([]ServiceData, error) {
	if len(services) == 0 {
		return nil, nil
	}

	serviceARNs := make([]string, len(services))
	copy(serviceARNs, services)

	out, err := c.svc.DescribeServices(ctx, &awsecs.DescribeServicesInput{
		Cluster:  aws.String(cluster),
		Services: serviceARNs,
	})
	if err != nil {
		return nil, fmt.Errorf("DescribeServices(cluster=%q): %w", cluster, err)
	}

	result := make([]ServiceData, 0, len(out.Services))
	for _, svc := range out.Services {
		name := aws.ToString(svc.ServiceName)
		running := svc.RunningCount
		desired := svc.DesiredCount
		pending := svc.PendingCount
		activeDeployment := false
		// ECS already counts consecutively failed tasks per deployment, so
		// scan every deployment rather than stopping at the first that looks
		// active — that count is what spots a crash-loop below.
		var deployFailures int32
		for _, d := range svc.Deployments {
			deployFailures += d.FailedTasks
			switch {
			case aws.ToString(d.Status) == "PRIMARY" && d.RunningCount != d.DesiredCount:
				activeDeployment = true
			case aws.ToString(d.Status) == "ACTIVE":
				activeDeployment = true
			}
		}

		sd := ServiceData{
			Name:             name,
			RunningCount:     running,
			DesiredCount:     desired,
			PendingCount:     pending,
			ActiveDeployment: activeDeployment,
		}

		// Stopped-task detail costs two API calls per service per poll, and a
		// service with nothing wrong has no stopped tasks to explain. Ask only
		// when the summary already says something is off.
		if NeedsTaskDetail(sd, deployFailures) {
			sd.FailingTaskCount, sd.StoppedReason = c.fetchFailingTasks(ctx, cluster, name)
		}
		sd.Stoplight = ServiceStateToStoplight(sd)
		result = append(result, sd)
	}

	// For any requested service not returned, add a grey entry.
	found := make(map[string]bool, len(result))
	for _, sd := range result {
		found[sd.Name] = true
	}
	for _, svcName := range services {
		if !found[svcName] {
			result = append(result, ServiceData{
				Name:      svcName,
				Stoplight: aggregator.StoplightGrey,
			})
		}
	}

	return result, nil
}

// fetchFailingTasks lists recently stopped tasks for the service and returns
// the count that stopped due to an error, plus the StoppedReason of the most
// recent one. Errors are silently ignored — task detail is best-effort.
func (c *Client) fetchFailingTasks(ctx context.Context, cluster, service string) (int, string) {
	listOut, err := c.svc.ListTasks(ctx, &awsecs.ListTasksInput{
		Cluster:       aws.String(cluster),
		ServiceName:   aws.String(service),
		DesiredStatus: ecstypes.DesiredStatusStopped,
	})
	if err != nil || len(listOut.TaskArns) == 0 {
		return 0, ""
	}

	arns := listOut.TaskArns
	if len(arns) > 100 {
		arns = arns[:100]
	}

	descOut, err := c.svc.DescribeTasks(ctx, &awsecs.DescribeTasksInput{
		Cluster: aws.String(cluster),
		Tasks:   arns,
	})
	if err != nil {
		return 0, ""
	}

	return CountFailingTasks(descOut.Tasks, time.Now())
}

// failingTaskWindow bounds how far back a stopped task counts against its
// service. ECS keeps stopped tasks listable for roughly an hour, so without a
// window a single task that died and was immediately replaced keeps the
// service red long after it returned to steady state.
const failingTaskWindow = 15 * time.Minute

// NeedsTaskDetail reports whether a service's summary state is worth spending
// two extra API calls on stopped-task detail.
//
// deployFailures is ECS's own count of consecutively failed tasks across the
// service's deployments, and it is the reason this is not simply a
// running-versus-desired check: a service that crash-loops is restarted fast
// enough to keep reporting its full task count, so on those three fields alone
// it looks healthy. ECS reports the failures in the same DescribeServices
// response, at no extra cost.
func NeedsTaskDetail(sd ServiceData, deployFailures int32) bool {
	return sd.RunningCount != sd.DesiredCount ||
		sd.PendingCount > 0 ||
		sd.ActiveDeployment ||
		deployFailures > 0
}

// CountFailingTasks returns how many of the given stopped tasks failed recently
// enough to still reflect the service's health, along with the reason from the
// most recent one.
func CountFailingTasks(tasks []ecstypes.Task, now time.Time) (int, string) {
	failCount := 0
	var latestReason string
	var latestAt time.Time
	for _, t := range tasks {
		reason := aws.ToString(t.StoppedReason)
		if !IsFailingStopReason(reason) {
			continue
		}
		stoppedAt := aws.ToTime(t.StoppedAt)
		if stoppedAt.IsZero() || now.Sub(stoppedAt) > failingTaskWindow {
			continue
		}
		failCount++
		if stoppedAt.After(latestAt) {
			latestAt = stoppedAt
			latestReason = reason
		}
	}
	return failCount, latestReason
}

// IsFailingStopReason returns true for stop reasons that indicate a problem,
// as opposed to intentional stops like scaling or deployments.
func IsFailingStopReason(reason string) bool {
	if reason == "" {
		return false
	}
	// ECS appends context to some intentional stops, e.g.
	// "Scaling activity initiated by (deployment ecs-svc/123)", so these
	// match on prefix.
	for _, benign := range []string{
		"Scaling activity initiated",
		"Service scheduler initiated action",
	} {
		if strings.HasPrefix(reason, benign) {
			return false
		}
	}
	// These are only benign verbatim: "Essential container in task exited
	// (exit code 1)" is a real failure.
	switch reason {
	case "Task stopped by user",
		"Essential container in task exited":
		return false
	}
	return true
}

// ServiceStateToStoplight maps ECS service state to a Stoplight.
func ServiceStateToStoplight(sd ServiceData) aggregator.Stoplight {
	switch {
	case sd.DesiredCount == 0 && sd.RunningCount == 0:
		return aggregator.StoplightGrey
	case sd.RunningCount == 0 && sd.DesiredCount > 0:
		return aggregator.StoplightRed
	case sd.FailingTaskCount > 0:
		return aggregator.StoplightRed
	case sd.PendingCount > 0:
		return aggregator.StoplightYellow
	case sd.ActiveDeployment:
		return aggregator.StoplightYellow
	case sd.RunningCount != sd.DesiredCount:
		return aggregator.StoplightYellow
	default:
		return aggregator.StoplightGreen
	}
}
