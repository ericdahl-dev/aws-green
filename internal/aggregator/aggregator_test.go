package aggregator_test

import (
	"testing"

	"github.com/ericdahl-dev/aws-green/internal/aggregator"
)

func TestAggregate_empty(t *testing.T) {
	got := aggregator.Aggregate(nil)
	if got != aggregator.StoplightGrey {
		t.Errorf("expected grey, got %v", got)
	}
}

func TestAggregate_succeeded(t *testing.T) {
	got := aggregator.Aggregate([]aggregator.ExecutionStatus{aggregator.StatusSucceeded})
	if got != aggregator.StoplightGreen {
		t.Errorf("expected green, got %v", got)
	}
}

func TestAggregate_failedWins(t *testing.T) {
	got := aggregator.Aggregate([]aggregator.ExecutionStatus{aggregator.StatusSucceeded, aggregator.StatusFailed})
	if got != aggregator.StoplightRed {
		t.Errorf("expected red, got %v", got)
	}
}

func TestAggregate_inProgress(t *testing.T) {
	got := aggregator.Aggregate([]aggregator.ExecutionStatus{aggregator.StatusSucceeded, aggregator.StatusInProgress})
	if got != aggregator.StoplightYellow {
		t.Errorf("expected yellow, got %v", got)
	}
}

func TestAggregate_redBeatsYellow(t *testing.T) {
	got := aggregator.Aggregate([]aggregator.ExecutionStatus{aggregator.StatusInProgress, aggregator.StatusFailed})
	if got != aggregator.StoplightRed {
		t.Errorf("expected red, got %v", got)
	}
}

func TestStoplight_String(t *testing.T) {
	cases := []struct {
		light aggregator.Stoplight
		want  string
	}{
		{aggregator.StoplightGreen, "🟢"},
		{aggregator.StoplightRed, "🔴"},
		{aggregator.StoplightYellow, "🟡"},
		{aggregator.StoplightGrey, "⚪"},
	}
	for _, tc := range cases {
		if got := tc.light.String(); got != tc.want {
			t.Errorf("Stoplight(%d).String() = %q, want %q", tc.light, got, tc.want)
		}
	}
}
