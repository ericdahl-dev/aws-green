package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)


const DefaultPollInterval = 30

type Settings struct {
	PollInterval int `toml:"poll_interval_seconds"`
}

type Account struct {
	Name    string `toml:"name"`
	Profile string `toml:"profile"`
	Region  string `toml:"region"`
}

type Pipeline struct {
	Name string `toml:"name"`
}

type Stack struct {
	Name string `toml:"name"`
}

type ECSConfig struct {
	Cluster  string   `toml:"cluster"`
	Services []string `toml:"services"`
}

type Project struct {
	Name     string      `toml:"name"`
	Account  string      `toml:"account"`
	Pipeline Pipeline    `toml:"pipeline"`
	Stacks   []Stack     `toml:"stacks"`
	ECS      []ECSConfig `toml:"ecs"`
	Enabled  *bool       `toml:"enabled"`
}

// IsEnabled returns true unless explicitly set to false, so configs written
// before this field existed keep polling every project.
func (p Project) IsEnabled() bool {
	return p.Enabled == nil || *p.Enabled
}

type Config struct {
	Settings Settings  `toml:"settings"`
	Accounts []Account `toml:"accounts"`
	Projects []Project `toml:"projects"`

	accountIndex map[string]Account
	path         string
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config file not found at %s — create one to get started", path)
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if cfg.Settings.PollInterval == 0 {
		cfg.Settings.PollInterval = DefaultPollInterval
	}
	if cfg.Settings.PollInterval < 1 {
		return nil, fmt.Errorf("poll_interval_seconds must be at least 1 second")
	}

	cfg.path = path
	cfg.accountIndex = make(map[string]Account, len(cfg.Accounts))
	for _, a := range cfg.Accounts {
		cfg.accountIndex[a.Name] = a
	}

	for i, p := range cfg.Projects {
		if p.Account != "" {
			if _, ok := cfg.accountIndex[p.Account]; !ok {
				return nil, fmt.Errorf("projects[%d]: account %q not found in [[accounts]]", i, p.Account)
			}
		}
	}

	return &cfg, nil
}

// Path returns the file path this config was loaded from.
func (c *Config) Path() string { return c.path }

// EnabledProjects returns only projects that are enabled.
func (c *Config) EnabledProjects() []Project {
	var out []Project
	for _, p := range c.Projects {
		if p.IsEnabled() {
			out = append(out, p)
		}
	}
	return out
}

// Save writes the config back to the file it was loaded from.
func (c *Config) Save() error {
	if c.path == "" {
		return fmt.Errorf("config has no path set")
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.WriteFile(c.path, buf.Bytes(), 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	c.accountIndex = make(map[string]Account, len(c.Accounts))
	for _, a := range c.Accounts {
		c.accountIndex[a.Name] = a
	}
	return nil
}

// AddProject appends a new project and saves.
func (c *Config) AddProject(p Project) error {
	c.Projects = append(c.Projects, p)
	return c.Save()
}

// UpdateProject replaces the project at index i and saves.
func (c *Config) UpdateProject(i int, p Project) error {
	if i < 0 || i >= len(c.Projects) {
		return fmt.Errorf("project index %d out of range", i)
	}
	c.Projects[i] = p
	return c.Save()
}

// RemoveProject removes the project at index i and saves.
func (c *Config) RemoveProject(i int) error {
	if i < 0 || i >= len(c.Projects) {
		return fmt.Errorf("project index %d out of range", i)
	}
	c.Projects = append(c.Projects[:i], c.Projects[i+1:]...)
	return c.Save()
}

// ToggleProject flips the enabled state of project i and saves.
func (c *Config) ToggleProject(i int) error {
	if i < 0 || i >= len(c.Projects) {
		return fmt.Errorf("project index %d out of range", i)
	}
	enabled := !c.Projects[i].IsEnabled()
	c.Projects[i].Enabled = &enabled
	return c.Save()
}

func (c *Config) AccountFor(project Project) (Account, bool) {
	if project.Account == "" {
		return Account{}, false
	}
	a, ok := c.accountIndex[project.Account]
	return a, ok
}

type starterConfig struct {
	Settings Settings  `toml:"settings"`
	Accounts []Account `toml:"accounts"`
	Projects []Project `toml:"projects"`
}

// WriteStarter writes a minimal valid config with one [[projects]] entry.
func WriteStarter(path, accountName, profile, region, projectName, pipeline string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	sc := starterConfig{
		Settings: Settings{
			PollInterval: DefaultPollInterval,
		},
		Accounts: []Account{{
			Name:    strings.TrimSpace(accountName),
			Profile: strings.TrimSpace(profile),
			Region:  strings.TrimSpace(region),
		}},
		Projects: []Project{{
			Name:     strings.TrimSpace(projectName),
			Account:  strings.TrimSpace(accountName),
			Pipeline: Pipeline{Name: strings.TrimSpace(pipeline)},
		}},
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(sc); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
