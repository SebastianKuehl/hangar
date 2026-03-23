package main

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	hangarruntime "github.com/SebastianKuehl/hangar/internal/runtime"
	tea "github.com/charmbracelet/bubbletea"
)

func TestServiceItemsShowsRuntimeIcons(t *testing.T) {
	project := Project{
		Services: []Service{{Name: "api"}, {Name: "web"}, {Name: "worker"}},
	}

	got := serviceItems(project, []serviceRuntime{
		{known: true, running: true},
		{known: true, running: false},
		{},
	}, nil)

	want := []string{"● api", "○ web", "◌ worker"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("serviceItems = %#v, want %#v", got, want)
	}
}

func TestServicePaneContentReflectsBufferedLogs(t *testing.T) {
	project := Project{
		Name:     "Demo",
		Path:     filepath.Join(string(filepath.Separator), "workspace", "demo"),
		Services: []Service{{Name: "api", Path: filepath.Join("apps", "api"), Command: "npm run start"}},
	}
	key := serviceKey(project, project.Services[0])
	m := model{
		cfg: Config{Projects: []Project{project}},
		serviceRuntime: []serviceRuntime{{
			known:   true,
			running: true,
			runtime: hangarruntime.ServiceRuntime{
				PID:       42,
				LogPath:   "/tmp/demo-api.log",
				WorkDir:   filepath.Join(project.Path, "apps", "api"),
				StartedAt: time.Unix(1710000000, 0).UTC(),
			},
		}},
		serviceStates: map[string]serviceTransition{},
		logLines:      map[string][]string{key: {"boot", "ready"}},
	}
	m.syncSelectionState()

	if !strings.Contains(strings.Join(m.details.items, "\n"), "PID: 42") {
		t.Fatalf("expected details to include PID, got %#v", m.details.items)
	}
	if !reflect.DeepEqual(m.logs.items, []string{"boot", "ready"}) {
		t.Fatalf("logs.items = %#v, want %#v", m.logs.items, []string{"boot", "ready"})
	}
}

func TestUpdateAppliesLogChunksToSelectedService(t *testing.T) {
	project := Project{
		Name:     "Demo",
		Path:     filepath.Join(string(filepath.Separator), "workspace", "demo"),
		Services: []Service{{Name: "api", Path: filepath.Join("apps", "api"), Command: "npm run start"}},
	}
	key := serviceKey(project, project.Services[0])
	m := model{
		cfg:              Config{Projects: []Project{project}},
		serviceRuntime:   []serviceRuntime{{known: true, runtime: hangarruntime.ServiceRuntime{LogPath: "/tmp/demo-api.log"}}},
		serviceStates:    map[string]serviceTransition{},
		logLines:         map[string][]string{},
		logEvents:        make(chan tea.Msg),
		followingService: key,
		logWatchID:       7,
	}
	m.syncSelectionState()

	updated, _ := m.Update(logChunkMsg{serviceKey: key, watchID: 7, lines: []string{"one", "two"}, reset: true})
	got := updated.(model)
	if !reflect.DeepEqual(got.logs.items, []string{"one", "two"}) {
		t.Fatalf("logs after reset = %#v, want %#v", got.logs.items, []string{"one", "two"})
	}

	updated, _ = got.Update(logChunkMsg{serviceKey: key, watchID: 7, lines: []string{"three"}})
	got = updated.(model)
	if !reflect.DeepEqual(got.logs.items, []string{"one", "two", "three"}) {
		t.Fatalf("logs after append = %#v, want %#v", got.logs.items, []string{"one", "two", "three"})
	}
	if got.logs.selected != 2 {
		t.Fatalf("expected logs to auto-follow the newest line, selected=%d", got.logs.selected)
	}
}

func TestSyncSelectionStateSwitchesBetweenServiceBuffers(t *testing.T) {
	project := Project{
		Name:     "Demo",
		Path:     filepath.Join(string(filepath.Separator), "workspace", "demo"),
		Services: []Service{{Name: "api"}, {Name: "worker"}},
	}
	apiKey := serviceKey(project, project.Services[0])
	workerKey := serviceKey(project, project.Services[1])
	m := model{
		cfg:            Config{Projects: []Project{project}},
		serviceRuntime: []serviceRuntime{{known: true}, {known: true}},
		serviceStates:  map[string]serviceTransition{},
		logLines: map[string][]string{
			apiKey:    {"api-1"},
			workerKey: {"worker-1", "worker-2"},
		},
	}
	m.syncSelectionState()
	if !reflect.DeepEqual(m.logs.items, []string{"api-1"}) {
		t.Fatalf("initial logs.items = %#v, want %#v", m.logs.items, []string{"api-1"})
	}

	m.services.selected = 1
	m.syncSelectionState()
	if !reflect.DeepEqual(m.logs.items, []string{"worker-1", "worker-2"}) {
		t.Fatalf("switched logs.items = %#v, want %#v", m.logs.items, []string{"worker-1", "worker-2"})
	}
}

func TestUpdateDoesNotAutoFollowLogsWhenUserScrolledUp(t *testing.T) {
	project := Project{
		Name:     "Demo",
		Path:     filepath.Join(string(filepath.Separator), "workspace", "demo"),
		Services: []Service{{Name: "api", Path: filepath.Join("apps", "api"), Command: "npm run start"}},
	}
	key := serviceKey(project, project.Services[0])
	m := model{
		cfg: Config{Projects: []Project{project}},
		serviceRuntime: []serviceRuntime{{
			known:   true,
			runtime: hangarruntime.ServiceRuntime{LogPath: "/tmp/demo-api.log"},
		}},
		serviceStates:    map[string]serviceTransition{},
		logLines:         map[string][]string{key: {"one", "two"}},
		followingService: key,
		logWatchID:       7,
	}
	m.syncSelectionState()
	m.logs.selected = 0

	updated, _ := m.Update(logChunkMsg{serviceKey: key, watchID: 7, lines: []string{"three"}})
	got := updated.(model)
	if got.logs.selected != 0 {
		t.Fatalf("expected manual scroll position to remain when not at bottom, selected=%d", got.logs.selected)
	}
}

func TestRenderListPaneScrollsToSelectedLogLine(t *testing.T) {
	items := []string{
		"log-0", "log-1", "log-2", "log-3", "log-4",
		"log-5", "log-6", "log-7", "log-8",
	}
	pane := listPane{
		title:    "Logs",
		items:    items,
		selected: 7,
	}

	m := newModel()
	rendered := m.renderListPane(pane, 24, 7, true, false, false)

	if !strings.Contains(rendered, "> log-7") {
		t.Fatalf("expected rendered pane to include selected log line, got %q", rendered)
	}
	if strings.Contains(rendered, "log-0") {
		t.Fatalf("expected top of log pane to scroll away from earliest rows, got %q", rendered)
	}
}
