package config

import (
	"fmt"
	"os"

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
	Account string `toml:"account"`
	Name    string `toml:"name"`
}

type Config struct {
	Settings  Settings   `toml:"settings"`
	Accounts  []Account  `toml:"accounts"`
	Pipelines []Pipeline `toml:"pipelines"`

	accountIndex map[string]Account
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

	if len(cfg.Pipelines) == 0 {
		return nil, fmt.Errorf("config must include at least one [[pipelines]] entry")
	}

	cfg.accountIndex = make(map[string]Account, len(cfg.Accounts))
	for _, a := range cfg.Accounts {
		cfg.accountIndex[a.Name] = a
	}

	for i, p := range cfg.Pipelines {
		if p.Account != "" {
			if _, ok := cfg.accountIndex[p.Account]; !ok {
				return nil, fmt.Errorf("pipelines[%d]: account %q not found in [[accounts]]", i, p.Account)
			}
		}
	}

	return &cfg, nil
}

func (c *Config) AccountFor(pipeline Pipeline) (Account, bool) {
	if pipeline.Account == "" {
		return Account{}, false
	}
	a, ok := c.accountIndex[pipeline.Account]
	return a, ok
}
