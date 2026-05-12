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

// ProjectState holds the current display state for a single Project.
type ProjectState struct {
	Name     string
	Account  string
	Pipeline PipelineState
}

// Stoplight returns the worst-case stoplight across all project resources.
// Currently derived from the pipeline only; future slices will add stacks and ECS.
func (p ProjectState) Stoplight() aggregator.Stoplight {
	return p.Pipeline.Stoplight
}

// Snapshot is an immutable view of all project states at a point in time.
type Snapshot struct {
	Projects  []ProjectState
	UpdatedAt time.Time
}

// NewFromProjects creates a fresh Snapshot from a slice of ProjectStates.
func NewFromProjects(projects []ProjectState) Snapshot {
	copied := make([]ProjectState, len(projects))
	copy(copied, projects)
	return Snapshot{
		Projects:  copied,
		UpdatedAt: time.Now(),
	}
}
