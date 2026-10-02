package health_test

import (
	"testing"

	"github.com/ericdahl-dev/aws-green/internal/health"
)

// A crash-looping Service is restarted fast enough to keep its full task
// count, so it has to be stuck on its failing tasks, not on counts — the same
// signal that already turns it red.
func TestServiceCrashLoopIsStuck(t *testing.T) {
	sv := health.Service{Running: 2, Desired: 2, FailingTasks: 3}
	if got := sv.Stoplight(); got != health.StoplightRed {
		t.Fatalf("Stoplight() = %v, want red", got)
	}
	if got := sv.StuckReason(); got != "ecs_tasks_failing" {
		t.Errorf("StuckReason() = %q, want ecs_tasks_failing", got)
	}
}

func TestServiceStoplight(t *testing.T) {
	cases := []struct {
		name string
		sv   health.Service
		want health.Stoplight
	}{
		{"healthy", health.Service{Running: 2, Desired: 2}, health.StoplightGreen},
		{"deploying", health.Service{Running: 2, Desired: 2, ActiveDeployment: true}, health.StoplightYellow},
		{"scaling_up", health.Service{Running: 1, Desired: 2}, health.StoplightYellow},
		{"pending_tasks", health.Service{Running: 2, Desired: 2, Pending: 1}, health.StoplightYellow},
		{"zero_desired_zero_running", health.Service{}, health.StoplightGray},
		{"down", health.Service{Running: 0, Desired: 2}, health.StoplightRed},
		{"failing_tasks", health.Service{Running: 2, Desired: 2, FailingTasks: 1}, health.StoplightRed},
		{"failing_tasks_with_running", health.Service{Running: 1, Desired: 2, FailingTasks: 2}, health.StoplightRed},
	}
	for _, tc := range cases {
		if got := tc.sv.Stoplight(); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestServiceStuckReason(t *testing.T) {
	cases := []struct {
		name string
		sv   health.Service
		want string
	}{
		{"healthy", health.Service{Running: 2, Desired: 2}, ""},
		{"scaled_to_zero", health.Service{}, ""},
		{"under_provisioned", health.Service{Running: 1, Desired: 3}, "ecs_count_mismatch"},
		// A service running more tasks than desired has not converged either.
		{"over_provisioned", health.Service{Running: 4, Desired: 3}, "ecs_count_mismatch"},
		{"failing_outranks_counts", health.Service{Running: 1, Desired: 3, FailingTasks: 1}, "ecs_tasks_failing"},
	}
	for _, tc := range cases {
		if got := tc.sv.StuckReason(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
