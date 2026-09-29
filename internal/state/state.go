package state

import (
	"time"

	"github.com/ericdahl-dev/aws-green/internal/health"
)

// ActionState holds display state for a single pipeline action.
type ActionState struct {
	Name   string
	Status health.ExecutionStatus
	// ApprovalToken is set only while this is a manual approval waiting on a
	// decision. PutApprovalResult needs it, and it goes stale once anyone
	// approves, rejects, or the request times out.
	ApprovalToken string
}

// AwaitingApproval reports whether this action is a manual approval waiting
// on a decision.
func (a ActionState) AwaitingApproval() bool {
	return a.ApprovalToken != ""
}

// PendingApproval identifies an open approval request: everything
// PutApprovalResult needs besides the pipeline name and the decision.
type PendingApproval struct {
	StageName  string
	ActionName string
	Token      string
}

// StageState holds display state for a single Pipeline stage.
type StageState struct {
	Name      string
	Status    health.ExecutionStatus
	StartedAt *time.Time // non-nil when the stage has started
	EndedAt   *time.Time // non-nil when the stage has finished
	Actions   []ActionState
}

// PipelineState holds the current display state for a single Pipeline.
type PipelineState struct {
	Account   string
	Name      string
	Stoplight health.Stoplight
	Stages    []StageState
	// FetchStatus is set when the last fetch failed and Stages are carried
	// forward from an earlier cycle.
	FetchStatus
}

// PendingApproval returns the first approval waiting on a decision, or nil.
func (p PipelineState) PendingApproval() *PendingApproval {
	for _, s := range p.Stages {
		for _, a := range s.Actions {
			if a.AwaitingApproval() {
				return &PendingApproval{StageName: s.Name, ActionName: a.Name, Token: a.ApprovalToken}
			}
		}
	}
	return nil
}

// NewPipeline builds a PipelineState from its stages, deriving the
// Stoplight: the worst stage status, raised to awaiting approval when an
// approval is open, since stage statuses alone can't tell an approval gate
// from a running build. The Account is left for the caller.
func NewPipeline(name string, stages []StageState) PipelineState {
	statuses := make([]health.ExecutionStatus, len(stages))
	for i, s := range stages {
		statuses[i] = s.Status
	}
	ps := PipelineState{
		Name:      name,
		Stoplight: health.Aggregate(statuses),
		Stages:    stages,
	}
	if ps.PendingApproval() != nil && ps.Stoplight < health.StoplightAwaitingApproval {
		ps.Stoplight = health.StoplightAwaitingApproval
	}
	return ps
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
	Status    health.StackStatus
	Stoplight health.Stoplight
	StartedAt *time.Time
}

// ECSServiceState holds the current display state for an ECS service.
type ECSServiceState struct {
	Name             string
	Cluster          string
	RunningCount     int32
	DesiredCount     int32
	PendingCount     int32
	ActiveDeployment bool
	Stoplight        health.Stoplight
	FailingTaskCount int
	StoppedReason    string
}

// Health returns the part of the Service's state that decides its health.
func (s ECSServiceState) Health() health.Service {
	return health.Service{
		Running:          s.RunningCount,
		Desired:          s.DesiredCount,
		Pending:          s.PendingCount,
		ActiveDeployment: s.ActiveDeployment,
		FailingTasks:     s.FailingTaskCount,
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
func (p ProjectState) Stoplight() health.Stoplight {
	return max(p.Pipeline.Stoplight, p.StacksStoplight(), p.ECSStoplight())
}

// StacksStoplight returns the worst-case stoplight across the project's
// stacks, or grey when it has none.
func (p ProjectState) StacksStoplight() health.Stoplight {
	worst := health.StoplightGrey
	for _, s := range p.Stacks {
		worst = max(worst, s.Stoplight)
	}
	return worst
}

// ECSStoplight returns the worst-case stoplight across the project's ECS
// services, or grey when it has none.
func (p ProjectState) ECSStoplight() health.Stoplight {
	worst := health.StoplightGrey
	for _, s := range p.ECSServices {
		worst = max(worst, s.Stoplight)
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
