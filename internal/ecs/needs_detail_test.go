package ecs_test

import (
	"testing"

	"github.com/ericdahl-dev/aws-green/internal/ecs"
)

func TestNeedsTaskDetail(t *testing.T) {
	cases := []struct {
		name           string
		sd             ecs.ServiceData
		deployFailures int32
		want           bool
	}{
		// The case the gate exists for: everything is fine, so there are no
		// stopped tasks to explain and the two calls would be wasted.
		{"healthy", sd(2, 2, 0, false, 0), 0, false},
		// A scaled-to-zero service is deliberately down, not broken.
		{"scaled to zero", sd(0, 0, 0, false, 0), 0, false},

		{"down", sd(0, 2, 0, false, 0), 0, true},
		{"scaling up", sd(1, 2, 0, false, 0), 0, true},
		{"pending tasks", sd(2, 2, 1, false, 0), 0, true},
		{"deploying", sd(2, 2, 0, true, 0), 0, true},

		// A crash-looping service is restarted fast enough to keep reporting
		// its full task count, so only ECS's own failure count catches it.
		// Losing this case would turn a red service green.
		{"crash loop at full count", sd(2, 2, 0, false, 0), 3, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ecs.NeedsTaskDetail(tc.sd, tc.deployFailures); got != tc.want {
				t.Errorf("NeedsTaskDetail = %v, want %v", got, tc.want)
			}
		})
	}
}
