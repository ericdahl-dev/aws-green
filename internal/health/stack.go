package health

import "strings"

// StackStatus is a CloudFormation stack status, e.g. UPDATE_IN_PROGRESS. It is
// the one place that reads the raw string, so the Stoplight, the stuck alert
// and the fix plan can't each grow their own rules for it.
type StackStatus string

// InProgress reports whether CloudFormation is still working on the stack.
// REVIEW_IN_PROGRESS is excluded: a change set waiting on review is parked on
// a person, not running.
func (s StackStatus) InProgress() bool {
	return strings.HasSuffix(string(s), "_IN_PROGRESS") && s != "REVIEW_IN_PROGRESS"
}

// Failed reports whether the stack's last operation failed.
func (s StackStatus) Failed() bool {
	return strings.HasSuffix(string(s), "_FAILED")
}

// RollingBack reports whether CloudFormation is reverting a failed create or
// update. The cleanup that follows a finished rollback doesn't count.
func (s StackStatus) RollingBack() bool {
	return s == "ROLLBACK_IN_PROGRESS" || s == "UPDATE_ROLLBACK_IN_PROGRESS"
}

// RollbackFailed reports whether an update's rollback failed, leaving the
// stack wedged until ContinueUpdateRollback recovers it.
func (s StackStatus) RollbackFailed() bool {
	return s == "UPDATE_ROLLBACK_FAILED"
}

// Cancellable reports whether CancelUpdateStack can stop the operation in
// progress. AWS accepts it only for an update, not a create, delete or
// rollback.
func (s StackStatus) Cancellable() bool {
	return s == "UPDATE_IN_PROGRESS"
}

// StuckReason names what is wrong with a stack that would count as stuck
// once it outlives the threshold, or "" when nothing is.
func (s StackStatus) StuckReason() string {
	switch {
	case s.Failed():
		return "stack_failed"
	case s.InProgress():
		return "stack_in_progress"
	}
	return ""
}

// Stoplight maps the stack status to a Stoplight.
func (s StackStatus) Stoplight() Stoplight {
	status := string(s)
	switch {
	case strings.HasSuffix(status, "_COMPLETE") &&
		!strings.HasPrefix(status, "DELETE") &&
		!strings.HasPrefix(status, "ROLLBACK_COMPLETE"):
		return StoplightGreen
	case status == "ROLLBACK_COMPLETE":
		return StoplightGreen
	case strings.HasSuffix(status, "_FAILED"),
		strings.HasPrefix(status, "ROLLBACK_") && !strings.HasSuffix(status, "_COMPLETE"),
		strings.HasPrefix(status, "DELETE_"):
		return StoplightRed
	case status == "REVIEW_IN_PROGRESS":
		return StoplightGray
	case strings.HasSuffix(status, "_IN_PROGRESS"):
		return StoplightYellow
	default:
		return StoplightGray
	}
}
