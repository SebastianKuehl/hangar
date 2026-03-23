package main

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMatchProjectRuntime(t *testing.T) {
	project := Project{
		Name: "Demo",
		Path: filepath.Join(string(filepath.Separator), "workspace", "demo"),
		Services: []Service{
			{Name: "api", Path: filepath.Join("apps", "api"), Command: "npm run start"},
			{Name: "web", Path: filepath.Join("apps", "web"), Command: "bun run start"},
		},
	}

	runtime := matchProjectRuntime(project, []processSnapshot{
		{
			PID:       101,
			Name:      "node",
			Cmdline:   "node server.js",
			Cwd:       filepath.Join(project.Path, "apps", "api"),
			Exe:       "/usr/local/bin/node",
			Status:    "running",
			RSS:       1024,
			CreatedAt: 100,
		},
		{
			PID:       202,
			Name:      "bun",
			Cmdline:   "bun run start",
			Cwd:       filepath.Join(project.Path, "apps", "web"),
			Exe:       "/usr/local/bin/bun",
			Status:    "running",
			RSS:       2048,
			CreatedAt: 200,
		},
		{
			PID:       303,
			Name:      "python",
			Cmdline:   "python worker.py",
			Cwd:       filepath.Join(project.Path, "apps", "api"),
			Exe:       "/usr/bin/python",
			Status:    "running",
			CreatedAt: 300,
		},
	})

	if len(runtime) != 2 {
		t.Fatalf("expected 2 runtime entries, got %d", len(runtime))
	}
	if !runtime[0].running || runtime[0].process.PID != 101 {
		t.Fatalf("expected api runtime to match PID 101, got %#v", runtime[0])
	}
	if !runtime[1].running || runtime[1].process.PID != 202 {
		t.Fatalf("expected web runtime to match PID 202, got %#v", runtime[1])
	}
}

func TestServiceItemsShowsRuntimeIcons(t *testing.T) {
	project := Project{
		Services: []Service{
			{Name: "api"},
			{Name: "web"},
			{Name: "worker"},
		},
	}

	got := serviceItems(project, []serviceRuntime{
		{known: true, running: true},
		{known: true, running: false},
		{},
	})

	want := []string{"● api", "○ web", "◌ worker"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("serviceItems = %#v, want %#v", got, want)
	}
}

func TestServicePaneContentReflectsSelectedService(t *testing.T) {
	m := model{
		cfg: Config{
			Projects: []Project{
				{
					Name: "Demo",
					Path: filepath.Join(string(filepath.Separator), "workspace", "demo"),
					Services: []Service{
						{Name: "api", Path: filepath.Join("apps", "api"), Command: "npm run start"},
						{Name: "web", Path: filepath.Join("apps", "web"), Command: "bun run start"},
					},
				},
			},
		},
		serviceRuntime: []serviceRuntime{
			{known: true, running: false},
			{
				known:     true,
				running:   true,
				instances: 1,
				process: processSnapshot{
					PID:       42,
					Cwd:       filepath.Join(string(filepath.Separator), "workspace", "demo", "apps", "web"),
					Cmdline:   "bun run start",
					Exe:       "/usr/local/bin/bun",
					Status:    "running",
					RSS:       2048,
					CreatedAt: 1710000000000,
				},
			},
		},
	}
	m.projects.selected = 0
	m.services.selected = 1

	m.syncSelectionState()

	if !strings.Contains(strings.Join(m.details.items, "\n"), "PID: 42") {
		t.Fatalf("expected details to include selected service process details, got %#v", m.details.items)
	}
	if !strings.Contains(strings.Join(m.logs.items, "\n"), "Live logs are unavailable") {
		t.Fatalf("expected logs pane to explain external log limitation, got %#v", m.logs.items)
	}
	if got := m.services.items[0]; got != "○ api" {
		t.Fatalf("expected first service to show not-running icon, got %q", got)
	}
	if got := m.services.items[1]; got != "● web" {
		t.Fatalf("expected second service to show running icon, got %q", got)
	}
}

func TestUpdateIgnoresStaleRuntimeRefresh(t *testing.T) {
	m := model{
		cfg: Config{
			Projects: []Project{
				{
					Name: "Demo",
					Path: filepath.Join(string(filepath.Separator), "workspace", "demo"),
					Services: []Service{
						{Name: "api", Path: filepath.Join("apps", "api"), Command: "npm run start"},
					},
				},
			},
		},
		runtimeRequest: 2,
	}
	m.projects.selected = 0
	m.syncSelectionState()

	updated, _ := m.Update(runtimeRefreshMsg{
		projectIndex: 0,
		requestID:    1,
		projectPath:  filepath.Join(string(filepath.Separator), "workspace", "demo"),
		serviceCount: 1,
		runtime: []serviceRuntime{
			{known: true, running: true, process: processSnapshot{PID: 99}},
		},
	})

	got := updated.(model)
	if got.selectedServiceRuntime().running {
		t.Fatalf("stale runtime refresh should not be applied: %#v", got.selectedServiceRuntime())
	}
}
