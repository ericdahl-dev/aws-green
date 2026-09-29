package fix_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ericdahl-dev/aws-green/internal/fix"
)

func actionerOf(a fix.Actioner) fix.ActionerFactory {
	return func(string, string) (fix.Actioner, error) { return a, nil }
}

func TestApply(t *testing.T) {
	approve := fix.PlanApproval(approvalProject("tok"), true)
	approve.Profile = "libnd-view"

	cases := []struct {
		name    string
		plan    *fix.FixPlan
		factory fix.ActionerFactory
		want    fix.Outcome
	}{
		{
			name:    "success refreshes",
			plan:    approve,
			factory: actionerOf(&fakeActioner{}),
			want:    fix.Outcome{Message: "✓ approve", Refresh: true},
		},
		{
			// Someone decided in the console since the last poll: not a
			// failure, and a re-poll catches the row up.
			name:    "already decided refreshes, not a failure",
			plan:    approve,
			factory: actionerOf(&fakeActioner{err: fix.ErrApprovalAlreadyDecided}),
			want:    fix.Outcome{Message: "approval already decided elsewhere — refreshing", Refresh: true},
		},
		{
			name:    "not permitted names the profile and permission",
			plan:    approve,
			factory: actionerOf(&fakeActioner{err: fix.ErrApprovalNotPermitted}),
			want:    fix.Outcome{Message: "profile libnd-view can't approve — it needs codepipeline:PutApprovalResult", Failed: true},
		},
		{
			name:    "other errors fail",
			plan:    approve,
			factory: actionerOf(&fakeActioner{err: errors.New("throttled")}),
			want:    fix.Outcome{Message: "fix failed: throttled", Failed: true},
		},
		{
			name: "no actioner fails",
			plan: approve,
			factory: func(string, string) (fix.Actioner, error) {
				return nil, errors.New("no profile")
			},
			want: fix.Outcome{Message: "fix failed: build actioner: no profile", Failed: true},
		},
	}
	for _, tc := range cases {
		if got := fix.Apply(context.Background(), tc.plan, tc.factory); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// Without a profile the credentials come from the default chain, so the
// message can't name one.
func TestApplyNotPermittedWithoutProfile(t *testing.T) {
	plan := fix.PlanApproval(approvalProject("tok"), false)
	plan.Profile = ""
	got := fix.Apply(context.Background(), plan, actionerOf(&fakeActioner{err: fix.ErrApprovalNotPermitted}))
	if want := "these credentials can't reject — it needs codepipeline:PutApprovalResult"; got.Message != want {
		t.Errorf("Message = %q, want %q", got.Message, want)
	}
}
