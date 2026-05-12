package ui

import (
	"context"
	"fmt"
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ericdahl-dev/aws-green/internal/aggregator"
	"github.com/ericdahl-dev/aws-green/internal/fix"
	"github.com/ericdahl-dev/aws-green/internal/state"
)

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).MarginBottom(1)
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	normalStyle   = lipgloss.NewStyle()
	staleStyle    = lipgloss.NewStyle().Faint(true)
	hintStyle     = lipgloss.NewStyle().Faint(true)
	confirmStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("226"))
	successStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	stageIndent   = "        "
)

const selectionTimeout = 10 * time.Second
const timerTickInterval = time.Second
const fixStatusDuration = 5 * time.Second

type selectionExpiredMsg struct{}
type timerTickMsg struct{}
type fixDoneMsg struct{ err error }
type fixStatusExpiredMsg struct{}

// FixAppliedMsg is sent to the parent model when a fix succeeds, triggering a re-poll.
type FixAppliedMsg struct{}

type fixState int

const (
	fixIdle fixState = iota
	fixConfirming
	fixExecuting
	fixShowResult
)

type Dashboard struct {
	snapshot      state.Snapshot
	cursor        int
	expanded      map[int]bool
	lastActivity  time.Time
	selectionFade bool

	actioner  fix.Actioner
	fixCtx    context.Context

	fixStatus    fixState
	fixPlan      *fix.FixPlan
	fixResultMsg string
	fixErr       bool
}

func NewDashboard(snap state.Snapshot, actioner fix.Actioner, ctx context.Context) Dashboard {
	return Dashboard{
		snapshot:     snap,
		expanded:     make(map[int]bool),
		lastActivity: time.Now(),
		actioner:     actioner,
		fixCtx:       ctx,
	}
}

func stoplightPriority(s aggregator.Stoplight) int {
	switch s {
	case aggregator.StoplightYellow:
		return 0
	case aggregator.StoplightRed:
		return 1
	case aggregator.StoplightGreen:
		return 2
	default:
		return 3
	}
}

func sortedProjectOrder(projects []state.ProjectState) []int {
	order := make([]int, len(projects))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		pa := stoplightPriority(projects[order[a]].Stoplight())
		pb := stoplightPriority(projects[order[b]].Stoplight())
		return pa < pb
	})
	return order
}

func selectionTimeoutCmd() tea.Cmd {
	return tea.Tick(selectionTimeout, func(time.Time) tea.Msg {
		return selectionExpiredMsg{}
	})
}

func timerTickCmd() tea.Cmd {
	return tea.Tick(timerTickInterval, func(time.Time) tea.Msg {
		return timerTickMsg{}
	})
}

func fixStatusExpiredCmd() tea.Cmd {
	return tea.Tick(fixStatusDuration, func(time.Time) tea.Msg {
		return fixStatusExpiredMsg{}
	})
}

func (d Dashboard) Init() tea.Cmd {
	return tea.Batch(selectionTimeoutCmd(), timerTickCmd())
}

func (d Dashboard) selectedProject() *state.ProjectState {
	order := sortedProjectOrder(d.snapshot.Projects)
	if d.cursor >= len(order) {
		return nil
	}
	p := d.snapshot.Projects[order[d.cursor]]
	return &p
}

func (d Dashboard) Update(msg tea.Msg) (Dashboard, tea.Cmd) {
	count := len(d.snapshot.Projects)

	switch msg := msg.(type) {
	case tea.KeyMsg:
		// While confirming, only enter/esc are meaningful.
		if d.fixStatus == fixConfirming {
			switch msg.String() {
			case "enter":
				d.fixStatus = fixExecuting
				plan := d.fixPlan
				actioner := d.actioner
				ctx := d.fixCtx
				return d, func() tea.Msg {
					err := fix.Execute(ctx, plan, actioner)
					return fixDoneMsg{err: err}
				}
			case "esc":
				d.fixStatus = fixIdle
				d.fixPlan = nil
			}
			return d, nil
		}

		d.lastActivity = time.Now()
		d.selectionFade = false

		switch msg.String() {
		case "up", "k":
			if d.cursor > 0 {
				d.cursor--
			}
		case "down", "j":
			if d.cursor < count-1 {
				d.cursor++
			}
		case "enter", " ":
			if count > 0 {
				d.expanded[d.cursor] = !d.expanded[d.cursor]
			}
		case "f":
			if proj := d.selectedProject(); proj != nil {
				if plan := fix.Plan(*proj); plan != nil {
					d.fixStatus = fixConfirming
					d.fixPlan = plan
				}
			}
			return d, selectionTimeoutCmd()
		}
		return d, selectionTimeoutCmd()

	case selectionExpiredMsg:
		if time.Since(d.lastActivity) >= selectionTimeout {
			d.selectionFade = true
		}

	case timerTickMsg:
		return d, timerTickCmd()

	case fixDoneMsg:
		if msg.err != nil {
			d.fixStatus = fixShowResult
			d.fixResultMsg = fmt.Sprintf("fix failed: %v", msg.err)
			d.fixErr = true
			return d, tea.Batch(fixStatusExpiredCmd(), func() tea.Msg { return nil })
		}
		d.fixStatus = fixShowResult
		d.fixResultMsg = fmt.Sprintf("✓ %s", d.fixPlan.Kind)
		d.fixErr = false
		return d, tea.Batch(fixStatusExpiredCmd(), func() tea.Msg { return FixAppliedMsg{} })

	case fixStatusExpiredMsg:
		d.fixStatus = fixIdle
		d.fixPlan = nil
		d.fixResultMsg = ""

	case state.Snapshot:
		d.snapshot = msg
		if d.cursor >= len(msg.Projects) && len(msg.Projects) > 0 {
			d.cursor = len(msg.Projects) - 1
		}
	}
	return d, nil
}

func (d Dashboard) SelectedPipeline() *state.PipelineState {
	order := sortedProjectOrder(d.snapshot.Projects)
	if d.cursor >= len(order) {
		return nil
	}
	p := d.snapshot.Projects[order[d.cursor]].Pipeline
	return &p
}

func (d Dashboard) View() string {
	out := titleStyle.Render("aws-green") + "\n"

	if len(d.snapshot.Projects) == 0 {
		out += staleStyle.Render("  No projects configured.") + "\n"
	}

	order := sortedProjectOrder(d.snapshot.Projects)
	for displayIdx, projIdx := range order {
		proj := d.snapshot.Projects[projIdx]
		selected := displayIdx == d.cursor && !d.selectionFade
		expanded := d.expanded[displayIdx]

		triangle := "▶"
		if expanded {
			triangle = "▼"
		}
		line := projectRow(proj)
		if selected {
			out += selectedStyle.Render(triangle+" "+line) + "\n"
		} else {
			out += normalStyle.Render("  "+line) + "\n"
		}

		if expanded {
			out += renderPipelineSection(proj.Pipeline)
			out += renderStacksSection(proj.Stacks)
			out += renderECSSection(proj.ECSServices)
		}
	}

	out += "\n" + d.hintLine()
	return out
}

func (d Dashboard) hintLine() string {
	switch d.fixStatus {
	case fixConfirming:
		return confirmStyle.Render(fmt.Sprintf("%s  [enter] confirm  [esc] cancel", d.fixPlan.Description))
	case fixExecuting:
		return hintStyle.Render(fmt.Sprintf("fixing: %s…", d.fixPlan.Kind))
	case fixShowResult:
		if d.fixErr {
			return errorStyle.Render(d.fixResultMsg)
		}
		return successStyle.Render(d.fixResultMsg)
	default:
		return hintStyle.Render("↑/↓ navigate  enter/space expand  f fix  o open  r refresh  q quit  ? help")
	}
}

func projectRow(proj state.ProjectState) string {
	icon := proj.Stoplight().String()
	name := proj.Name
	if proj.Account != "" {
		name = proj.Account + " / " + proj.Name
	}
	row := fmt.Sprintf("%s  %-30s", icon, name)

	summary := "  Pipeline " + proj.Pipeline.Stoplight.String()
	if len(proj.Stacks) > 0 {
		worst := aggregator.StoplightGrey
		for _, s := range proj.Stacks {
			if s.Stoplight > worst {
				worst = s.Stoplight
			}
		}
		summary += "  Stacks " + worst.String()
	}
	if len(proj.ECSServices) > 0 {
		worst := aggregator.StoplightGrey
		for _, s := range proj.ECSServices {
			if s.Stoplight > worst {
				worst = s.Stoplight
			}
		}
		summary += "  ECS " + worst.String()
	}
	row += hintStyle.Render(summary)

	if proj.Pipeline.IsStale() {
		age := time.Since(*proj.Pipeline.StaleAt).Round(time.Second)
		row += staleStyle.Render(fmt.Sprintf("  ⚠ last seen %s ago", age))
	}
	return row
}

func renderPipelineSection(p state.PipelineState) string {
	out := ""
	if p.Name != "" {
		out += normalStyle.Render("      pipeline  "+p.Name) + "\n"
	}
	out += renderStages(p)
	return out
}

func renderStacksSection(stacks []state.StackState) string {
	if len(stacks) == 0 {
		return ""
	}
	out := normalStyle.Render("      stacks") + "\n"
	for _, s := range stacks {
		icon := s.Stoplight.String()
		timer := ""
		if s.StartedAt != nil && isInProgressStatus(s.Status) {
			timer = " " + staleStyle.Render(formatDuration(time.Since(*s.StartedAt)))
		}
		out += fmt.Sprintf("%s%s  %-40s %s%s\n", stageIndent, icon, s.Name, staleStyle.Render(s.Status), timer)
	}
	return out
}

func isInProgressStatus(status string) bool {
	return status == "CREATE_IN_PROGRESS" || status == "UPDATE_IN_PROGRESS" ||
		status == "UPDATE_ROLLBACK_IN_PROGRESS" || status == "DELETE_IN_PROGRESS" ||
		status == "ROLLBACK_IN_PROGRESS"
}

func renderECSSection(services []state.ECSServiceState) string {
	if len(services) == 0 {
		return ""
	}
	out := normalStyle.Render("      ecs") + "\n"
	for _, s := range services {
		icon := s.Stoplight.String()
		counts := fmt.Sprintf("%d/%d", s.RunningCount, s.DesiredCount)
		out += fmt.Sprintf("%s%s  %-40s %s\n", stageIndent, icon, s.Name, staleStyle.Render(counts))
	}
	return out
}

func renderStages(p state.PipelineState) string {
	if p.Err != nil && len(p.Stages) == 0 {
		return iconRed.Render(stageIndent+"⚠ "+p.Err.Error()) + "\n"
	}
	if len(p.Stages) == 0 {
		return staleStyle.Render(stageIndent+"no stage data") + "\n"
	}
	out := ""
	for _, stage := range p.Stages {
		timer := stageTimer(stage)
		if timer != "" {
			out += fmt.Sprintf("%s%s  %-22s %s\n", stageIndent, stageStatusIcon(string(stage.Status)), stage.Name, staleStyle.Render(timer))
		} else {
			out += fmt.Sprintf("%s%s  %s\n", stageIndent, stageStatusIcon(string(stage.Status)), stage.Name)
		}
	}
	return out
}

func stageTimer(s state.StageState) string {
	switch s.Status {
	case "InProgress":
		if s.StartedAt != nil {
			return formatDuration(time.Since(*s.StartedAt))
		}
	case "Succeeded", "Failed", "Stopped":
		if s.StartedAt != nil && s.EndedAt != nil {
			return formatDuration(s.EndedAt.Sub(*s.StartedAt))
		}
	}
	return ""
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < 0 {
		d = 0
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
