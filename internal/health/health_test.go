package health_test

import (
	"testing"

	"github.com/ericdahl-dev/aws-green/internal/health"
)

func TestAggregate_empty(t *testing.T) {
	got := health.Aggregate(nil)
	if got != health.StoplightGrey {
		t.Errorf("expected grey, got %v", got)
	}
}

func TestAggregate_succeeded(t *testing.T) {
	got := health.Aggregate([]health.ExecutionStatus{health.StatusSucceeded})
	if got != health.StoplightGreen {
		t.Errorf("expected green, got %v", got)
	}
}

func TestAggregate_failedWins(t *testing.T) {
	got := health.Aggregate([]health.ExecutionStatus{health.StatusSucceeded, health.StatusFailed})
	if got != health.StoplightRed {
		t.Errorf("expected red, got %v", got)
	}
}

func TestAggregate_inProgress(t *testing.T) {
	got := health.Aggregate([]health.ExecutionStatus{health.StatusSucceeded, health.StatusInProgress})
	if got != health.StoplightYellow {
		t.Errorf("expected yellow, got %v", got)
	}
}

func TestAggregate_redBeatsYellow(t *testing.T) {
	got := health.Aggregate([]health.ExecutionStatus{health.StatusInProgress, health.StatusFailed})
	if got != health.StoplightRed {
		t.Errorf("expected red, got %v", got)
	}
}

func TestStoplight_String(t *testing.T) {
	cases := []struct {
		light health.Stoplight
		want  string
	}{
		{health.StoplightGreen, "🟢"},
		{health.StoplightRed, "🔴"},
		{health.StoplightYellow, "🟡"},
		{health.StoplightGrey, "⚪"},
	}
	for _, tc := range cases {
		if got := tc.light.String(); got != tc.want {
			t.Errorf("Stoplight(%d).String() = %q, want %q", tc.light, got, tc.want)
		}
	}
}

// A pipeline waiting on a person outranks one that is merely running, but a
// failure anywhere still outranks both.
func TestStoplightAwaitingApprovalOrdering(t *testing.T) {
	if health.StoplightAwaitingApproval <= health.StoplightYellow {
		t.Error("awaiting approval should outrank yellow")
	}
	if health.StoplightRed <= health.StoplightAwaitingApproval {
		t.Error("red should outrank awaiting approval")
	}
	if got := health.StoplightAwaitingApproval.String(); got != "⏸" {
		t.Errorf("String() = %q, want ⏸", got)
	}
}
