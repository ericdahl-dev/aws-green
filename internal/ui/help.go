package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

var helpStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	Padding(1, 2).
	BorderForeground(lipgloss.Color("212"))

const helpMarkdown = `# aws-green · keybindings

| Key | Action |
|-----|--------|
| **↑** / **k** | Move up |
| **↓** / **j** | Move down |
| **enter** / **space** | Expand or collapse project |
| **f** | Smart fix (restart / force deploy / rollback) |
| **o** | Open pipeline in AWS Console |
| **r** | Force refresh |
| **m** | Manage projects (add / edit / delete / enable) |
| **?** | Toggle this help |
| **esc** | Close help |
| **q** / **ctrl+c** | Quit |
`

// RenderHelp renders markdown help for the given terminal width.
func RenderHelp(width int) string {
	if width < 30 {
		width = 80
	}
	innerW := width - 4
	if innerW < 40 {
		innerW = 72
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithWordWrap(innerW),
		glamour.WithStandardStyle("dark"),
	)
	if err != nil {
		return helpStyle.Render(fallbackHelpText())
	}
	out, err := r.Render(helpMarkdown)
	if err != nil {
		return helpStyle.Render(fallbackHelpText())
	}
	return helpStyle.Render(strings.TrimRight(out, "\n"))
}

func fallbackHelpText() string {
	return `aws-green keybindings

  ↑ / k          move up
  ↓ / j          move down
  enter / space  expand/collapse project
  f              smart fix (restart / force deploy / rollback)
  o              open pipeline in AWS Console
  r              force refresh
  m              manage projects (add / edit / delete / enable)
  ?              toggle this help
  esc            close help
  q / ctrl+c     quit`
}

type Help struct{}

// View renders help using a default width (TTY width is passed via RenderHelp from the root model).
func (h Help) View() string {
	return RenderHelp(80)
}

type ToggleHelpMsg struct{}

func ToggleHelpCmd() tea.Cmd {
	return func() tea.Msg { return ToggleHelpMsg{} }
}
