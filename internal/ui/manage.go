package ui

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/ericdahl-dev/aws-green/internal/config"
)

// BackMsg is sent when the user exits the manage screen.
type BackMsg struct{}

// ConfigChangedMsg is sent when the config has been mutated (add/edit/delete).
type ConfigChangedMsg struct {
	Config *config.Config
}

type manageMode int

const (
	manageModeList manageMode = iota
	manageModeForm
	manageModeConfirmDelete
)

// Manage is a Bubble Tea component for CRUD management of projects.
type Manage struct {
	cfg     *config.Config
	cursor  int
	mode    manageMode
	form    *huh.Form
	editIdx int // -1 = add, >=0 = edit index
	err     string

	fName     string
	fAccount  string
	fPipeline string
}

func NewManage(cfg *config.Config) Manage {
	return Manage{cfg: cfg, cursor: 0, editIdx: -1}
}

func (m Manage) Init() tea.Cmd { return nil }

func (m Manage) Update(msg tea.Msg) (Manage, tea.Cmd) {
	switch m.mode {
	case manageModeForm:
		return m.updateForm(msg)
	case manageModeConfirmDelete:
		return m.updateConfirm(msg)
	default:
		return m.updateList(msg)
	}
}

func (m Manage) updateList(msg tea.Msg) (Manage, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		projects := m.cfg.Projects
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(projects)-1 {
				m.cursor++
			}
		case "e":
			if len(projects) > 0 {
				p := projects[m.cursor]
				m.editIdx = m.cursor
				m.fName = p.Name
				m.fAccount = p.Account
				m.fPipeline = p.Pipeline.Name
				m.form = m.buildForm("Edit project")
				m.mode = manageModeForm
				return m, m.form.Init()
			}
		case "a":
			m.editIdx = -1
			m.fName = ""
			m.fAccount = ""
			m.fPipeline = ""
			m.form = m.buildForm("Add project")
			m.mode = manageModeForm
			return m, m.form.Init()
		case "d":
			if len(projects) > 0 {
				m.mode = manageModeConfirmDelete
			}
		case "t", " ":
			if len(projects) > 0 {
				if err := m.cfg.ToggleProject(m.cursor); err != nil {
					m.err = err.Error()
				} else {
					m.err = ""
					return m, configChangedCmd(m.cfg)
				}
			}
		case "esc":
			return m, func() tea.Msg { return BackMsg{} }
		}
	}
	return m, nil
}

func (m Manage) updateForm(msg tea.Msg) (Manage, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok && msg.String() == "esc" {
		m.mode = manageModeList
		m.form = nil
		return m, nil
	}

	form, cmd := m.form.Update(msg)
	if f, ok := form.(*huh.Form); ok {
		m.form = f
	}

	if m.form.State == huh.StateCompleted {
		name := strings.TrimSpace(m.fName)
		account := strings.TrimSpace(m.fAccount)
		pipeline := strings.TrimSpace(m.fPipeline)

		proj := config.Project{
			Name:     name,
			Account:  account,
			Pipeline: config.Pipeline{Name: pipeline},
		}
		if m.editIdx >= 0 {
			// preserve existing stacks/ecs config
			existing := m.cfg.Projects[m.editIdx]
			proj.Stacks = existing.Stacks
			proj.ECS = existing.ECS
			if err := m.cfg.UpdateProject(m.editIdx, proj); err != nil {
				m.err = err.Error()
			} else {
				m.err = ""
			}
		} else {
			if err := m.cfg.AddProject(proj); err != nil {
				m.err = err.Error()
			} else {
				m.err = ""
				m.cursor = len(m.cfg.Projects) - 1
			}
		}
		m.mode = manageModeList
		m.form = nil
		return m, configChangedCmd(m.cfg)
	}

	if m.form.State == huh.StateAborted {
		m.mode = manageModeList
		m.form = nil
	}

	return m, cmd
}

func (m Manage) updateConfirm(msg tea.Msg) (Manage, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "y", "Y":
			if err := m.cfg.RemoveProject(m.cursor); err != nil {
				m.err = err.Error()
			} else {
				m.err = ""
				if m.cursor >= len(m.cfg.Projects) && m.cursor > 0 {
					m.cursor--
				}
			}
			m.mode = manageModeList
			return m, configChangedCmd(m.cfg)
		default:
			m.mode = manageModeList
		}
	}
	return m, nil
}

func (m Manage) buildForm(title string) *huh.Form {
	accountHint := "AWS account name from config (or blank for default)"
	if len(m.cfg.Accounts) > 0 {
		names := make([]string, len(m.cfg.Accounts))
		for i, a := range m.cfg.Accounts {
			names[i] = a.Name
		}
		accountHint = "One of: " + strings.Join(names, ", ") + " (or blank for default)"
	}
	return huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Project name").
				Description("Display name for this project (e.g. my-app).").
				Value(&m.fName).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return errors.New("project name is required")
					}
					return nil
				}),
			huh.NewInput().
				Title("Account").
				Description(accountHint).
				Value(&m.fAccount).
				Validate(func(s string) error {
					s = strings.TrimSpace(s)
					if s == "" {
						return nil
					}
					for _, a := range m.cfg.Accounts {
						if a.Name == s {
							return nil
						}
					}
					if len(m.cfg.Accounts) > 0 {
						return fmt.Errorf("unknown account %q — must match a configured account name", s)
					}
					return nil
				}),
			huh.NewInput().
				Title("CodePipeline name").
				Description("The exact name of the pipeline in AWS CodePipeline (or blank to omit).").
				Value(&m.fPipeline),
		).Title(title),
	)
}

func (m Manage) View() string {
	switch m.mode {
	case manageModeForm:
		if m.form != nil {
			return m.form.View()
		}
	case manageModeConfirmDelete:
		if m.cursor < len(m.cfg.Projects) {
			p := m.cfg.Projects[m.cursor]
			return fmt.Sprintf(
				"\n  Delete project %q? This cannot be undone.\n\n  Press y to confirm, any other key to cancel.\n",
				p.Name,
			)
		}
	}
	return m.listView()
}

func (m Manage) listView() string {
	out := ""
	projects := m.cfg.Projects

	if len(projects) == 0 {
		out += staleStyle.Render("  No projects configured.") + "\n\n"
	}

	for i, p := range projects {
		account := p.Account
		if account == "" {
			account = "(default)"
		}
		pipeline := p.Pipeline.Name
		if pipeline == "" {
			pipeline = "(none)"
		}
		toggle := "✓"
		style := normalStyle
		if !p.IsEnabled() {
			toggle = "✗"
			style = staleStyle
		}
		line := fmt.Sprintf(" %s  %-30s  %-20s  %s", toggle, p.Name, account, pipeline)
		if i == m.cursor {
			out += selectedStyle.Render("▶"+line) + "\n"
		} else {
			out += style.Render(" "+line) + "\n"
		}
	}

	if m.err != "" {
		out += "\n" + errorStyle.Render("  ⚠ "+m.err) + "\n"
	}

	out += "\n" + hintStyle.Render("↑/↓ navigate  a add  e edit  d delete  t toggle  esc back")
	return out
}

func configChangedCmd(cfg *config.Config) tea.Cmd {
	return func() tea.Msg {
		return ConfigChangedMsg{Config: cfg}
	}
}
