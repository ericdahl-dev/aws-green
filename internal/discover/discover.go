// Package discover finds the CloudFormation stacks and ECS services that
// belong with a Pipeline, so adding a project doesn't mean hand-editing them
// into the Config file.
//
// The pipeline definition can't answer this: CDK pipelines deploy through
// CodeBuild (`cdk deploy`), so no action names a stack. What links them is
// the ProjectName tag the CDK blueprints put on the pipeline and on every
// stack. That is a heuristic — a shared tag also matches shared stacks — so
// callers show the result for the user to confirm, never save it blindly.
package discover

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ericdahl-dev/aws-green/internal/config"
)

// ProjectTag is the tag that links a pipeline to its stacks.
const ProjectTag = "ProjectName"

// Stack is an active CloudFormation stack and its tags.
type Stack struct {
	Name string
	Tags map[string]string
}

// AWS is what discovery needs to ask AWS.
type AWS interface {
	// PipelineTags returns the pipeline's tags.
	PipelineTags(ctx context.Context, pipeline string) (map[string]string, error)
	// PipelineStack returns the stack that defines the pipeline, or "" if
	// it wasn't created by CloudFormation.
	PipelineStack(ctx context.Context, pipeline string) (string, error)
	// Stacks returns every active stack in the account and region.
	Stacks(ctx context.Context) ([]Stack, error)
	// ECSServiceARNs returns the ARNs of the ECS services a stack created.
	ECSServiceARNs(ctx context.Context, stack string) ([]string, error)
}

// Result is what discovery found for one pipeline.
type Result struct {
	Stacks []string
	ECS    []config.ECSConfig
}

// Discover finds the stacks and ECS services that belong with pipeline.
func Discover(ctx context.Context, aws AWS, pipeline string) (Result, error) {
	tags, err := aws.PipelineTags(ctx, pipeline)
	if err != nil {
		return Result{}, fmt.Errorf("pipeline tags: %w", err)
	}
	project := tags[ProjectTag]
	if project == "" {
		return Result{}, nil
	}
	own, err := aws.PipelineStack(ctx, pipeline)
	if err != nil {
		return Result{}, fmt.Errorf("pipeline stack: %w", err)
	}
	stacks, err := aws.Stacks(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("list stacks: %w", err)
	}

	var r Result
	for _, s := range stacks {
		if s.Name != own && s.Tags[ProjectTag] == project {
			r.Stacks = append(r.Stacks, s.Name)
		}
	}
	sort.Strings(r.Stacks)

	byCluster := map[string]int{}
	for _, stack := range r.Stacks {
		arns, err := aws.ECSServiceARNs(ctx, stack)
		if err != nil {
			return Result{}, fmt.Errorf("resources of %s: %w", stack, err)
		}
		for _, arn := range arns {
			cluster, service, ok := parseServiceARN(arn)
			if !ok {
				continue
			}
			i, seen := byCluster[cluster]
			if !seen {
				i = len(r.ECS)
				byCluster[cluster] = i
				r.ECS = append(r.ECS, config.ECSConfig{Cluster: cluster})
			}
			r.ECS[i].Services = append(r.ECS[i].Services, service)
		}
	}
	return r, nil
}

// parseServiceARN splits an ECS service ARN of the long form
// arn:aws:ecs:<region>:<account>:service/<cluster>/<service>.
func parseServiceARN(arn string) (cluster, service string, ok bool) {
	_, resource, found := strings.Cut(arn, ":service/")
	if !found {
		return "", "", false
	}
	cluster, service, found = strings.Cut(resource, "/")
	return cluster, service, found && cluster != "" && service != ""
}

// Likely narrows the result to the stacks and services named after the
// project. A shared tag can also match sibling projects — ProjectName=dec
// covers honeycomb, beehive and honeypot alike — and the project's own
// resources are the ones carrying its name. When nothing carries it, the tag
// match is all there is to go on, so everything is kept.
func (r Result) Likely(project string) Result {
	named := func(s string) bool {
		return project != "" && strings.Contains(strings.ToLower(s), strings.ToLower(project))
	}
	var out Result
	for _, s := range r.Stacks {
		if named(s) {
			out.Stacks = append(out.Stacks, s)
		}
	}
	for _, e := range r.ECS {
		var services []string
		for _, sv := range e.Services {
			if named(sv) {
				services = append(services, sv)
			}
		}
		if len(services) > 0 {
			out.ECS = append(out.ECS, config.ECSConfig{Cluster: e.Cluster, Services: services})
		}
	}
	if len(out.Stacks) == 0 && len(out.ECS) == 0 {
		return r
	}
	return out
}
