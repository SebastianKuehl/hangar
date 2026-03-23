package main

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
	}, nil)

	want := []string{"● api", "○ web", "◌ worker"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("serviceItems = %#v, want %#v", got, want)
	}
}

func TestServiceItemsShowPendingTransitionIcon(t *testing.T) {
	project := Project{
		Path: filepath.Join(string(filepath.Separator), "workspace", "demo"),
		Services: []Service{
			{Name: "api", Path: filepath.Join("apps", "api")},
			{Name: "web", Path: filepath.Join("apps", "web")},
		},
	}

	got := serviceItems(project, []serviceRuntime{
		{known: true, running: false},
		{known: true, running: false},
	}, map[string]serviceTransition{
		serviceKey(project, project.Services[1]): {targetRunning: true},
	})

	want := []string{"○ api", "◔ web"}
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
		serviceOwners: map[string]int32{},
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
		serviceOwners:  map[string]int32{},
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

func TestUpdateStartsStoppedServiceAndBlocksDuplicateToggle(t *testing.T) {
	previousStart := startServiceProcess
	defer func() { startServiceProcess = previousStart }()

	startCalls := 0
	startServiceProcess = func(project Project, service Service) (int32, error) {
		startCalls++
		return 77, nil
	}

	project := Project{
		Name: "Demo",
		Path: filepath.Join(string(filepath.Separator), "workspace", "demo"),
		Services: []Service{
			{Name: "api", Path: filepath.Join("apps", "api"), Command: "npm run start"},
		},
	}
	m := model{
		cfg: Config{Projects: []Project{project}},
		serviceRuntime: []serviceRuntime{
			{known: true, running: false},
		},
		serviceStates: map[string]serviceTransition{},
		serviceOwners: map[string]int32{},
		focus:         paneServices,
	}
	m.syncSelectionState()

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	got := updated.(model)
	if cmd == nil {
		t.Fatal("expected start command to be scheduled")
	}

	msg, ok := cmd().(serviceControlMsg)
	if !ok {
		t.Fatalf("expected serviceControlMsg, got %T", cmd())
	}
	if msg.err != nil {
		t.Fatalf("expected no start error, got %v", msg.err)
	}
	if msg.startedPID != 77 {
		t.Fatalf("expected started pid to be recorded, got %d", msg.startedPID)
	}
	if startCalls != 1 {
		t.Fatalf("expected one start call, got %d", startCalls)
	}
	if _, ok := got.serviceStates[serviceKey(project, project.Services[0])]; !ok {
		t.Fatal("expected service transition to be tracked while start is pending")
	}

	updated, duplicateCmd := got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	got = updated.(model)
	if duplicateCmd != nil {
		t.Fatal("expected duplicate s input to be ignored while service is starting")
	}
	if startCalls != 1 {
		t.Fatalf("expected duplicate toggle to be ignored, got %d start calls", startCalls)
	}
	if _, ok := got.serviceStates[serviceKey(project, project.Services[0])]; !ok {
		t.Fatal("expected service transition to remain tracked")
	}
}

func TestUpdateAllowsOtherServicesWhileTransitionPending(t *testing.T) {
	previousStart := startServiceProcess
	defer func() { startServiceProcess = previousStart }()

	started := []string{}
	startServiceProcess = func(project Project, service Service) (int32, error) {
		started = append(started, service.Name)
		return int32(len(started)), nil
	}

	project := Project{
		Name: "Demo",
		Path: filepath.Join(string(filepath.Separator), "workspace", "demo"),
		Services: []Service{
			{Name: "api", Path: filepath.Join("apps", "api"), Command: "npm run start"},
			{Name: "web", Path: filepath.Join("apps", "web"), Command: "npm run start"},
		},
	}
	m := model{
		cfg: Config{Projects: []Project{project}},
		serviceRuntime: []serviceRuntime{
			{known: true, running: false},
			{known: true, running: false},
		},
		serviceStates: map[string]serviceTransition{},
		serviceOwners: map[string]int32{},
		focus:         paneServices,
	}
	m.syncSelectionState()

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	got := updated.(model)
	if cmd == nil {
		t.Fatal("expected first service start command to be scheduled")
	}
	_ = cmd()

	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	got = updated.(model)

	updated, cmd = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	got = updated.(model)
	if cmd == nil {
		t.Fatal("expected second service start command to be scheduled")
	}
	_ = cmd()

	if !reflect.DeepEqual(started, []string{"api", "web"}) {
		t.Fatalf("expected both services to be startable while one is pending, got %#v", started)
	}
	if len(got.serviceStates) != 2 {
		t.Fatalf("expected both services to remain locked independently, got %#v", got.serviceStates)
	}
}

func TestRuntimeRefreshUnlocksServiceAfterRequestedState(t *testing.T) {
	previousStart := startServiceProcess
	previousStop := stopServiceProcesses
	defer func() {
		startServiceProcess = previousStart
		stopServiceProcesses = previousStop
	}()

	startServiceProcess = func(project Project, service Service) (int32, error) { return 42, nil }
	stopCalls := 0
	stopServiceProcesses = func(runtime serviceRuntime, ownedPID int32) error {
		stopCalls++
		if ownedPID != 42 {
			t.Fatalf("expected owned pid 42, got %d", ownedPID)
		}
		return nil
	}

	project := Project{
		Name: "Demo",
		Path: filepath.Join(string(filepath.Separator), "workspace", "demo"),
		Services: []Service{
			{Name: "api", Path: filepath.Join("apps", "api"), Command: "npm run start"},
		},
	}
	m := model{
		cfg: Config{Projects: []Project{project}},
		serviceRuntime: []serviceRuntime{
			{known: true, running: false},
		},
		serviceStates: map[string]serviceTransition{},
		serviceOwners: map[string]int32{},
		focus:         paneServices,
	}
	m.syncSelectionState()

	updated, startCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	got := updated.(model)
	if startCmd == nil {
		t.Fatal("expected start command to be scheduled")
	}

	updated, refreshCmd := got.Update(startCmd())
	got = updated.(model)
	if refreshCmd == nil {
		t.Fatal("expected runtime refresh after service control succeeds")
	}
	if got.serviceOwners[serviceKey(project, project.Services[0])] != 42 {
		t.Fatalf("expected started pid to be stored for safe stop, got %#v", got.serviceOwners)
	}

	updated, _ = got.Update(runtimeRefreshMsg{
		projectIndex: 0,
		requestID:    got.runtimeRequest,
		projectPath:  project.Path,
		serviceCount: 1,
		runtime: []serviceRuntime{
			{known: true, running: true, process: processSnapshot{PID: 42}},
		},
	})
	got = updated.(model)
	if len(got.serviceStates) != 0 {
		t.Fatalf("expected service transition to clear once runtime matches target, got %#v", got.serviceStates)
	}

	updated, stopCmd := got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	got = updated.(model)
	if stopCmd == nil {
		t.Fatal("expected stop command to be scheduled after service start is confirmed")
	}
	if _, ok := stopCmd().(serviceControlMsg); !ok {
		t.Fatalf("expected stop command to produce serviceControlMsg, got %T", stopCmd())
	}
	if stopCalls != 1 {
		t.Fatalf("expected stop command to run once, got %d", stopCalls)
	}
}

func TestRuntimeRefreshUnlocksTimedOutTransition(t *testing.T) {
	project := Project{
		Name: "Demo",
		Path: filepath.Join(string(filepath.Separator), "workspace", "demo"),
		Services: []Service{
			{Name: "api", Path: filepath.Join("apps", "api"), Command: "npm run start"},
		},
	}
	key := serviceKey(project, project.Services[0])
	m := model{
		cfg: Config{Projects: []Project{project}},
		serviceRuntime: []serviceRuntime{
			{known: true, running: false},
		},
		serviceStates: map[string]serviceTransition{
			key: {targetRunning: true, polls: maxServiceTransitionPolls - 1},
		},
		serviceOwners:  map[string]int32{key: 42},
		runtimeRequest: 1,
	}
	m.projects.selected = 0
	m.syncSelectionState()

	updated, _ := m.Update(runtimeRefreshMsg{
		projectIndex: 0,
		requestID:    1,
		projectPath:  project.Path,
		serviceCount: 1,
		runtime: []serviceRuntime{
			{known: true, running: false},
		},
	})
	got := updated.(model)
	if len(got.serviceStates) != 0 {
		t.Fatalf("expected timed-out transition to unlock, got %#v", got.serviceStates)
	}
	if len(got.serviceOwners) != 0 {
		t.Fatalf("expected timed-out transition to clear tracked pid, got %#v", got.serviceOwners)
	}
	if !strings.Contains(got.errMsg, "Timed out while starting api") {
		t.Fatalf("expected timeout error message, got %q", got.errMsg)
	}
}

func TestRunServiceStopAllowsOwnedProcessTrees(t *testing.T) {
	previousTree := ownedProcessTreePIDs
	previousTerminate := terminateProcessByPID
	defer func() {
		ownedProcessTreePIDs = previousTree
		terminateProcessByPID = previousTerminate
	}()

	ownedProcessTreePIDs = func(rootPID int32) map[int32]struct{} {
		return map[int32]struct{}{42: {}, 43: {}}
	}

	terminated := []int32{}
	terminateProcessByPID = func(pid int32) error {
		terminated = append(terminated, pid)
		return nil
	}

	err := runServiceStop(serviceRuntime{
		process: processSnapshot{PID: 42},
		processes: []processSnapshot{
			{PID: 42},
			{PID: 43},
		},
	}, 42)
	if err != nil {
		t.Fatalf("expected owned process tree to stop cleanly, got %v", err)
	}
	if !reflect.DeepEqual(terminated, []int32{42, 43}) {
		t.Fatalf("expected to terminate owned process tree, got %#v", terminated)
	}
}

func TestRunServiceStopAllowsSingleExternalMatchedTree(t *testing.T) {
	previousParent := parentPIDForProcess
	previousTerminate := terminateProcessByPID
	defer func() {
		parentPIDForProcess = previousParent
		terminateProcessByPID = previousTerminate
	}()

	parentPIDForProcess = func(pid int32) (int32, error) {
		switch pid {
		case 42:
			return 1, nil
		case 43:
			return 42, nil
		case 44:
			return 42, nil
		default:
			return 0, errors.New("unknown pid")
		}
	}

	terminated := []int32{}
	terminateProcessByPID = func(pid int32) error {
		terminated = append(terminated, pid)
		return nil
	}

	err := runServiceStop(serviceRuntime{
		processes: []processSnapshot{
			{PID: 42},
			{PID: 43},
			{PID: 44},
		},
	}, 0)
	if err != nil {
		t.Fatalf("expected single external process tree to stop cleanly, got %v", err)
	}
	if !reflect.DeepEqual(terminated, []int32{42, 43, 44}) {
		t.Fatalf("expected to terminate entire matched tree, got %#v", terminated)
	}
}

func TestRunServiceStopRejectsStaleOwnedPIDForMultipleMatches(t *testing.T) {
	previousTree := ownedProcessTreePIDs
	defer func() { ownedProcessTreePIDs = previousTree }()

	ownedProcessTreePIDs = func(rootPID int32) map[int32]struct{} {
		return map[int32]struct{}{}
	}

	err := runServiceStop(serviceRuntime{
		processes: []processSnapshot{
			{PID: 42},
			{PID: 43},
		},
	}, 99)
	if err == nil {
		t.Fatal("expected stale owned pid with multiple matches to fail safely")
	}
}

func TestRunServiceStopRejectsMultipleExternalRoots(t *testing.T) {
	previousParent := parentPIDForProcess
	defer func() { parentPIDForProcess = previousParent }()

	parentPIDForProcess = func(pid int32) (int32, error) {
		switch pid {
		case 42:
			return 1, nil
		case 43:
			return 2, nil
		default:
			return 0, errors.New("unknown pid")
		}
	}

	err := runServiceStop(serviceRuntime{
		processes: []processSnapshot{
			{PID: 42},
			{PID: 43},
		},
	}, 0)
	if err == nil {
		t.Fatal("expected multiple external roots to remain protected")
	}
}

func TestRuntimeRefreshErrorUnlocksTimedOutTransition(t *testing.T) {
	project := Project{
		Name: "Demo",
		Path: filepath.Join(string(filepath.Separator), "workspace", "demo"),
		Services: []Service{
			{Name: "api", Path: filepath.Join("apps", "api"), Command: "npm run start"},
		},
	}
	key := serviceKey(project, project.Services[0])
	m := model{
		cfg: Config{Projects: []Project{project}},
		serviceRuntime: []serviceRuntime{
			{known: true, running: false},
		},
		serviceStates: map[string]serviceTransition{
			key: {targetRunning: true, polls: maxServiceTransitionPolls - 1},
		},
		serviceOwners:  map[string]int32{key: 42},
		runtimeRequest: 1,
	}
	m.projects.selected = 0
	m.syncSelectionState()

	updated, _ := m.Update(runtimeRefreshMsg{
		projectIndex: 0,
		requestID:    1,
		projectPath:  project.Path,
		serviceCount: 1,
		err:          errors.New("boom"),
	})
	got := updated.(model)
	if len(got.serviceStates) != 0 {
		t.Fatalf("expected timed-out transition to unlock after refresh errors, got %#v", got.serviceStates)
	}
	if len(got.serviceOwners) != 0 {
		t.Fatalf("expected timed-out transition to clear tracked pid after refresh errors, got %#v", got.serviceOwners)
	}
	if !strings.Contains(got.errMsg, "Timed out while starting api") {
		t.Fatalf("expected timeout error after repeated refresh failures, got %q", got.errMsg)
	}
}

func TestRuntimeRefreshPrunesStaleOwnedPID(t *testing.T) {
	project := Project{
		Name: "Demo",
		Path: filepath.Join(string(filepath.Separator), "workspace", "demo"),
		Services: []Service{
			{Name: "api", Path: filepath.Join("apps", "api"), Command: "npm run start"},
		},
	}
	key := serviceKey(project, project.Services[0])
	m := model{
		cfg: Config{Projects: []Project{project}},
		serviceRuntime: []serviceRuntime{
			{known: true, running: true, process: processSnapshot{PID: 42}},
		},
		serviceOwners:  map[string]int32{key: 99},
		runtimeRequest: 1,
	}
	m.projects.selected = 0
	m.syncSelectionState()

	updated, _ := m.Update(runtimeRefreshMsg{
		projectIndex: 0,
		requestID:    1,
		projectPath:  project.Path,
		serviceCount: 1,
		runtime: []serviceRuntime{
			{known: true, running: true, process: processSnapshot{PID: 42}, processes: []processSnapshot{{PID: 42}}},
		},
	})
	got := updated.(model)
	if len(got.serviceOwners) != 0 {
		t.Fatalf("expected stale owner pid to be pruned, got %#v", got.serviceOwners)
	}
}
