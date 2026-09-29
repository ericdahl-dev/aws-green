package cfn

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/smithy-go"
	"github.com/ericdahl-dev/aws-green/internal/health"
)

// fakeDescribeStacks answers DescribeStacks per stack name.
type fakeDescribeStacks struct {
	stacks map[string]cfntypes.StackStatus
	errs   map[string]error
}

func (f fakeDescribeStacks) DescribeStacks(_ context.Context, in *cloudformation.DescribeStacksInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	name := *in.StackName
	if err, ok := f.errs[name]; ok {
		return nil, err
	}
	status, ok := f.stacks[name]
	if !ok {
		return &cloudformation.DescribeStacksOutput{}, nil
	}
	return &cloudformation.DescribeStacksOutput{Stacks: []cfntypes.Stack{{StackName: &name, StackStatus: status}}}, nil
}

// A failed call has to reach the poller as an error, so it keeps the last
// known stacks on screen marked stale instead of turning them grey.
func TestFetchStacksReturnsCallErrors(t *testing.T) {
	c := &Client{svc: fakeDescribeStacks{
		stacks: map[string]cfntypes.StackStatus{"ok": cfntypes.StackStatusUpdateComplete},
		errs:   map[string]error{"bad": errors.New("ExpiredToken: the SSO session has expired")},
	}}
	if _, err := c.FetchStacks(context.Background(), []string{"ok", "bad"}); err == nil {
		t.Fatal("expected the DescribeStacks error to be returned")
	}
}

// A stack that doesn't exist is an answer, not a failed call: AWS reports it
// as a ValidationError, and it shows grey without marking the fetch stale.
func TestFetchStacksReportsMissingStackAsNotFound(t *testing.T) {
	c := &Client{svc: fakeDescribeStacks{
		stacks: map[string]cfntypes.StackStatus{"ok": cfntypes.StackStatusUpdateComplete},
		errs: map[string]error{"gone": &smithy.GenericAPIError{
			Code: "ValidationError", Message: "Stack with id gone does not exist",
		}},
	}}
	got, err := c.FetchStacks(context.Background(), []string{"ok", "gone"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 stacks, got %+v", got)
	}
	if got[0].Stoplight != health.StoplightGreen {
		t.Errorf("ok: Stoplight = %v, want green", got[0].Stoplight)
	}
	if got[1].Status != "NOT_FOUND" || got[1].Stoplight != health.StoplightGrey {
		t.Errorf("gone: got %+v, want NOT_FOUND grey", got[1])
	}
}
