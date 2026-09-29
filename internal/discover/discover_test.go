package discover_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ericdahl-dev/aws-green/internal/config"
	"github.com/ericdahl-dev/aws-green/internal/discover"
)

// fakeAWS answers discovery's questions from canned data.
type fakeAWS struct {
	pipelineTags  map[string]string
	pipelineStack string
	stacks        []discover.Stack
	services      map[string][]string // stack name → ECS service ARNs
	err           error
}

func (f fakeAWS) PipelineTags(context.Context, string) (map[string]string, error) {
	return f.pipelineTags, f.err
}

func (f fakeAWS) PipelineStack(context.Context, string) (string, error) {
	return f.pipelineStack, nil
}

func (f fakeAWS) Stacks(context.Context) ([]discover.Stack, error) {
	return f.stacks, nil
}

func (f fakeAWS) ECSServiceARNs(_ context.Context, stack string) ([]string, error) {
	return f.services[stack], nil
}

func reservesAWS() fakeAWS {
	reserves := map[string]string{"ProjectName": "Reserves"}
	return fakeAWS{
		pipelineTags:  reserves,
		pipelineStack: "reserves-deployment",
		stacks: []discover.Stack{
			{Name: "reserves-deployment", Tags: reserves},
			{Name: "reserves-service", Tags: reserves},
			{Name: "api-service", Tags: map[string]string{"ProjectName": "API"}},
			{Name: "reserves-cluster", Tags: reserves},
			{Name: "untagged"},
			{Name: "reserves-test-service", Tags: reserves},
		},
		services: map[string][]string{
			"reserves-service":      {"arn:aws:ecs:us-east-1:123:service/reserves-cluster-A/reserves-app"},
			"reserves-test-service": {"arn:aws:ecs:us-east-1:123:service/reserves-test-cluster-B/reserves-test-app"},
		},
	}
}

// A pipeline's stacks are the ones sharing its ProjectName tag, minus the
// stack that defines the pipeline itself.
func TestDiscoverFindsStacksByProjectTag(t *testing.T) {
	got, err := discover.Discover(context.Background(), reservesAWS(), "reserves-pipeline")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"reserves-cluster", "reserves-service", "reserves-test-service"}
	if !reflect.DeepEqual(got.Stacks, want) {
		t.Errorf("Stacks = %v, want %v", got.Stacks, want)
	}
}

// Without a ProjectName tag there is nothing to link on — an untagged
// pipeline must not claim every untagged stack in the account.
func TestDiscoverUntaggedPipelineFindsNothing(t *testing.T) {
	aws := reservesAWS()
	aws.pipelineTags = nil
	got, err := discover.Discover(context.Background(), aws, "reserves-pipeline")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Stacks) != 0 {
		t.Errorf("Stacks = %v, want none", got.Stacks)
	}
}

// ECS services come from the discovered stacks' resources, grouped by the
// cluster named in each service ARN.
func TestDiscoverFindsECSServicesByCluster(t *testing.T) {
	aws := reservesAWS()
	aws.services["reserves-service"] = append(aws.services["reserves-service"],
		"arn:aws:ecs:us-east-1:123:service/reserves-cluster-A/reserves-worker")
	got, err := discover.Discover(context.Background(), aws, "reserves-pipeline")
	if err != nil {
		t.Fatal(err)
	}
	want := []config.ECSConfig{
		{Cluster: "reserves-cluster-A", Services: []string{"reserves-app", "reserves-worker"}},
		{Cluster: "reserves-test-cluster-B", Services: []string{"reserves-test-app"}},
	}
	if !reflect.DeepEqual(got.ECS, want) {
		t.Errorf("ECS = %+v, want %+v", got.ECS, want)
	}
}

// A failed call is reported, not mistaken for "nothing found".
func TestDiscoverReportsAWSErrors(t *testing.T) {
	aws := reservesAWS()
	aws.err = errors.New("AccessDenied")
	if _, err := discover.Discover(context.Background(), aws, "reserves-pipeline"); err == nil {
		t.Error("expected the AWS error to be returned")
	}
}

// A shared tag like ProjectName=dec also matches sibling projects, so the
// likely picks are the ones named after the project.
func TestLikelyKeepsWhatIsNamedAfterTheProject(t *testing.T) {
	r := discover.Result{
		Stacks: []string{"dec-prod-beehive", "dec-prod-foundation", "dec-prod-honeycomb", "dec-test-honeycomb"},
		ECS: []config.ECSConfig{
			{Cluster: "dec-prod-foundation-Cluster", Services: []string{"dec-prod-honeycomb-App", "dec-prod-honeypot-App"}},
			{Cluster: "dec-other-Cluster", Services: []string{"dec-prod-beehive-App"}},
		},
	}
	got := r.Likely("Honeycomb")
	want := discover.Result{
		Stacks: []string{"dec-prod-honeycomb", "dec-test-honeycomb"},
		ECS:    []config.ECSConfig{{Cluster: "dec-prod-foundation-Cluster", Services: []string{"dec-prod-honeycomb-App"}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Likely = %+v, want %+v", got, want)
	}
}

// When nothing is named after the project, the tag match is all there is to
// go on, so everything stays likely.
func TestLikelyKeepsEverythingWhenNothingIsNamedAfterTheProject(t *testing.T) {
	r := discover.Result{
		Stacks: []string{"svc-a", "svc-b"},
		ECS:    []config.ECSConfig{{Cluster: "c", Services: []string{"app"}}},
	}
	if got := r.Likely("reserves"); !reflect.DeepEqual(got, r) {
		t.Errorf("Likely = %+v, want everything %+v", got, r)
	}
}
