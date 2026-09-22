package aggregator

// Stoplight represents the health color for a Pipeline.
type Stoplight int

const (
	StoplightGrey   Stoplight = iota // no executions, or superseded
	StoplightGreen                   // succeeded
	StoplightYellow                  // in progress
	// StoplightAwaitingApproval is a pipeline paused at a manual approval. It
	// outranks yellow because it needs a person, not more time. Aggregate never
	// returns it: execution statuses can't tell a gate from a build, so
	// state.FromData raises it from the action's approval token.
	StoplightAwaitingApproval
	StoplightRed // failed or stopped
)

func (s Stoplight) String() string {
	switch s {
	case StoplightGreen:
		return "🟢"
	case StoplightRed:
		return "🔴"
	case StoplightYellow:
		return "🟡"
	case StoplightAwaitingApproval:
		return "⏸"
	default:
		return "⚪"
	}
}

// ExecutionStatus mirrors the relevant AWS CodePipeline execution status values.
type ExecutionStatus string

const (
	StatusSucceeded  ExecutionStatus = "Succeeded"
	StatusFailed     ExecutionStatus = "Failed"
	StatusStopped    ExecutionStatus = "Stopped"
	StatusInProgress ExecutionStatus = "InProgress"
	StatusSuperseded ExecutionStatus = "Superseded"
)

func statusToStoplight(s ExecutionStatus) Stoplight {
	switch s {
	case StatusSucceeded:
		return StoplightGreen
	case StatusFailed, StatusStopped:
		return StoplightRed
	case StatusInProgress:
		return StoplightYellow
	default:
		return StoplightGrey
	}
}

// Aggregate returns the worst-case Stoplight across all provided execution statuses.
// Red > Yellow > Green > Grey.
func Aggregate(statuses []ExecutionStatus) Stoplight {
	result := StoplightGrey
	for _, s := range statuses {
		light := statusToStoplight(s)
		if light > result {
			result = light
		}
	}
	return result
}
