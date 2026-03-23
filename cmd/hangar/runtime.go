package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shirou/gopsutil/v4/process"
)

const runtimeRefreshInterval = 2 * time.Second

type processSnapshot struct {
	PID       int32
	Name      string
	Cmdline   string
	Cwd       string
	Exe       string
	Status    string
	RSS       uint64
	CreatedAt int64
}

type serviceRuntime struct {
	known     bool
	running   bool
	instances int
	process   processSnapshot
}

type runtimeRefreshMsg struct {
	projectIndex int
	requestID    int
	projectPath  string
	serviceCount int
	runtime      []serviceRuntime
	err          error
}

type runtimeTickMsg time.Time

var listProcessSnapshots = loadProcessSnapshots

func nextRuntimeRefreshCmd() tea.Cmd {
	return tea.Tick(runtimeRefreshInterval, func(t time.Time) tea.Msg {
		return runtimeTickMsg(t)
	})
}

func refreshProjectRuntimeCmd(requestID, projectIndex int, project Project) tea.Cmd {
	return func() tea.Msg {
		snapshots, err := listProcessSnapshots()
		if err != nil {
			return runtimeRefreshMsg{projectIndex: projectIndex, requestID: requestID, projectPath: project.Path, serviceCount: len(project.Services), err: err}
		}
		return runtimeRefreshMsg{
			projectIndex: projectIndex,
			requestID:    requestID,
			projectPath:  project.Path,
			serviceCount: len(project.Services),
			runtime:      matchProjectRuntime(project, snapshots),
		}
	}
}

func loadProcessSnapshots() ([]processSnapshot, error) {
	processes, err := process.Processes()
	if err != nil {
		return nil, err
	}

	snapshots := make([]processSnapshot, 0, len(processes))
	for _, proc := range processes {
		snapshot := processSnapshot{PID: proc.Pid}
		snapshot.Name, _ = proc.Name()
		snapshot.Cmdline, _ = proc.Cmdline()
		snapshot.Cwd, _ = proc.Cwd()
		snapshot.Exe, _ = proc.Exe()
		if status, err := proc.Status(); err == nil {
			snapshot.Status = strings.Join(status, ", ")
		}
		if mem, err := proc.MemoryInfo(); err == nil && mem != nil {
			snapshot.RSS = mem.RSS
		}
		snapshot.CreatedAt, _ = proc.CreateTime()
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func matchProjectRuntime(project Project, snapshots []processSnapshot) []serviceRuntime {
	runtime := make([]serviceRuntime, len(project.Services))
	for i, service := range project.Services {
		serviceDir := canonicalRuntimePath(servicePath(project, service))
		best := serviceRuntime{known: true}
		for _, snapshot := range snapshots {
			if !matchesServiceProcess(service, serviceDir, snapshot) {
				continue
			}
			best.running = true
			best.instances++
			if shouldPreferProcess(snapshot, best.process) {
				best.process = snapshot
			}
		}
		runtime[i] = best
	}
	return runtime
}

func matchesServiceProcess(service Service, serviceDir string, snapshot processSnapshot) bool {
	processDir := canonicalRuntimePath(snapshot.Cwd)
	if processDir == "" || processDir != serviceDir {
		return false
	}

	signature := strings.ToLower(strings.Join([]string{snapshot.Name, snapshot.Cmdline, snapshot.Exe}, " "))
	if strings.TrimSpace(service.Command) == "" {
		return strings.Contains(signature, "bun") || strings.Contains(signature, "node") || strings.Contains(signature, "npm")
	}
	if strings.Contains(strings.ToLower(service.Command), "bun") {
		return strings.Contains(signature, "bun")
	}
	return strings.Contains(signature, "node") || strings.Contains(signature, "npm")
}

func shouldPreferProcess(candidate, current processSnapshot) bool {
	if current.PID == 0 {
		return true
	}
	if candidate.CreatedAt == current.CreatedAt {
		return candidate.PID < current.PID
	}
	return candidate.CreatedAt > current.CreatedAt
}

func servicePath(project Project, service Service) string {
	if filepath.IsAbs(service.Path) {
		return service.Path
	}
	switch service.Path {
	case "", ".":
		return project.Path
	default:
		return filepath.Join(project.Path, service.Path)
	}
}

func canonicalRuntimePath(path string) string {
	if path == "" {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

func serviceDetailsItems(project Project, service Service, runtime serviceRuntime) []string {
	items := []string{
		"Project: " + project.Name,
		"Service: " + service.Name,
		"Path: " + servicePath(project, service),
		"Command: " + service.Command,
	}
	if !runtime.known || !runtime.running {
		return append(items,
			"Status: not running",
			"Instances: 0",
		)
	}

	items = append(items,
		"Status: running",
		fmt.Sprintf("Instances: %d", runtime.instances),
		fmt.Sprintf("PID: %d", runtime.process.PID),
		"Process cwd: "+fallbackValue(runtime.process.Cwd, "unavailable"),
		"Executable: "+fallbackValue(runtime.process.Exe, "unavailable"),
		"Proc status: "+fallbackValue(runtime.process.Status, "unavailable"),
		"Memory RSS: "+formatBytes(runtime.process.RSS),
		"Started: "+formatProcessStart(runtime.process.CreatedAt),
	)
	return items
}

func serviceLogItems(project Project, service Service, runtime serviceRuntime) []string {
	if !runtime.known || !runtime.running {
		return []string{
			"○ Service is not running.",
			"Expected path: " + servicePath(project, service),
			"Expected command: " + fallbackValue(service.Command, "unavailable"),
		}
	}

	return []string{
		fmt.Sprintf("● Detected PID %d for %s.", runtime.process.PID, service.Name),
		"Command line: " + fallbackValue(runtime.process.Cmdline, "unavailable"),
		"Working directory: " + fallbackValue(runtime.process.Cwd, "unavailable"),
		"Live logs are unavailable for externally detected processes.",
		"Hangar can show runtime state here, but it cannot attach to arbitrary existing stdout streams cross-platform.",
	}
}

func fallbackValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func formatBytes(value uint64) string {
	if value == 0 {
		return "0 B"
	}

	units := []string{"B", "KB", "MB", "GB", "TB"}
	size := float64(value)
	unit := 0
	for size >= 1024 && unit < len(units)-1 {
		size /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d %s", value, units[unit])
	}
	return fmt.Sprintf("%.1f %s", size, units[unit])
}

func formatProcessStart(createdAt int64) string {
	if createdAt == 0 {
		return "unavailable"
	}
	return time.UnixMilli(createdAt).Format(time.RFC3339)
}
