package health_test

import (
	"testing"

	"github.com/ericdahl-dev/aws-green/internal/health"
)

func TestStackStuckReason(t *testing.T) {
	cases := []struct {
		status health.StackStatus
		want   string
	}{
		{"CREATE_COMPLETE", ""},
		{"UPDATE_COMPLETE", ""},
		{"UPDATE_IN_PROGRESS", "stack_in_progress"},
		{"CREATE_IN_PROGRESS", "stack_in_progress"},
		{"UPDATE_ROLLBACK_IN_PROGRESS", "stack_in_progress"},
		{"CREATE_FAILED", "stack_failed"},
		{"UPDATE_ROLLBACK_FAILED", "stack_failed"},
		{"DELETE_FAILED", "stack_failed"},
		// A change set waiting on review shows grey: it's parked on a person,
		// not a wedged deploy.
		{"REVIEW_IN_PROGRESS", ""},
	}
	for _, tc := range cases {
		if got := tc.status.StuckReason(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestStackStoplight(t *testing.T) {
	cases := []struct {
		status health.StackStatus
		want   health.Stoplight
	}{
		{"CREATE_COMPLETE", health.StoplightGreen},
		{"UPDATE_COMPLETE", health.StoplightGreen},
		{"ROLLBACK_COMPLETE", health.StoplightGreen},
		{"CREATE_FAILED", health.StoplightRed},
		{"ROLLBACK_FAILED", health.StoplightRed},
		{"DELETE_FAILED", health.StoplightRed},
		{"ROLLBACK_IN_PROGRESS", health.StoplightRed},
		{"DELETE_IN_PROGRESS", health.StoplightRed},
		{"CREATE_IN_PROGRESS", health.StoplightYellow},
		{"UPDATE_IN_PROGRESS", health.StoplightYellow},
		{"UPDATE_ROLLBACK_IN_PROGRESS", health.StoplightYellow},
		{"REVIEW_IN_PROGRESS", health.StoplightGrey},
		{"", health.StoplightGrey},
	}

	for _, tc := range cases {
		got := tc.status.Stoplight()
		if got != tc.want {
			t.Errorf("%s.Stoplight() = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestStackRollingBack(t *testing.T) {
	for status, want := range map[health.StackStatus]bool{
		"ROLLBACK_IN_PROGRESS":                         true,
		"UPDATE_ROLLBACK_IN_PROGRESS":                  true,
		"UPDATE_ROLLBACK_COMPLETE_CLEANUP_IN_PROGRESS": false,
		"UPDATE_IN_PROGRESS":                           false,
		"UPDATE_ROLLBACK_FAILED":                       false,
	} {
		if got := status.RollingBack(); got != want {
			t.Errorf("%s.RollingBack() = %v, want %v", status, got, want)
		}
	}
}
