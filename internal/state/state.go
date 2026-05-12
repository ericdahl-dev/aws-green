package state

import (
	"time"

	awsclient "github.com/ericdahl-dev/aws-green/internal/aws"
	"github.com/ericdahl-dev/aws-green/internal/aggregator"
)

// StageState holds display state for a single Pipeline stage.
type StageState struct {
	Name      string
	Status    aggregator.ExecutionStatus
	StartedAt *time.Time
	EndedAt   *time.Time
}

// PipelineState holds the current display state for a single Pipeline.
type PipelineState struct {
	Account   string
	Name      string
	Stoplight aggregator.Stoplight
	Stages    []StageState
	StaleAt   *time.Time
	Err       error
}

func (p PipelineState) FullName() string {
	if p.Account != "" {
		return p.Account + " / " + p.Name
	}
	return p.Name
}

func (p PipelineState) IsStale() bool {
	return p.StaleAt != nil
}

// FromData converts a PipelineData fetch result into a PipelineState.
func FromData(account string, d awsclient.PipelineData) PipelineState {
	stages := make([]StageState, len(d.Stages))
	for i, s := range d.Stages {
		stages[i] = StageState{Name: s.Name, Status: s.Status, StartedAt: s.StartedAt, EndedAt: s.EndedAt}
	}

	statuses := make([]aggregator.ExecutionStatus, len(d.Stages))
	for i, s := range d.Stages {
		statuses[i] = s.Status
	}

	return PipelineState{
		Account:   account,
		Name:      d.Name,
		Stoplight: aggregator.Aggregate(statuses),
		Stages:    stages,
	}
}

// Snapshot is an immutable view of all pipeline states at a point in time.
type Snapshot struct {
	Pipelines []PipelineState
	UpdatedAt time.Time
}

// New creates a fresh Snapshot from a slice of PipelineStates.
func New(pipelines []PipelineState) Snapshot {
	copied := make([]PipelineState, len(pipelines))
	copy(copied, pipelines)
	return Snapshot{
		Pipelines: copied,
		UpdatedAt: time.Now(),
	}
}
