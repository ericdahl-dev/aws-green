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
[[projects]]
name    = "my-project"
account = ""

  [projects.pipeline]
  name = "my-pipeline"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(cfg.Projects))
	}
	if cfg.Projects[0].Pipeline.Name != "my-pipeline" {
		t.Errorf("expected pipeline name my-pipeline, got %s", cfg.Projects[0].Pipeline.Name)
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

func TestLoad_noProjects(t *testing.T) {
	path := writeConfig(t, ``)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error for empty config: %v", err)
	}
	if len(cfg.Projects) != 0 {
		t.Errorf("expected 0 projects, got %d", len(cfg.Projects))
	}
}

func TestLoad_invalidPollInterval(t *testing.T) {
	path := writeConfig(t, `
[settings]
poll_interval_seconds = -1

[[projects]]
name = "p"

  [projects.pipeline]
  name = "pipe"
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for poll_interval_seconds = -1")
	}
}

func TestLoad_unknownAccount(t *testing.T) {
	path := writeConfig(t, `
[[projects]]
account = "nonexistent"
name    = "p"

  [projects.pipeline]
  name = "pipe"
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

[[projects]]
name    = "my-project"
account = "prod"

  [projects.pipeline]
  name = "deploy"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	acct, ok := cfg.AccountFor(cfg.Projects[0])
	if !ok {
		t.Fatal("expected account to be found")
	}
	if acct.Profile != "prod-profile" {
		t.Errorf("expected profile prod-profile, got %s", acct.Profile)
	}
}

func TestLoad_projectWithStacksAndECS(t *testing.T) {
	path := writeConfig(t, `
[[projects]]
name    = "annex-ims"
account = ""

  [projects.pipeline]
  name = "annex-ims-pipeline"

  [[projects.stacks]]
  name = "annex-ims-cluster"

  [[projects.ecs]]
  cluster  = "annex-ims-test"
  services = ["web", "worker"]
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	proj := cfg.Projects[0]
	if len(proj.Stacks) != 1 {
		t.Errorf("expected 1 stack, got %d", len(proj.Stacks))
	}
	if proj.Stacks[0].Name != "annex-ims-cluster" {
		t.Errorf("expected stack name annex-ims-cluster, got %s", proj.Stacks[0].Name)
	}
	if len(proj.ECS) != 1 {
		t.Errorf("expected 1 ecs entry, got %d", len(proj.ECS))
	}
	if proj.ECS[0].Cluster != "annex-ims-test" {
		t.Errorf("expected cluster annex-ims-test, got %s", proj.ECS[0].Cluster)
	}
	if len(proj.ECS[0].Services) != 2 {
		t.Errorf("expected 2 ecs services, got %d", len(proj.ECS[0].Services))
	}
}

func TestEnabledProjects_defaultsToEnabled(t *testing.T) {
	path := writeConfig(t, `
[[projects]]
name = "a"

  [projects.pipeline]
  name = "pipe-a"

[[projects]]
name    = "b"
enabled = false

  [projects.pipeline]
  name = "pipe-b"

[[projects]]
name    = "c"
enabled = true

  [projects.pipeline]
  name = "pipe-c"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Projects) != 3 {
		t.Fatalf("expected 3 projects, got %d", len(cfg.Projects))
	}
	enabled := cfg.EnabledProjects()
	if len(enabled) != 2 {
		t.Fatalf("expected 2 enabled projects, got %d", len(enabled))
	}
	if enabled[0].Name != "a" || enabled[1].Name != "c" {
		t.Errorf("expected a and c enabled, got %s and %s", enabled[0].Name, enabled[1].Name)
	}
	if cfg.Projects[1].IsEnabled() {
		t.Error("expected project b to be disabled")
	}
}

func TestToggleProject_persists(t *testing.T) {
	path := writeConfig(t, `
[[projects]]
name = "a"

  [projects.pipeline]
  name = "pipe-a"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := cfg.ToggleProject(0); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	if cfg.Projects[0].IsEnabled() {
		t.Error("expected project to be disabled after toggle")
	}

	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Projects[0].IsEnabled() {
		t.Error("expected disabled state to survive a reload")
	}
	if len(reloaded.EnabledProjects()) != 0 {
		t.Error("expected no enabled projects after reload")
	}

	if err := reloaded.ToggleProject(0); err != nil {
		t.Fatalf("toggle back: %v", err)
	}
	if !reloaded.Projects[0].IsEnabled() {
		t.Error("expected project to be enabled after second toggle")
	}
}

func TestToggleProject_outOfRange(t *testing.T) {
	path := writeConfig(t, `
[[projects]]
name = "a"

  [projects.pipeline]
  name = "pipe-a"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := cfg.ToggleProject(5); err == nil {
		t.Error("expected an error for an out-of-range index")
	}
}

func TestLoad_webhooks(t *testing.T) {
	path := writeConfig(t, `
[settings]
stuck_threshold_minutes = 10

[[projects]]
name = "a"

  [projects.pipeline]
  name = "pipe-a"

[[webhooks]]
url = "https://hooks.example.com/aws-green"

[[webhooks]]
url    = "http://localhost:9000/hook"
secret = "s3cret"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Webhooks) != 2 {
		t.Fatalf("expected 2 webhooks, got %d", len(cfg.Webhooks))
	}
	if cfg.Webhooks[0].URL != "https://hooks.example.com/aws-green" {
		t.Errorf("unexpected url %q", cfg.Webhooks[0].URL)
	}
	if cfg.Webhooks[0].Secret != "" {
		t.Errorf("expected no secret, got %q", cfg.Webhooks[0].Secret)
	}
	if cfg.Webhooks[1].Secret != "s3cret" {
		t.Errorf("unexpected secret %q", cfg.Webhooks[1].Secret)
	}
	if cfg.Settings.StuckThresholdMinutes != 10 {
		t.Errorf("expected threshold 10, got %d", cfg.Settings.StuckThresholdMinutes)
	}
}

func TestLoad_stuckThresholdDefault(t *testing.T) {
	path := writeConfig(t, `
[[projects]]
name = "a"

  [projects.pipeline]
  name = "pipe-a"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Settings.StuckThresholdMinutes != config.DefaultStuckThresholdMinutes {
		t.Errorf("expected default threshold %d, got %d",
			config.DefaultStuckThresholdMinutes, cfg.Settings.StuckThresholdMinutes)
	}
}

func TestLoad_invalidWebhooks(t *testing.T) {
	cases := map[string]string{
		"missing url":  "[[webhooks]]\nsecret = \"x\"\n",
		"empty url":    "[[webhooks]]\nurl = \"\"\n",
		"not a url":    "[[webhooks]]\nurl = \"not-a-url\"\n",
		"bad scheme":   "[[webhooks]]\nurl = \"ftp://example.com/hook\"\n",
		"negative ttl": "[settings]\nstuck_threshold_minutes = -1\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeConfig(t, body)
			if _, err := config.Load(path); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestSave_preservesWebhooks(t *testing.T) {
	path := writeConfig(t, `
[[projects]]
name = "a"

  [projects.pipeline]
  name = "pipe-a"

[[webhooks]]
url    = "https://hooks.example.com/aws-green"
secret = "s3cret"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A project edit from the TUI must not drop the webhook config.
	if err := cfg.AddProject(config.Project{Name: "b"}); err != nil {
		t.Fatalf("adding project: %v", err)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("reloading: %v", err)
	}
	if len(reloaded.Webhooks) != 1 || reloaded.Webhooks[0].Secret != "s3cret" {
		t.Errorf("webhooks lost on save: %+v", reloaded.Webhooks)
	}
	if reloaded.Settings.StuckThresholdMinutes != config.DefaultStuckThresholdMinutes {
		t.Errorf("threshold lost on save: %d", reloaded.Settings.StuckThresholdMinutes)
	}
}
