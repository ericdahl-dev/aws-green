package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericdahl-dev/aws-green/internal/config"
	"github.com/ericdahl-dev/aws-green/internal/discover"
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
	m := NewManage(cfg, nil)
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
	m := NewManage(cfg, nil)
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

// The form edits name, account and pipeline; everything else about the
// project, including whether it is disabled, has to survive the edit.
func TestEditProjectKeepsItDisabled(t *testing.T) {
	cfg := loadManageConfig(t)
	disabled := false
	cfg.Projects[0].Enabled = &disabled
	m := NewManage(cfg, nil)
	m = send(m, key("e"))
	m = enter(typeText(m, "-prod"))
	m = enter(m)
	_ = enter(m)

	if got := cfg.Projects[0]; got.Name != "honeycomb-prod" || got.IsEnabled() {
		t.Errorf("edited project = %+v (enabled=%v), want honeycomb-prod still disabled", got, got.IsEnabled())
	}
}

// fakeDiscoverer records what it was asked and answers with a canned result.
type fakeDiscoverer struct {
	calls                     int
	profile, region, pipeline string
	result                    discover.Result
	err                       error
}

func (f *fakeDiscoverer) discover(_ context.Context, profile, region, pipeline string) (discover.Result, error) {
	f.calls++
	f.profile, f.region, f.pipeline = profile, region, pipeline
	return f.result, f.err
}

func addReserves(m Manage) Manage {
	m = send(m, key("a"))
	m = enter(typeText(m, "reserves"))
	m = enter(typeText(m, "libnd"))
	return enter(typeText(m, "reserves-pipeline"))
}

// Adding a project looks up its stacks and ECS services in its Account and
// saves the ones the user confirms.
func TestAddProjectSavesDiscoveredResources(t *testing.T) {
	cfg := loadManageConfig(t)
	d := &fakeDiscoverer{result: discover.Result{
		Stacks: []string{"reserves-cluster", "reserves-service"},
		ECS:    []config.ECSConfig{{Cluster: "reserves-cluster-A", Services: []string{"reserves-app"}}},
	}}
	m := addReserves(NewManage(cfg, d.discover))
	if m.mode != manageModeConfirmDiscovered {
		t.Fatalf("mode = %v, want the discovery confirmation", m.mode)
	}
	m = enter(m) // stacks, as pre-selected
	m = enter(m) // ECS services, as pre-selected

	if d.calls != 1 || d.profile != "libnd-view" || d.region != "us-east-1" || d.pipeline != "reserves-pipeline" {
		t.Errorf("discovery asked for %+v, want libnd-view / us-east-1 / reserves-pipeline", d)
	}
	if m.mode != manageModeList {
		t.Fatalf("mode = %v, want list after confirming", m.mode)
	}
	got := cfg.Projects[len(cfg.Projects)-1]
	if !reflect.DeepEqual(got.Stacks, []config.Stack{{Name: "reserves-cluster"}, {Name: "reserves-service"}}) {
		t.Errorf("Stacks = %+v", got.Stacks)
	}
	if !reflect.DeepEqual(got.ECS, d.result.ECS) {
		t.Errorf("ECS = %+v, want %+v", got.ECS, d.result.ECS)
	}
}

// A shared tag also finds sibling projects; only the likely picks are
// pre-selected, so accepting the defaults leaves the siblings out.
func TestAddProjectPreselectsOnlyLikelyResources(t *testing.T) {
	cfg := loadManageConfig(t)
	d := &fakeDiscoverer{result: discover.Result{
		Stacks: []string{"dec-prod-beehive", "reserves-service"},
		ECS:    []config.ECSConfig{{Cluster: "shared", Services: []string{"dec-beehive-app", "reserves-app"}}},
	}}
	m := enter(enter(addReserves(NewManage(cfg, d.discover))))

	got := cfg.Projects[len(cfg.Projects)-1]
	if !reflect.DeepEqual(got.Stacks, []config.Stack{{Name: "reserves-service"}}) {
		t.Errorf("Stacks = %+v, want only reserves-service", got.Stacks)
	}
	if want := []config.ECSConfig{{Cluster: "shared", Services: []string{"reserves-app"}}}; !reflect.DeepEqual(got.ECS, want) {
		t.Errorf("ECS = %+v, want %+v", got.ECS, want)
	}
	_ = m
}

// Discovery is a convenience: when it fails the project still saves, and the
// user is told why nothing was found.
func TestAddProjectSavesWhenDiscoveryFails(t *testing.T) {
	cfg := loadManageConfig(t)
	d := &fakeDiscoverer{err: errors.New("AccessDenied")}
	m := addReserves(NewManage(cfg, d.discover))

	if m.mode != manageModeList {
		t.Fatalf("mode = %v, want list", m.mode)
	}
	if got := cfg.Projects[len(cfg.Projects)-1]; got.Name != "reserves" || len(got.Stacks) != 0 {
		t.Errorf("saved %+v, want reserves with no stacks", got)
	}
	if !strings.Contains(m.err, "AccessDenied") {
		t.Errorf("err = %q, want the discovery error shown", m.err)
	}
}

// Editing a project whose stacks and services are already configured leaves
// them alone — no lookup, no confirmation.
func TestEditProjectWithResourcesSkipsDiscovery(t *testing.T) {
	cfg := loadManageConfig(t)
	cfg.Projects[0].Stacks = []config.Stack{{Name: "honeycomb-stack"}}
	d := &fakeDiscoverer{result: discover.Result{Stacks: []string{"other"}}}
	m := send(NewManage(cfg, d.discover), key("e"))
	m = enter(enter(enter(m)))

	if d.calls != 0 {
		t.Errorf("discovery ran %d times, want 0", d.calls)
	}
	if m.mode != manageModeList {
		t.Errorf("mode = %v, want list", m.mode)
	}
}
