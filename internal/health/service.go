package health

// Service is the part of an ECS Service's state that decides its health. It
// is the one place that reads the task counts, so the Stoplight on screen and
// the stuck alert can't disagree about what counts as broken.
type Service struct {
	Running          int32
	Desired          int32
	Pending          int32
	ActiveDeployment bool
	// FailingTasks is the Task detail count: tasks that stopped for a
	// failing reason recently enough to still reflect the Service's health.
	FailingTasks int
}

// Stoplight maps the Service's state to a Stoplight.
func (s Service) Stoplight() Stoplight {
	switch {
	case s.Desired == 0 && s.Running == 0:
		return StoplightGrey
	case s.Running == 0 && s.Desired > 0:
		return StoplightRed
	case s.FailingTasks > 0:
		return StoplightRed
	case s.Pending > 0:
		return StoplightYellow
	case s.ActiveDeployment:
		return StoplightYellow
	case s.Running != s.Desired:
		return StoplightYellow
	default:
		return StoplightGreen
	}
}

// StuckReason names what is wrong with a Service that would count as stuck
// once it outlives the threshold, or "" when nothing is. Failing tasks come
// first: a crash-looping Service keeps its full task count, so counts alone
// would never flag it.
func (s Service) StuckReason() string {
	switch {
	case s.FailingTasks > 0:
		return "ecs_tasks_failing"
	case s.Running != s.Desired:
		return "ecs_count_mismatch"
	}
	return ""
}
