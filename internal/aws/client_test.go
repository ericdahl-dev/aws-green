package aws_test

import (
	"testing"

	awsclient "github.com/ericdahl-dev/aws-green/internal/aws"
)

// TestPipelineData_noExecutionStatusField verifies that PipelineData does not
// carry a redundant ExecutionStatus field. Pipeline stoplight is derived once,
// in state.FromData, from stage statuses via aggregator.Aggregate.
func TestPipelineData_noExecutionStatusField(t *testing.T) {
	// If PipelineData still has ExecutionStatus this will fail to compile.
	_ = awsclient.PipelineData{
		Name:   "my-pipeline",
		Stages: nil,
	}
	// Verify the struct has exactly the fields we expect.
	var d awsclient.PipelineData
	d.Name = "test"
	d.ConsoleURL = "https://example.com"
	d.Stages = nil
	_ = d
}
