package ui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/ericdahl-dev/aws-green/internal/health"
)

var (
	iconGreen  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	iconRed    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	iconYellow = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	iconFaint  = lipgloss.NewStyle().Faint(true)
	// Magenta, so a gate waiting on a person doesn't read as "still running".
	iconApproval = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
)

func stageStatusIcon(status health.ExecutionStatus) string {
	switch status {
	case health.StatusSucceeded:
		return iconGreen.Render("✓")
	case health.StatusFailed, health.StatusStopped:
		return iconRed.Render("✗")
	case health.StatusInProgress:
		return iconYellow.Render("●")
	default:
		return iconFaint.Render("○")
	}
}
