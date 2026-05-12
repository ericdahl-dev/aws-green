package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	awsclient "github.com/ericdahl-dev/aws-green/internal/aws"
	"github.com/ericdahl-dev/aws-green/internal/config"
	"github.com/ericdahl-dev/aws-green/internal/poller"
	"github.com/ericdahl-dev/aws-green/internal/state"
	"github.com/ericdahl-dev/aws-green/internal/ui"
)

type model struct {
	dashboard   ui.Dashboard
	showHelp    bool
	pollCh      <-chan state.Snapshot
	pollCancel  context.CancelFunc
	pollCtx     context.Context
	poller      *poller.Poller
	pollChWrite chan state.Snapshot
}

func waitForSnapshot(ch <-chan state.Snapshot) tea.Cmd {
	return func() tea.Msg {
		snap, ok := <-ch
		if !ok {
			return nil
		}
		return snap
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(waitForSnapshot(m.pollCh), m.dashboard.Init())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.pollCancel()
			return m, tea.Quit
		case "?":
			m.showHelp = !m.showHelp
			return m, nil
		case "esc":
			m.showHelp = false
			return m, nil
		case "r":
			m.poller.ForceRefresh(m.pollCtx, m.pollChWrite)
			return m, nil
		case "o":
			m.openInConsole()
			return m, nil
		}

	case state.Snapshot:
		cmds = append(cmds, waitForSnapshot(m.pollCh))
		var dashCmd tea.Cmd
		m.dashboard, dashCmd = m.dashboard.Update(msg)
		cmds = append(cmds, dashCmd)
		return m, tea.Batch(cmds...)
	}

	var cmd tea.Cmd
	m.dashboard, cmd = m.dashboard.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

func (m model) View() string {
	if m.showHelp {
		return ui.Help{}.View()
	}
	return m.dashboard.View()
}

func (m *model) openInConsole() {
	p := m.dashboard.SelectedPipeline()
	if p == nil {
		return
	}
	// Construct console URL from account region if available.
	_ = exec.Command("open", fmt.Sprintf("https://console.aws.amazon.com/codesuite/codepipeline/pipelines/%s/view", p.Name)).Start()
}

func configPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.toml"
	}
	return filepath.Join(home, ".config", "aws-green", "config.toml")
}

func main() {
	cfg, err := config.Load(configPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "aws-green: %v\n", err)
		os.Exit(1)
	}

	factory := func(profile, region string) (poller.Fetcher, error) {
		return awsclient.New(profile, region)
	}

	p := poller.New(cfg, factory)
	ctx, cancel := context.WithCancel(context.Background())

	writeCh := make(chan state.Snapshot, 4)
	readCh, stopPoller := p.Start(ctx)

	go func() {
		for snap := range readCh {
			writeCh <- snap
		}
		close(writeCh)
	}()

	m := model{
		dashboard:   ui.NewDashboard(p.Snapshot()),
		pollCh:      writeCh,
		pollCancel:  func() { cancel(); stopPoller() },
		pollCtx:     ctx,
		poller:      p,
		pollChWrite: writeCh,
	}

	prog := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := prog.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
