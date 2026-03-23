package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
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
	processes []processSnapshot
}

type serviceTransition struct {
	targetRunning bool
	polls         int
}

const maxServiceTransitionPolls = 5

func (s serviceTransition) label() string {
	if s.targetRunning {
		return "starting"
	}
	return "stopping"
}

type serviceControlMsg struct {
	projectIndex int
	serviceKey   string
	startedPID   int32
	err          error
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
var startServiceProcess = runServiceStart
var stopServiceProcesses = runServiceStop
var ownedProcessTreePIDs = processTreePIDs
var terminateProcessByPID = terminateProcess
var parentPIDForProcess = parentPID

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
			best.processes = append(best.processes, snapshot)
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

func serviceDetailsItems(project Project, service Service, runtime serviceRuntime, transition *serviceTransition) []string {
	items := []string{
		"Project: " + project.Name,
		"Service: " + service.Name,
		"Path: " + servicePath(project, service),
		"Command: " + service.Command,
	}
	if transition != nil {
		items = append(items,
			"Status: "+transition.label(),
			fmt.Sprintf("Instances: %d", runtime.instances),
			"Hangar is waiting for runtime confirmation before allowing another start/stop toggle.",
		)
		if runtime.running {
			items = append(items,
				fmt.Sprintf("PID: %d", runtime.process.PID),
				"Process cwd: "+fallbackValue(runtime.process.Cwd, "unavailable"),
				"Executable: "+fallbackValue(runtime.process.Exe, "unavailable"),
				"Proc status: "+fallbackValue(runtime.process.Status, "unavailable"),
				"Memory RSS: "+formatBytes(runtime.process.RSS),
				"Started: "+formatProcessStart(runtime.process.CreatedAt),
			)
		}
		return items
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

func serviceLogItems(project Project, service Service, runtime serviceRuntime, transition *serviceTransition) []string {
	if transition != nil {
		lines := []string{
			fmt.Sprintf("◔ Hangar is %s %s.", transition.label(), service.Name),
			"Expected path: " + servicePath(project, service),
			"Expected command: " + fallbackValue(service.Command, "unavailable"),
		}
		if runtime.running {
			lines = append(lines,
				fmt.Sprintf("Current PID: %d", runtime.process.PID),
				"Hangar will unlock the service once runtime polling confirms the requested state.",
			)
		}
		return lines
	}
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

func startServiceCmd(projectIndex int, project Project, service Service) tea.Cmd {
	return func() tea.Msg {
		pid, err := startServiceProcess(project, service)
		return serviceControlMsg{
			projectIndex: projectIndex,
			serviceKey:   serviceKey(project, service),
			startedPID:   pid,
			err:          err,
		}
	}
}

func stopServiceCmd(projectIndex int, project Project, service Service, runtime serviceRuntime, ownedPID int32) tea.Cmd {
	return func() tea.Msg {
		return serviceControlMsg{
			projectIndex: projectIndex,
			serviceKey:   serviceKey(project, service),
			err:          stopServiceProcesses(runtime, ownedPID),
		}
	}
}

func serviceKey(project Project, service Service) string {
	return strings.Join([]string{project.Path, service.Path, service.Name}, "\x00")
}

func runServiceStart(project Project, service Service) (int32, error) {
	if strings.TrimSpace(service.Command) == "" {
		return 0, fmt.Errorf("service %q has no configured start command", service.Name)
	}

	dir := servicePath(project, service)
	info, err := os.Stat(dir)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("service path %q is not a directory", dir)
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer devNull.Close()

	cmd := startShellCommand(service.Command)
	cmd.Dir = dir
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull

	if err := cmd.Start(); err != nil {
		return 0, err
	}
	if err := cmd.Process.Release(); err != nil {
		return 0, err
	}
	return int32(cmd.Process.Pid), nil
}

func startShellCommand(command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/C", command)
	}
	return exec.Command("sh", "-lc", "exec "+command)
}

func runServiceStop(runtime serviceRuntime, ownedPID int32) error {
	pidSet := map[int32]struct{}{}
	for _, snapshot := range runtime.processes {
		if snapshot.PID > 0 {
			pidSet[snapshot.PID] = struct{}{}
		}
	}
	if runtime.process.PID > 0 {
		pidSet[runtime.process.PID] = struct{}{}
	}
	if len(pidSet) == 0 {
		return errors.New("no running process matched the selected service")
	}

	pids := make([]int32, 0, len(pidSet))
	for pid := range pidSet {
		pids = append(pids, pid)
	}
	sort.Slice(pids, func(i, j int) bool { return pids[i] < pids[j] })

	targetPIDs := pids
	if ownedPID > 0 {
		treePIDs := ownedProcessTreePIDs(ownedPID)
		targetPIDs = intersectPIDs(pids, treePIDs)
		if len(targetPIDs) == 0 {
			for _, pid := range pids {
				if pid == ownedPID {
					targetPIDs = []int32{ownedPID}
					break
				}
			}
		}
		if len(targetPIDs) == 0 {
			if len(pids) == 1 {
				targetPIDs = pids
			} else {
				return fmt.Errorf("refusing to stop service: tracked pid %d is no longer among %d matched processes", ownedPID, len(pids))
			}
		}
	}
	if ownedPID == 0 && len(targetPIDs) > 1 {
		if !isSingleMatchedTree(targetPIDs) {
			return fmt.Errorf("refusing to stop service: matched %d processes and none were started by Hangar", len(targetPIDs))
		}
	}

	errs := make([]error, 0)
	for _, pid := range targetPIDs {
		if err := terminateProcessByPID(pid); err != nil {
			errs = append(errs, fmt.Errorf("stop pid %d: %w", pid, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func processTreePIDs(rootPID int32) map[int32]struct{} {
	tree := map[int32]struct{}{rootPID: {}}
	queue := []int32{rootPID}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]

		proc, err := process.NewProcess(pid)
		if err != nil {
			continue
		}
		children, err := proc.Children()
		if err != nil {
			continue
		}
		for _, child := range children {
			if child == nil {
				continue
			}
			if _, seen := tree[child.Pid]; seen {
				continue
			}
			tree[child.Pid] = struct{}{}
			queue = append(queue, child.Pid)
		}
	}
	return tree
}

func terminateProcess(pid int32) error {
	proc, err := os.FindProcess(int(pid))
	if err != nil {
		return err
	}
	return proc.Kill()
}

func parentPID(pid int32) (int32, error) {
	proc, err := process.NewProcess(pid)
	if err != nil {
		return 0, err
	}
	return proc.Ppid()
}

func intersectPIDs(pids []int32, allowed map[int32]struct{}) []int32 {
	out := make([]int32, 0, len(pids))
	for _, pid := range pids {
		if _, ok := allowed[pid]; ok {
			out = append(out, pid)
		}
	}
	return out
}

func isSingleMatchedTree(pids []int32) bool {
	if len(pids) <= 1 {
		return true
	}

	pidSet := map[int32]struct{}{}
	for _, pid := range pids {
		pidSet[pid] = struct{}{}
	}

	roots := map[int32]struct{}{}
	for _, pid := range pids {
		root := pid
		current := pid
		for {
			parentPID, err := parentPIDForProcess(current)
			if err != nil {
				break
			}
			if _, ok := pidSet[parentPID]; !ok {
				break
			}
			root = parentPID
			current = parentPID
		}
		roots[root] = struct{}{}
		if len(roots) > 1 {
			return false
		}
	}
	return true
}

func runtimeContainsPID(runtime serviceRuntime, pid int32) bool {
	if runtime.process.PID == pid {
		return true
	}
	for _, snapshot := range runtime.processes {
		if snapshot.PID == pid {
			return true
		}
	}
	return false
}
