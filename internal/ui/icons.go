package ui

import "github.com/charmbracelet/lipgloss"

var (
	iconGreen  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	iconRed    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	iconYellow = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	iconFaint  = lipgloss.NewStyle().Faint(true)
)

func stageStatusIcon(status string) string {
	switch status {
	case "Succeeded":
		return iconGreen.Render("✓")
	case "Failed", "Stopped":
		return iconRed.Render("✗")
	case "InProgress":
		return iconYellow.Render("●")
	default:
		return iconFaint.Render("○")
	}
}
