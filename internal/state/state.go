package state

import (
	"time"

	awsclient "github.com/ericdahl-dev/aws-green/internal/aws"
	"github.com/ericdahl-dev/aws-green/internal/aggregator"
	"github.com/ericdahl-dev/aws-green/internal/cfn"
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

// StackState holds the current display state for a CloudFormation stack.
type StackState struct {
	Name      string
	Status    string
	Stoplight aggregator.Stoplight
	StartedAt *time.Time
}

// StackStateFromData converts a cfn.StackData into a StackState.
func StackStateFromData(d cfn.StackData) StackState {
	return StackState{
		Name:      d.Name,
		Status:    d.Status,
		Stoplight: d.Stoplight,
		StartedAt: d.StartedAt,
	}
}

// ProjectState holds the current display state for a single Project.
type ProjectState struct {
	Name     string
	Account  string
	Pipeline PipelineState
	Stacks   []StackState
}

// Stoplight returns the worst-case stoplight across all project resources.
func (p ProjectState) Stoplight() aggregator.Stoplight {
	worst := p.Pipeline.Stoplight
	for _, s := range p.Stacks {
		if s.Stoplight > worst {
			worst = s.Stoplight
		}
	}
	return worst
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
