package ui

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericdahl-dev/aws-green/internal/config"
)

func loadManageConfig(t *testing.T) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `
[[accounts]]
  name = "libnd"
  profile = "libnd-view"
  region = "us-east-1"

[[projects]]
  name = "honeycomb"
  account = "libnd"
  [projects.pipeline]
    name = "honeycomb-pipeline"
`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// runCmd resolves a command into the messages it produces, the way the Bubble
// Tea runtime would. Commands that block (cursor blink ticks) are dropped.
func runCmd(cmd tea.Cmd, depth int) []tea.Msg {
	if cmd == nil || depth > 5 {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(20 * time.Millisecond):
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, runCmd(c, depth+1)...)
		}
		return out
	}
	// tea.Sequence produces an unexported slice of commands.
	if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
		var out []tea.Msg
		for i := 0; i < v.Len(); i++ {
			out = append(out, runCmd(v.Index(i).Interface().(tea.Cmd), depth+1)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

// send delivers msg to the manage screen and feeds back any messages its
// commands produce, so huh form navigation completes as it would at runtime.
func send(m Manage, msg tea.Msg) Manage {
	queue := []tea.Msg{msg}
	for i := 0; len(queue) > 0 && i < 50; i++ {
		next := queue[0]
		queue = queue[1:]
		if _, ok := next.(ConfigChangedMsg); ok {
			continue
		}
		var cmd tea.Cmd
		m, cmd = m.Update(next)
		queue = append(queue, runCmd(cmd, 0)...)
	}
	return m
}

func typeText(m Manage, s string) Manage {
	for _, r := range s {
		m = send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

func enter(m Manage) Manage { return send(m, tea.KeyMsg{Type: tea.KeyEnter}) }

func TestAddProjectSavesEnteredValues(t *testing.T) {
	cfg := loadManageConfig(t)
	m := NewManage(cfg)
	m = send(m, key("a"))
	m = enter(typeText(m, "annex-ims"))
	m = enter(typeText(m, "libnd"))
	m = enter(typeText(m, "annex-pipeline"))

	if m.mode != manageModeList {
		t.Fatalf("mode = %v, want list after submitting form", m.mode)
	}
	if len(cfg.Projects) != 2 {
		t.Fatalf("got %d projects, want 2", len(cfg.Projects))
	}
	got := cfg.Projects[1]
	if got.Name != "annex-ims" || got.Account != "libnd" || got.Pipeline.Name != "annex-pipeline" {
		t.Errorf("added project = %+v, want annex-ims / libnd / annex-pipeline", got)
	}

	reloaded, err := config.Load(cfg.Path())
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Projects[1].Name != "annex-ims" {
		t.Errorf("saved project name = %q, want annex-ims", reloaded.Projects[1].Name)
	}
}

func TestEditProjectSavesChangedValues(t *testing.T) {
	cfg := loadManageConfig(t)
	cfg.Projects[0].Stacks = []config.Stack{{Name: "honeycomb-stack"}}
	m := NewManage(cfg)
	m = send(m, key("e"))
	m = enter(typeText(m, "-prod"))
	m = enter(m)
	_ = enter(m)

	got := cfg.Projects[0]
	if got.Name != "honeycomb-prod" || got.Account != "libnd" || got.Pipeline.Name != "honeycomb-pipeline" {
		t.Errorf("edited project = %+v, want honeycomb-prod / libnd / honeycomb-pipeline", got)
	}
	if len(got.Stacks) != 1 || got.Stacks[0].Name != "honeycomb-stack" {
		t.Errorf("stacks = %+v, want honeycomb-stack preserved", got.Stacks)
	}
}
