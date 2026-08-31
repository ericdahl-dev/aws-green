package ui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ericdahl-dev/aws-green/internal/aggregator"
	"github.com/ericdahl-dev/aws-green/internal/fix"
	"github.com/ericdahl-dev/aws-green/internal/state"
)

var (
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

// ActionerFactory builds a fix.Actioner for the given AWS profile and region.
type ActionerFactory func(profile, region string) (fix.Actioner, error)

// navItemKind distinguishes navigable rows in the dashboard list.
type navItemKind int

const (
	navProject navItemKind = iota
	navStage
)

// navItem identifies a single navigable row.
type navItem struct {
	kind     navItemKind
	projKey  string // state.ProjectState.Key(), not Name — names repeat across accounts
	stageIdx int    // only meaningful for navStage
}

type Dashboard struct {
	snapshot        state.Snapshot
	cursor          int
	expanded        map[string]bool
	stagesExpanded  map[string]bool // key: "projKey/stageName"
	lastActivity    time.Time
	selectionFade   bool

	actionerFactory ActionerFactory
	fixCtx          context.Context

	fixStatus    fixState
	fixPlan      *fix.FixPlan
	fixResultMsg string
	fixErr       bool
}

func NewDashboard(snap state.Snapshot, actionerFactory ActionerFactory, ctx context.Context) Dashboard {
	return Dashboard{
		snapshot:        snap,
		expanded:        make(map[string]bool),
		stagesExpanded:  make(map[string]bool),
		lastActivity:    time.Now(),
		actionerFactory: actionerFactory,
		fixCtx:          ctx,
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

// buildNavList returns the flat list of navigable rows given current expansion state.
func (d Dashboard) buildNavList() []navItem {
	var items []navItem
	order := sortedProjectOrder(d.snapshot.Projects)
	for _, projIdx := range order {
		proj := d.snapshot.Projects[projIdx]
		items = append(items, navItem{kind: navProject, projKey: proj.Key()})
		if d.expanded[proj.Key()] {
			for i := range proj.Pipeline.Stages {
				items = append(items, navItem{kind: navStage, projKey: proj.Key(), stageIdx: i})
			}
		}
	}
	return items
}

// currentNavItem returns the navItem at the cursor, or nil.
func (d Dashboard) currentNavItem() *navItem {
	items := d.buildNavList()
	if d.cursor >= len(items) {
		return nil
	}
	item := items[d.cursor]
	return &item
}

func (d Dashboard) selectedProject() *state.ProjectState {
	item := d.currentNavItem()
	if item == nil {
		return nil
	}
	for i := range d.snapshot.Projects {
		if d.snapshot.Projects[i].Key() == item.projKey {
			p := d.snapshot.Projects[i]
			return &p
		}
	}
	return nil
}

func (d Dashboard) Update(msg tea.Msg) (Dashboard, tea.Cmd) {
	count := len(d.buildNavList())

	switch msg := msg.(type) {
	case tea.KeyMsg:
		// While confirming, only enter/esc are meaningful.
		if d.fixStatus == fixConfirming {
			switch msg.String() {
			case "enter":
				d.fixStatus = fixExecuting
				plan := d.fixPlan
				factory := d.actionerFactory
				ctx := d.fixCtx
				return d, func() tea.Msg {
					actioner, err := factory(plan.Profile, plan.Region)
					if err != nil {
						return fixDoneMsg{err: fmt.Errorf("build actioner: %w", err)}
					}
					err = fix.Execute(ctx, plan, actioner)
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
			if item := d.currentNavItem(); item != nil {
				switch item.kind {
				case navProject:
					d.expanded[item.projKey] = !d.expanded[item.projKey]
					// clamp cursor in case stages disappeared
					newCount := len(d.buildNavList())
					if d.cursor >= newCount {
						d.cursor = newCount - 1
					}
				case navStage:
					proj := d.selectedProject()
					if proj != nil && item.stageIdx < len(proj.Pipeline.Stages) {
						key := proj.Key() + "/" + proj.Pipeline.Stages[item.stageIdx].Name
						d.stagesExpanded[key] = !d.stagesExpanded[key]
					}
				}
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
		newCount := len(d.buildNavList())
		if d.cursor >= newCount && newCount > 0 {
			d.cursor = newCount - 1
		}
	}
	return d, nil
}

func (d Dashboard) SelectedPipeline() *state.PipelineState {
	if proj := d.selectedProject(); proj != nil {
		p := proj.Pipeline
		return &p
	}
	return nil
}

// BodyView renders the dashboard without the app title (the root model prepends title and spinner).
func (d Dashboard) BodyView() string {
	out := ""

	if len(d.snapshot.Projects) == 0 {
		out += staleStyle.Render("  No projects configured.") + "\n"
	}

	navList := d.buildNavList()
	navCursor := d.cursor

	order := sortedProjectOrder(d.snapshot.Projects)
	for _, projIdx := range order {
		proj := d.snapshot.Projects[projIdx]
		expanded := d.expanded[proj.Key()]

		// find this project's nav index
		projNavIdx := -1
		for ni, item := range navList {
			if item.kind == navProject && item.projKey == proj.Key() {
				projNavIdx = ni
				break
			}
		}
		selected := projNavIdx == navCursor && !d.selectionFade

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
			out += d.renderPipelineSection(proj, navList, navCursor)
			out += renderStacksSection(proj)
			out += renderECSSection(proj)
		}
	}

	out += "\n" + d.hintLine()
	return out
}

func (d Dashboard) View() string {
	return TitleLine(false, "") + d.BodyView()
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
		return hintStyle.Render("↑/↓ navigate  enter/space expand  f fix  o open  r refresh  m manage  q quit  ? help")
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
	if len(proj.Stacks) > 0 || proj.StacksFetch.IsStale() {
		worst := aggregator.StoplightGrey
		var alertLabel string
		var alertStyle lipgloss.Style
		for _, s := range proj.Stacks {
			if s.Stoplight > worst {
				worst = s.Stoplight
			}
			if alertLabel == "" {
				if strings.HasSuffix(s.Status, "_FAILED") {
					alertLabel = stackRollbackShortLabel(s.Status)
					alertStyle = errorStyle
				} else if s.Status == "UPDATE_ROLLBACK_IN_PROGRESS" || s.Status == "ROLLBACK_IN_PROGRESS" {
					alertLabel = "⚠ rolling back"
					alertStyle = confirmStyle
				}
			}
		}
		stackSummary := "  Stacks " + worst.String()
		if alertLabel != "" {
			stackSummary += " " + alertStyle.Render(alertLabel)
		}
		if proj.StacksFetch.IsStale() {
			stackSummary += " " + staleStyle.Render("⚠ stale")
		}
		summary += stackSummary
	}
	if len(proj.ECSServices) > 0 || proj.ECSFetch.IsStale() {
		worst := aggregator.StoplightGrey
		for _, s := range proj.ECSServices {
			if s.Stoplight > worst {
				worst = s.Stoplight
			}
		}
		summary += "  ECS " + worst.String()
		if proj.ECSFetch.IsStale() {
			summary += " " + staleStyle.Render("⚠ stale")
		}
	}
	row += hintStyle.Render(summary)

	if proj.Pipeline.IsStale() {
		age := time.Since(*proj.Pipeline.StaleAt).Round(time.Second)
		row += staleStyle.Render(fmt.Sprintf("  ⚠ last seen %s ago", age))
	}
	// One hint per row, whichever of the three fetches hit the bad credential.
	if isAuthError(proj.Pipeline.Err) || isAuthError(proj.StacksFetch.Err) || isAuthError(proj.ECSFetch.Err) {
		row += errorStyle.Render("  (auth error — expand for login hint)")
	}
	return row
}

func (d Dashboard) renderPipelineSection(proj state.ProjectState, navList []navItem, navCursor int) string {
	p := proj.Pipeline
	out := ""
	if p.Name != "" {
		out += normalStyle.Render("      pipeline  "+p.Name) + "\n"
	}
	out += d.renderStages(proj, navList, navCursor)
	return out
}

func renderStacksSection(proj state.ProjectState) string {
	stacks := proj.Stacks
	if len(stacks) == 0 && !proj.StacksFetch.IsStale() {
		return ""
	}
	out := normalStyle.Render("      stacks") + "\n"
	if proj.StacksFetch.Err != nil {
		out += renderFetchError(proj.StacksFetch.Err, proj.Account)
	}
	for _, s := range stacks {
		icon := s.Stoplight.String()
		timer := ""
		if s.StartedAt != nil && isInProgressStatus(s.Status) {
			timer = " " + staleStyle.Render(formatDuration(time.Since(*s.StartedAt)))
		}
		label, style := stackStatusLabel(s.Status)
		out += fmt.Sprintf("%s%s  %-40s %s%s\n", stageIndent, icon, s.Name, style.Render(label), timer)
	}
	return out
}

func stackStatusLabel(status string) (string, lipgloss.Style) {
	switch status {
	case "CREATE_COMPLETE", "UPDATE_COMPLETE", "ROLLBACK_COMPLETE":
		return "✓ complete", successStyle
	case "UPDATE_IN_PROGRESS":
		return "↻ updating", confirmStyle
	case "CREATE_IN_PROGRESS":
		return "↻ creating", confirmStyle
	case "DELETE_IN_PROGRESS":
		return "↻ deleting", confirmStyle
	case "UPDATE_ROLLBACK_IN_PROGRESS", "ROLLBACK_IN_PROGRESS":
		return "⚠ rolling back", confirmStyle
	case "UPDATE_ROLLBACK_FAILED":
		return "✗ rollback failed", errorStyle
	case "UPDATE_ROLLBACK_COMPLETE_CLEANUP_IN_PROGRESS":
		return "↻ rollback cleanup", confirmStyle
	case "UPDATE_ROLLBACK_COMPLETE":
		return "⚠ rolled back", confirmStyle
	case "CREATE_FAILED", "DELETE_FAILED", "ROLLBACK_FAILED":
		return "✗ " + strings.ToLower(strings.ReplaceAll(status, "_", " ")), errorStyle
	case "DELETE_COMPLETE":
		return "deleted", staleStyle
	case "REVIEW_IN_PROGRESS":
		return "reviewing", staleStyle
	default:
		if strings.HasSuffix(status, "_FAILED") {
			return "✗ " + strings.ToLower(strings.ReplaceAll(status, "_", " ")), errorStyle
		}
		if strings.HasSuffix(status, "_IN_PROGRESS") {
			return "↻ " + strings.ToLower(strings.ReplaceAll(status, "_", " ")), confirmStyle
		}
		return strings.ToLower(strings.ReplaceAll(status, "_", " ")), staleStyle
	}
}

func stackRollbackShortLabel(status string) string {
	switch status {
	case "UPDATE_ROLLBACK_FAILED":
		return "✗ rollback failed"
	default:
		return "✗ " + strings.ToLower(strings.ReplaceAll(status, "_", " "))
	}
}

func isInProgressStatus(status string) bool {
	return status == "CREATE_IN_PROGRESS" || status == "UPDATE_IN_PROGRESS" ||
		status == "UPDATE_ROLLBACK_IN_PROGRESS" || status == "DELETE_IN_PROGRESS" ||
		status == "ROLLBACK_IN_PROGRESS"
}

func renderECSSection(proj state.ProjectState) string {
	services := proj.ECSServices
	if len(services) == 0 && !proj.ECSFetch.IsStale() {
		return ""
	}
	out := normalStyle.Render("      ecs") + "\n"
	if proj.ECSFetch.Err != nil {
		out += renderFetchError(proj.ECSFetch.Err, proj.Account)
	}
	for _, s := range services {
		icon := s.Stoplight.String()
		counts := fmt.Sprintf("%d/%d", s.RunningCount, s.DesiredCount)
		detail := staleStyle.Render(counts)
		if s.PendingCount > 0 {
			detail += " " + confirmStyle.Render(fmt.Sprintf("%d pending", s.PendingCount))
		}
		if s.FailingTaskCount > 0 {
			detail += " " + errorStyle.Render(fmt.Sprintf("%d failing", s.FailingTaskCount))
			if s.StoppedReason != "" {
				detail += " " + errorStyle.Render("("+truncate(s.StoppedReason, 60)+")")
			}
		}
		out += fmt.Sprintf("%s%s  %-40s %s\n", stageIndent, icon, s.Name, detail)
	}
	return out
}

// renderFetchError renders a failed fetch: the error itself, plus the login
// hint when it looks like a credential problem rather than a service one.
func renderFetchError(err error, account string) string {
	out := iconRed.Render(stageIndent+"⚠ "+err.Error()) + "\n"
	if isAuthError(err) {
		loginCmd := "aws sso login"
		if account != "" {
			loginCmd = "aws sso login --profile " + account
		}
		out += hintStyle.Render(stageIndent+"  run: "+loginCmd) + "\n"
	}
	return out
}

// isAuthError reports whether err looks like an AWS authentication or
// credential failure (expired SSO token, missing credentials, etc.).
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"expired", "sso", "token", "credentials", "no credentials",
		"notauthorized", "invalidclienttokenid", "authfailure",
		"accessdenied", "invalidtoken",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	var apiErr interface{ ErrorCode() string }
	if errors.As(err, &apiErr) {
		code := strings.ToLower(apiErr.ErrorCode())
		for _, needle := range []string{"expired", "token", "auth", "credentials", "denied"} {
			if strings.Contains(code, needle) {
				return true
			}
		}
	}
	return false
}

func (d Dashboard) renderStages(proj state.ProjectState, navList []navItem, navCursor int) string {
	p := proj.Pipeline
	if p.Err != nil && len(p.Stages) == 0 {
		return renderFetchError(p.Err, p.Account)
	}
	if len(p.Stages) == 0 {
		return staleStyle.Render(stageIndent+"no stage data") + "\n"
	}
	out := ""
	for i, stage := range p.Stages {
		stageNavIdx := -1
		for ni, item := range navList {
			if item.kind == navStage && item.projKey == proj.Key() && item.stageIdx == i {
				stageNavIdx = ni
				break
			}
		}
		stageSelected := stageNavIdx == navCursor && !d.selectionFade
		key := proj.Key() + "/" + stage.Name
		stageExp := d.stagesExpanded[key]

		triangle := ""
		if len(stage.Actions) > 0 {
			if stageExp {
				triangle = "▼ "
			} else {
				triangle = "▶ "
			}
		} else {
			triangle = "  "
		}

		timer := stageTimer(stage)
		var stageLine string
		if timer != "" {
			stageLine = fmt.Sprintf("%s%s%s  %-20s %s", stageIndent, triangle, stageStatusIcon(string(stage.Status)), stage.Name, staleStyle.Render(timer))
		} else {
			stageLine = fmt.Sprintf("%s%s%s  %s", stageIndent, triangle, stageStatusIcon(string(stage.Status)), stage.Name)
		}
		if stageSelected {
			out += selectedStyle.Render(stageLine) + "\n"
		} else {
			out += stageLine + "\n"
		}

		if stageExp {
			for _, action := range stage.Actions {
				out += fmt.Sprintf("%s      %s  %s\n", stageIndent, stageStatusIcon(string(action.Status)), hintStyle.Render(action.Name))
			}
		}
	}
	return out
}

func stageTimer(s state.StageState) string {
	switch s.Status {
	case aggregator.StatusInProgress:
		if s.StartedAt != nil {
			return formatDuration(time.Since(*s.StartedAt))
		}
	case aggregator.StatusSucceeded, aggregator.StatusFailed, aggregator.StatusStopped:
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

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
