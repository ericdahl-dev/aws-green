package cfn_test

import (
	"testing"

	"github.com/ericdahl-dev/aws-green/internal/aggregator"
	"github.com/ericdahl-dev/aws-green/internal/cfn"
)

func TestStackStatusToStoplight(t *testing.T) {
	cases := []struct {
		status string
		want   aggregator.Stoplight
	}{
		{"CREATE_COMPLETE", aggregator.StoplightGreen},
		{"UPDATE_COMPLETE", aggregator.StoplightGreen},
		{"ROLLBACK_COMPLETE", aggregator.StoplightGreen},
		{"CREATE_FAILED", aggregator.StoplightRed},
		{"ROLLBACK_FAILED", aggregator.StoplightRed},
		{"DELETE_FAILED", aggregator.StoplightRed},
		{"ROLLBACK_IN_PROGRESS", aggregator.StoplightRed},
		{"DELETE_IN_PROGRESS", aggregator.StoplightRed},
		{"CREATE_IN_PROGRESS", aggregator.StoplightYellow},
		{"UPDATE_IN_PROGRESS", aggregator.StoplightYellow},
		{"UPDATE_ROLLBACK_IN_PROGRESS", aggregator.StoplightYellow},
		{"REVIEW_IN_PROGRESS", aggregator.StoplightGrey},
		{"", aggregator.StoplightGrey},
	}

	for _, tc := range cases {
		got := cfn.StackStatusToStoplight(tc.status)
		if got != tc.want {
			t.Errorf("StackStatusToStoplight(%q) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestStackData_fields(t *testing.T) {
	sd := cfn.StackData{
		Name:      "my-stack",
		Status:    "CREATE_COMPLETE",
		Stoplight: aggregator.StoplightGreen,
	}
	if sd.Name != "my-stack" {
		t.Errorf("expected name my-stack, got %s", sd.Name)
	}
	if sd.Stoplight != aggregator.StoplightGreen {
		t.Errorf("expected green stoplight")
	}
}
