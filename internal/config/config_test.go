package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ericdahl-dev/aws-green/internal/config"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_minimal(t *testing.T) {
	path := writeConfig(t, `
[[pipelines]]
name = "my-pipeline"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Pipelines) != 1 {
		t.Fatalf("expected 1 pipeline, got %d", len(cfg.Pipelines))
	}
	if cfg.Settings.PollInterval != config.DefaultPollInterval {
		t.Errorf("expected default poll interval %d, got %d", config.DefaultPollInterval, cfg.Settings.PollInterval)
	}
}

func TestLoad_missingFile(t *testing.T) {
	_, err := config.Load("/nonexistent/path/config.toml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoad_noPipelines(t *testing.T) {
	path := writeConfig(t, ``)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for no pipelines")
	}
}

func TestLoad_invalidPollInterval(t *testing.T) {
	path := writeConfig(t, `
[settings]
poll_interval_seconds = -1

[[pipelines]]
name = "p"
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for poll_interval_seconds = -1")
	}
}

func TestLoad_unknownAccount(t *testing.T) {
	path := writeConfig(t, `
[[pipelines]]
account = "nonexistent"
name    = "p"
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for unknown account reference")
	}
}

func TestLoad_accountLookup(t *testing.T) {
	path := writeConfig(t, `
[[accounts]]
name    = "prod"
profile = "prod-profile"
region  = "us-east-1"

[[pipelines]]
account = "prod"
name    = "deploy"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	acct, ok := cfg.AccountFor(cfg.Pipelines[0])
	if !ok {
		t.Fatal("expected account to be found")
	}
	if acct.Profile != "prod-profile" {
		t.Errorf("expected profile prod-profile, got %s", acct.Profile)
	}
}
