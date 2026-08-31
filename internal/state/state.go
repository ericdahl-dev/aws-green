package state

import (
	"time"

	awsclient "github.com/ericdahl-dev/aws-green/internal/aws"
	"github.com/ericdahl-dev/aws-green/internal/aggregator"
	"github.com/ericdahl-dev/aws-green/internal/cfn"
	"github.com/ericdahl-dev/aws-green/internal/ecs"
)

// ActionState holds display state for a single pipeline action.
type ActionState struct {
	Name   string
	Status aggregator.ExecutionStatus
}

// StageState holds display state for a single Pipeline stage.
type StageState struct {
	Name      string
	Status    aggregator.ExecutionStatus
	StartedAt *time.Time
	EndedAt   *time.Time
	Actions   []ActionState
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
		actions := make([]ActionState, len(s.Actions))
		for j, a := range s.Actions {
			actions[j] = ActionState{Name: a.Name, Status: a.Status}
		}
		stages[i] = StageState{Name: s.Name, Status: s.Status, StartedAt: s.StartedAt, EndedAt: s.EndedAt, Actions: actions}
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

// FetchStatus records the outcome of a resource fetch. Without it a failed
// call is indistinguishable on screen from a project that has nothing of that
// kind configured — both render as an empty list.
type FetchStatus struct {
	StaleAt *time.Time
	Err     error
}

// IsStale reports whether the last fetch failed and the values being shown
// are carried forward from an earlier cycle.
func (f FetchStatus) IsStale() bool {
	return f.StaleAt != nil
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

// ECSServiceState holds the current display state for an ECS service.
type ECSServiceState struct {
	Name             string
	Cluster          string
	RunningCount     int32
	DesiredCount     int32
	PendingCount     int32
	ActiveDeployment bool
	Stoplight        aggregator.Stoplight
	FailingTaskCount int
	StoppedReason    string
}

// ECSServiceStateFromData converts an ecs.ServiceData into an ECSServiceState.
func ECSServiceStateFromData(cluster string, d ecs.ServiceData) ECSServiceState {
	return ECSServiceState{
		Name:             d.Name,
		Cluster:          cluster,
		RunningCount:     d.RunningCount,
		DesiredCount:     d.DesiredCount,
		PendingCount:     d.PendingCount,
		ActiveDeployment: d.ActiveDeployment,
		Stoplight:        d.Stoplight,
		FailingTaskCount: d.FailingTaskCount,
		StoppedReason:    d.StoppedReason,
	}
}

// ProjectState holds the current display state for a single Project.
type ProjectState struct {
	Name        string
	Account     string
	Profile     string
	Region      string
	Pipeline    PipelineState
	Stacks      []StackState
	StacksFetch FetchStatus
	ECSServices []ECSServiceState
	ECSFetch    FetchStatus
}

// Key returns a stable identifier for a project. Project names are not unique
// — the same project is commonly configured once per AWS account — so the
// account has to be part of the identity. Anything that has to tell two rows
// apart (expansion state, cursor resolution) must key on this, not on Name.
func (p ProjectState) Key() string {
	if p.Account == "" {
		return p.Name
	}
	return p.Account + "/" + p.Name
}

// Stoplight returns the worst-case stoplight across all project resources.
func (p ProjectState) Stoplight() aggregator.Stoplight {
	worst := p.Pipeline.Stoplight
	for _, s := range p.Stacks {
		if s.Stoplight > worst {
			worst = s.Stoplight
		}
	}
	for _, s := range p.ECSServices {
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
