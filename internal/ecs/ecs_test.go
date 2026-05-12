package ecs_test

import (
	"testing"

	"github.com/ericdahl-dev/aws-green/internal/aggregator"
	"github.com/ericdahl-dev/aws-green/internal/ecs"
)

func TestServiceStateToStoplight(t *testing.T) {
	cases := []struct {
		name          string
		running       int32
		desired       int32
		activeDeploym bool
		want          aggregator.Stoplight
	}{
		{"healthy", 2, 2, false, aggregator.StoplightGreen},
		{"deploying", 2, 2, true, aggregator.StoplightYellow},
		{"scaling_up", 1, 2, false, aggregator.StoplightYellow},
		{"zero_desired_zero_running", 0, 0, false, aggregator.StoplightGrey},
		{"down", 0, 2, false, aggregator.StoplightRed},
	}

	for _, tc := range cases {
		got := ecs.ServiceStateToStoplight(tc.running, tc.desired, tc.activeDeploym)
		if got != tc.want {
			t.Errorf("%s: ServiceStateToStoplight(%d, %d, %v) = %v, want %v",
				tc.name, tc.running, tc.desired, tc.activeDeploym, got, tc.want)
		}
	}
}

func TestServiceData_fields(t *testing.T) {
	sd := ecs.ServiceData{
		Name:             "web",
		RunningCount:     3,
		DesiredCount:     3,
		ActiveDeployment: false,
		Stoplight:        aggregator.StoplightGreen,
	}
	if sd.Name != "web" {
		t.Errorf("expected name web, got %s", sd.Name)
	}
	if sd.RunningCount != 3 {
		t.Errorf("expected running 3, got %d", sd.RunningCount)
	}
}
