package ecs_test

import (
	"testing"

	"github.com/ericdahl-dev/aws-green/internal/aggregator"
	"github.com/ericdahl-dev/aws-green/internal/ecs"
)

func sd(running, desired, pending int32, activeDeployment bool, failingTasks int) ecs.ServiceData {
	return ecs.ServiceData{
		RunningCount:     running,
		DesiredCount:     desired,
		PendingCount:     pending,
		ActiveDeployment: activeDeployment,
		FailingTaskCount: failingTasks,
	}
}

func TestServiceStateToStoplight(t *testing.T) {
	cases := []struct {
		name string
		sd   ecs.ServiceData
		want aggregator.Stoplight
	}{
		{"healthy", sd(2, 2, 0, false, 0), aggregator.StoplightGreen},
		{"deploying", sd(2, 2, 0, true, 0), aggregator.StoplightYellow},
		{"scaling_up", sd(1, 2, 0, false, 0), aggregator.StoplightYellow},
		{"pending_tasks", sd(2, 2, 1, false, 0), aggregator.StoplightYellow},
		{"zero_desired_zero_running", sd(0, 0, 0, false, 0), aggregator.StoplightGrey},
		{"down", sd(0, 2, 0, false, 0), aggregator.StoplightRed},
		{"failing_tasks", sd(2, 2, 0, false, 1), aggregator.StoplightRed},
		{"failing_tasks_with_running", sd(1, 2, 0, false, 2), aggregator.StoplightRed},
	}

	for _, tc := range cases {
		got := ecs.ServiceStateToStoplight(tc.sd)
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestServiceData_fields(t *testing.T) {
	s := ecs.ServiceData{
		Name:             "web",
		RunningCount:     3,
		DesiredCount:     3,
		ActiveDeployment: false,
		Stoplight:        aggregator.StoplightGreen,
	}
	if s.Name != "web" {
		t.Errorf("expected name web, got %s", s.Name)
	}
	if s.RunningCount != 3 {
		t.Errorf("expected running 3, got %d", s.RunningCount)
	}
}

func TestIsFailingStopReason(t *testing.T) {
	cases := []struct {
		reason string
		want   bool
	}{
		{"", false},
		{"Scaling activity initiated", false},
		{"Service scheduler initiated action", false},
		{"Task stopped by user", false},
		{"Essential container in task exited", false},
		{"Essential container in task exited (exit code 1)", true},
		{"Task failed ELB health checks", true},
		{"CannotPullContainerError: ref pull has been retried", true},
		{"OutOfMemoryError: Container killed due to memory", true},
		{"some unexpected reason", true},
	}

	for _, tc := range cases {
		got := ecs.IsFailingStopReason(tc.reason)
		if got != tc.want {
			t.Errorf("IsFailingStopReason(%q) = %v, want %v", tc.reason, got, tc.want)
		}
	}
}
