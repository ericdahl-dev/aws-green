package ui

import (
	"fmt"
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ericdahl-dev/aws-green/internal/aggregator"
	"github.com/ericdahl-dev/aws-green/internal/state"
)

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).MarginBottom(1)
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	normalStyle   = lipgloss.NewStyle()
	staleStyle    = lipgloss.NewStyle().Faint(true)
	hintStyle     = lipgloss.NewStyle().Faint(true)
	stageIndent   = "      "
)

const selectionTimeout = 10 * time.Second
const timerTickInterval = time.Second

type selectionExpiredMsg struct{}
type timerTickMsg struct{}

type Dashboard struct {
	snapshot      state.Snapshot
	cursor        int
	expanded      map[int]bool
	lastActivity  time.Time
	selectionFade bool
}

func NewDashboard(snap state.Snapshot) Dashboard {
	return Dashboard{
		snapshot:     snap,
		expanded:     make(map[int]bool),
		lastActivity: time.Now(),
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

func sortedPipelineOrder(pipelines []state.PipelineState) []int {
	order := make([]int, len(pipelines))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		pa := stoplightPriority(pipelines[order[a]].Stoplight)
		pb := stoplightPriority(pipelines[order[b]].Stoplight)
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

func (d Dashboard) Init() tea.Cmd {
	return tea.Batch(selectionTimeoutCmd(), timerTickCmd())
}

func (d Dashboard) Update(msg tea.Msg) (Dashboard, tea.Cmd) {
	count := len(d.snapshot.Pipelines)
	switch msg := msg.(type) {
	case tea.KeyMsg:
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
		}
		return d, selectionTimeoutCmd()
	case selectionExpiredMsg:
		if time.Since(d.lastActivity) >= selectionTimeout {
			d.selectionFade = true
		}
	case timerTickMsg:
		return d, timerTickCmd()
	case state.Snapshot:
		d.snapshot = msg
		if d.cursor >= len(msg.Pipelines) && len(msg.Pipelines) > 0 {
			d.cursor = len(msg.Pipelines) - 1
		}
	}
	return d, nil
}

func (d Dashboard) SelectedPipeline() *state.PipelineState {
	order := sortedPipelineOrder(d.snapshot.Pipelines)
	if d.cursor >= len(order) {
		return nil
	}
	p := d.snapshot.Pipelines[order[d.cursor]]
	return &p
}

func (d Dashboard) View() string {
	out := titleStyle.Render("aws-green") + "\n"

	if len(d.snapshot.Pipelines) == 0 {
		out += staleStyle.Render("  No pipelines configured.") + "\n"
	}

	order := sortedPipelineOrder(d.snapshot.Pipelines)
	for displayIdx, pipeIdx := range order {
		p := d.snapshot.Pipelines[pipeIdx]
		selected := displayIdx == d.cursor && !d.selectionFade
		expanded := d.expanded[displayIdx]

		triangle := "▶"
		if expanded {
			triangle = "▼"
		}
		line := pipelineRow(p)
		if selected {
			out += selectedStyle.Render(triangle+" "+line) + "\n"
		} else {
			out += normalStyle.Render("  "+line) + "\n"
		}

		if expanded {
			out += renderStages(p)
		}
	}

	out += "\n" + hintStyle.Render("↑/↓ navigate  enter/space expand  o open  r refresh  q quit  ? help")
	return out
}

func pipelineRow(p state.PipelineState) string {
	icon := p.Stoplight.String()
	name := p.FullName()
	row := fmt.Sprintf("%s  %-50s", icon, name)
	if p.IsStale() {
		age := time.Since(*p.StaleAt).Round(time.Second)
		row = staleStyle.Render(row + fmt.Sprintf("  ⚠ last seen %s ago", age))
	}
	return row
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
