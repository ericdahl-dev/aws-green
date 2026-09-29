package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/ericdahl-dev/aws-green/internal/config"
	"github.com/ericdahl-dev/aws-green/internal/discover"
)

// Discoverer finds a pipeline's stacks and ECS services in an AWS profile and
// region; see discover.Run.
type Discoverer func(ctx context.Context, profile, region, pipeline string) (discover.Result, error)

// discoveredMsg carries a finished discovery back to the manage screen.
type discoveredMsg struct {
	result discover.Result
	err    error
}

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
	manageModeDiscovering
	manageModeConfirmDiscovered
)

// Manage is a Bubble Tea component for CRUD management of projects.
type Manage struct {
	cfg     *config.Config
	cursor  int
	mode    manageMode
	form    *huh.Form
	editIdx int // -1 = add, >=0 = edit index
	err     string

	discover Discoverer
	// pending is the project from the form, held while its stacks and
	// services are discovered and confirmed.
	pending config.Project
	found   discover.Result

	// fields is a pointer because Manage is copied on every Update, and the
	// huh form binds to these values by address.
	fields *manageFields
}

type manageFields struct {
	name     string
	account  string
	pipeline string
	// stacks and services are the discovered resources the user keeps;
	// services are "cluster/service".
	stacks   []string
	services []string
}

// NewManage builds the manage screen. A nil discoverer skips looking up a new
// project's stacks and ECS services.
func NewManage(cfg *config.Config, discoverer Discoverer) Manage {
	return Manage{cfg: cfg, cursor: 0, editIdx: -1, fields: &manageFields{}, discover: discoverer}
}

func (m Manage) Init() tea.Cmd { return nil }

func (m Manage) Update(msg tea.Msg) (Manage, tea.Cmd) {
	switch m.mode {
	case manageModeForm:
		return m.updateForm(msg)
	case manageModeConfirmDelete:
		return m.updateConfirm(msg)
	case manageModeDiscovering:
		return m.updateDiscovering(msg)
	case manageModeConfirmDiscovered:
		return m.updateConfirmDiscovered(msg)
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
				*m.fields = manageFields{name: p.Name, account: p.Account, pipeline: p.Pipeline.Name}
				m.form = m.buildForm("Edit project")
				m.mode = manageModeForm
				return m, m.form.Init()
			}
		case "a":
			m.editIdx = -1
			*m.fields = manageFields{}
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
		name := strings.TrimSpace(m.fields.name)
		account := strings.TrimSpace(m.fields.account)
		pipeline := strings.TrimSpace(m.fields.pipeline)

		// An edit starts from the existing project, so what the form doesn't
		// show — stacks, ECS, whether it's enabled — survives it.
		var proj config.Project
		if m.editIdx >= 0 {
			proj = m.cfg.Projects[m.editIdx]
		}
		proj.Name = name
		proj.Account = account
		proj.Pipeline.Name = pipeline
		// A project with a pipeline but nothing else to watch is worth a
		// lookup; one whose stacks and services are already set is left alone.
		if m.discover != nil && proj.Pipeline.Name != "" && len(proj.Stacks) == 0 && len(proj.ECS) == 0 {
			m.pending = proj
			m.mode = manageModeDiscovering
			m.form = nil
			return m, m.discoverCmd(proj)
		}
		return m.save(proj)
	}

	if m.form.State == huh.StateAborted {
		m.mode = manageModeList
		m.form = nil
	}

	return m, cmd
}

// save adds or updates proj, returns to the list and announces the change.
func (m Manage) save(proj config.Project) (Manage, tea.Cmd) {
	var err error
	if m.editIdx >= 0 {
		err = m.cfg.UpdateProject(m.editIdx, proj)
	} else if err = m.cfg.AddProject(proj); err == nil {
		m.cursor = len(m.cfg.Projects) - 1
	}
	m.err = ""
	if err != nil {
		m.err = err.Error()
	}
	m.mode = manageModeList
	m.form = nil
	return m, configChangedCmd(m.cfg)
}

func (m Manage) discoverCmd(proj config.Project) tea.Cmd {
	var profile, region string
	if acct, ok := m.cfg.AccountFor(proj); ok {
		profile, region = acct.Profile, acct.Region
	}
	discoverer := m.discover
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		r, err := discoverer(ctx, profile, region, proj.Pipeline.Name)
		return discoveredMsg{result: r, err: err}
	}
}

func (m Manage) updateDiscovering(msg tea.Msg) (Manage, tea.Cmd) {
	done, ok := msg.(discoveredMsg)
	if !ok {
		return m, nil
	}
	m.err = ""
	switch {
	case done.err != nil:
		m, cmd := m.save(m.pending)
		m.err = "couldn't look up stacks and ECS services: " + done.err.Error()
		return m, cmd
	case len(done.result.Stacks) == 0 && len(done.result.ECS) == 0:
		m, cmd := m.save(m.pending)
		m.err = fmt.Sprintf("no stacks or ECS services share a %s tag with this pipeline — add them to the config by hand", discover.ProjectTag)
		return m, cmd
	}
	m.found = done.result
	likely := done.result.Likely(m.pending.Name)
	m.fields.stacks = likely.Stacks
	m.fields.services = serviceKeys(likely.ECS)
	m.form = m.buildDiscoveredForm()
	m.mode = manageModeConfirmDiscovered
	return m, m.form.Init()
}

func (m Manage) updateConfirmDiscovered(msg tea.Msg) (Manage, tea.Cmd) {
	proj := m.pending
	if msg, ok := msg.(tea.KeyMsg); ok && msg.String() == "esc" {
		return m.save(proj)
	}
	form, cmd := m.form.Update(msg)
	if f, ok := form.(*huh.Form); ok {
		m.form = f
	}
	switch m.form.State {
	case huh.StateCompleted:
		keep := map[string]bool{}
		for _, k := range m.fields.stacks {
			keep[k] = true
		}
		for _, s := range m.found.Stacks {
			if keep[s] {
				proj.Stacks = append(proj.Stacks, config.Stack{Name: s})
			}
		}
		keep = map[string]bool{}
		for _, k := range m.fields.services {
			keep[k] = true
		}
		for _, e := range m.found.ECS {
			var services []string
			for _, sv := range e.Services {
				if keep[e.Cluster+"/"+sv] {
					services = append(services, sv)
				}
			}
			if len(services) > 0 {
				proj.ECS = append(proj.ECS, config.ECSConfig{Cluster: e.Cluster, Services: services})
			}
		}
		return m.save(proj)
	case huh.StateAborted:
		return m.save(proj)
	}
	return m, cmd
}

// serviceKeys flattens ECS config into "cluster/service" keys.
func serviceKeys(ecs []config.ECSConfig) []string {
	var keys []string
	for _, e := range ecs {
		for _, sv := range e.Services {
			keys = append(keys, e.Cluster+"/"+sv)
		}
	}
	return keys
}

// buildDiscoveredForm lists what discovery found, with the likely picks
// selected, for the user to confirm before any of it is saved.
func (m Manage) buildDiscoveredForm() *huh.Form {
	var fields []huh.Field
	if len(m.found.Stacks) > 0 {
		opts := make([]huh.Option[string], len(m.found.Stacks))
		for i, s := range m.found.Stacks {
			opts[i] = huh.NewOption(s, s)
		}
		fields = append(fields, huh.NewMultiSelect[string]().
			Title("CloudFormation stacks").
			Description("Found by the pipeline's "+discover.ProjectTag+" tag. space toggles, enter confirms.").
			Options(opts...).
			Value(&m.fields.stacks))
	}
	if keys := serviceKeys(m.found.ECS); len(keys) > 0 {
		opts := make([]huh.Option[string], len(keys))
		for i, k := range keys {
			cluster, service, _ := strings.Cut(k, "/")
			opts[i] = huh.NewOption(service+"  ("+cluster+")", k)
		}
		fields = append(fields, huh.NewMultiSelect[string]().
			Title("ECS services").
			Description("From those stacks' resources. space toggles, enter confirms.").
			Options(opts...).
			Value(&m.fields.services))
	}
	return huh.NewForm(huh.NewGroup(fields...).Title("Watch these for " + m.pending.Name + "?"))
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
				Value(&m.fields.name).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return errors.New("project name is required")
					}
					return nil
				}),
			huh.NewInput().
				Title("Account").
				Description(accountHint).
				Value(&m.fields.account).
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
				Value(&m.fields.pipeline),
		).Title(title),
	)
}

func (m Manage) View() string {
	switch m.mode {
	case manageModeForm:
		if m.form != nil {
			return m.form.View()
		}
	case manageModeDiscovering:
		return fmt.Sprintf("\n  Looking for stacks and ECS services that belong with %s…\n", m.pending.Pipeline.Name)
	case manageModeConfirmDiscovered:
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
