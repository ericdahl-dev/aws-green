package ecs_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
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
		{"Scaling activity initiated by (deployment ecs-svc/4770560200895743049)", false},
		{"Service scheduler initiated action", false},
		{"Service scheduler initiated action by (deployment ecs-svc/123)", false},
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

func stoppedTask(reason string, stoppedAt time.Time) ecstypes.Task {
	t := ecstypes.Task{StoppedReason: aws.String(reason)}
	if !stoppedAt.IsZero() {
		t.StoppedAt = aws.Time(stoppedAt)
	}
	return t
}

func TestCountFailingTasks(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 30, 0, 0, time.UTC)

	cases := []struct {
		name       string
		tasks      []ecstypes.Task
		wantCount  int
		wantReason string
	}{
		{
			name:      "no tasks",
			tasks:     nil,
			wantCount: 0,
		},
		{
			name:      "deployment scale-down is not a failure",
			tasks:     []ecstypes.Task{stoppedTask("Scaling activity initiated by (deployment ecs-svc/477)", now.Add(-time.Minute))},
			wantCount: 0,
		},
		{
			name:       "recent failure counts",
			tasks:      []ecstypes.Task{stoppedTask("Task failed container health checks", now.Add(-2*time.Minute))},
			wantCount:  1,
			wantReason: "Task failed container health checks",
		},
		{
			name:      "failure outside the window is ignored",
			tasks:     []ecstypes.Task{stoppedTask("Task failed container health checks", now.Add(-45*time.Minute))},
			wantCount: 0,
		},
		{
			name:      "missing stoppedAt is ignored",
			tasks:     []ecstypes.Task{stoppedTask("OutOfMemoryError: Container killed", time.Time{})},
			wantCount: 0,
		},
		{
			name: "reason comes from the most recent failure",
			tasks: []ecstypes.Task{
				stoppedTask("CannotPullContainerError: ref pull has been retried", now.Add(-10*time.Minute)),
				stoppedTask("Task failed container health checks", now.Add(-1*time.Minute)),
				stoppedTask("Scaling activity initiated by (deployment ecs-svc/477)", now.Add(-30*time.Second)),
			},
			wantCount:  2,
			wantReason: "Task failed container health checks",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			count, reason := ecs.CountFailingTasks(tc.tasks, now)
			if count != tc.wantCount {
				t.Errorf("count = %d, want %d", count, tc.wantCount)
			}
			if reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}
