package ui

import "github.com/charmbracelet/lipgloss"

var helpStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	Padding(1, 3).
	BorderForeground(lipgloss.Color("212"))

type Help struct{}

func (h Help) View() string {
	return helpStyle.Render(`aws-green keybindings

  ↑ / k         move up
  ↓ / j         move down
  enter / space  expand/collapse pipeline
  f              smart fix (restart / force deploy / rollback)
  o              open pipeline in AWS Console
  r              force refresh
  ?              toggle this help
  esc            close help
  q / ctrl+c     quit`)
}
