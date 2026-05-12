package ecs

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsecs "github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/ericdahl-dev/aws-green/internal/aggregator"
)

// ServiceData holds the fetched state for a single ECS service.
type ServiceData struct {
	Name             string
	RunningCount     int32
	DesiredCount     int32
	ActiveDeployment bool
	Stoplight        aggregator.Stoplight
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
		activeDeployment := false
		for _, d := range svc.Deployments {
			if aws.ToString(d.Status) == "PRIMARY" && d.RunningCount != d.DesiredCount {
				activeDeployment = true
				break
			}
			if aws.ToString(d.Status) == "ACTIVE" {
				activeDeployment = true
				break
			}
		}
		result = append(result, ServiceData{
			Name:             name,
			RunningCount:     running,
			DesiredCount:     desired,
			ActiveDeployment: activeDeployment,
			Stoplight:        ServiceStateToStoplight(running, desired, activeDeployment),
		})
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

// ServiceStateToStoplight maps ECS service running/desired/deployment state to a Stoplight.
func ServiceStateToStoplight(running, desired int32, activeDeployment bool) aggregator.Stoplight {
	switch {
	case desired == 0 && running == 0:
		return aggregator.StoplightGrey
	case running == 0 && desired > 0:
		return aggregator.StoplightRed
	case activeDeployment:
		return aggregator.StoplightYellow
	case running != desired:
		return aggregator.StoplightYellow
	default:
		return aggregator.StoplightGreen
	}
}
