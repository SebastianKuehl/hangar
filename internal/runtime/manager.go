package runtime

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	stdruntime "runtime"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

const (
	logsDirName = "logs"
	runDirName  = "run"
)

type ServiceConfig struct {
	ProjectName string
	ProjectPath string
	Name        string
	Path        string
	Command     string
	ComposeFile string // non-empty for docker-compose services; used to disambiguate same-named services across files
}

type ServiceRuntime struct {
	ID          string     `json:"id"`
	ProjectName string     `json:"project_name"`
	ProjectPath string     `json:"project_path"`
	Name        string     `json:"name"`
	Path        string     `json:"path"`
	WorkDir     string     `json:"work_dir"`
	PID         int        `json:"pid"`
	CreatedAt   int64      `json:"created_at,omitempty"`
	Command     string     `json:"command"`
	LogPath     string     `json:"log_path"`
	StartedAt   time.Time  `json:"started_at"`
	Status      string     `json:"status"`
	ExitCode    *int       `json:"exit_code,omitempty"`
	StoppedAt   *time.Time `json:"stopped_at,omitempty"`
}

type Manager struct {
	rootDir string
	logsDir string
	runDir  string
}

func NewManager(rootDir string) (*Manager, error) {
	if strings.TrimSpace(rootDir) == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		rootDir = filepath.Join(homeDir, "hangar")
	}

	mgr := &Manager{
		rootDir: rootDir,
		logsDir: filepath.Join(rootDir, logsDirName),
		runDir:  filepath.Join(rootDir, runDirName),
	}
	if err := mgr.EnsureLayout(); err != nil {
		return nil, err
	}
	return mgr, nil
}

func (m *Manager) RootDir() string {
	return m.rootDir
}

func (m *Manager) EnsureLayout() error {
	for _, dir := range []string{m.rootDir, m.logsDir, m.runDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) LogPath(svc ServiceConfig) string {
	return filepath.Join(m.logsDir, serviceSlug(svc)+".log")
}

func (m *Manager) RuntimePath(svc ServiceConfig) string {
	return filepath.Join(m.runDir, serviceSlug(svc)+".json")
}

func (m *Manager) StartService(ctx context.Context, svc ServiceConfig) (ServiceRuntime, error) {
	if err := m.EnsureLayout(); err != nil {
		return ServiceRuntime{}, err
	}
	if strings.TrimSpace(svc.Command) == "" {
		return ServiceRuntime{}, fmt.Errorf("service %q has no configured start command", svc.Name)
	}
	if err := ctx.Err(); err != nil {
		return ServiceRuntime{}, err
	}

	workDir, err := serviceWorkDir(svc)
	if err != nil {
		return ServiceRuntime{}, err
	}
	if existing, err := m.GetRuntime(svc); err == nil && m.IsRunning(existing) {
		return ServiceRuntime{}, fmt.Errorf("service %q is already running with pid %d", svc.Name, existing.PID)
	}

	logPath := m.LogPath(svc)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return ServiceRuntime{}, err
	}
	defer logFile.Close()

	writeLogMarker(logFile, fmt.Sprintf("starting service %s", svc.Name))

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return ServiceRuntime{}, err
	}
	defer devNull.Close()

	cmd := startShellCommand(svc.Command)
	cmd.Dir = workDir
	cmd.Stdin = devNull
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	prepareDetachedCommand(cmd)

	if err := cmd.Start(); err != nil {
		writeLogMarker(logFile, fmt.Sprintf("failed to start service %s: %v", svc.Name, err))
		return ServiceRuntime{}, err
	}

	createdAt, _ := processCreateTime(cmd.Process.Pid)
	rt := ServiceRuntime{
		ID:          serviceSlug(svc),
		ProjectName: svc.ProjectName,
		ProjectPath: svc.ProjectPath,
		Name:        svc.Name,
		Path:        svc.Path,
		WorkDir:     workDir,
		PID:         cmd.Process.Pid,
		CreatedAt:   createdAt,
		Command:     svc.Command,
		LogPath:     logPath,
		StartedAt:   time.Now().UTC(),
		Status:      "running",
	}
	if err := m.writeRuntime(rt); err != nil {
		_ = terminateManagedProcess(rt.PID)
		return ServiceRuntime{}, err
	}
	if err := cmd.Process.Release(); err != nil {
		return ServiceRuntime{}, err
	}
	return rt, nil
}

func (m *Manager) StopService(ctx context.Context, svc ServiceConfig) error {
	rt, err := m.GetRuntime(svc)
	if err != nil {
		return err
	}

	logFile, logErr := os.OpenFile(rt.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if logErr == nil {
		defer logFile.Close()
		writeLogMarker(logFile, fmt.Sprintf("stopping service %s", rt.Name))
	}

	if rt.PID > 0 && processExists(rt.PID) && !processMatchesRuntime(rt) {
		stalePID := rt.PID
		now := time.Now().UTC()
		rt.Status = "stopped"
		rt.StoppedAt = &now
		rt.PID = 0
		rt.CreatedAt = 0
		if err := m.writeRuntime(rt); err != nil {
			return err
		}
		if logErr == nil {
			writeLogMarker(logFile, fmt.Sprintf("refusing to stop service %s: pid %d no longer matches recorded process identity", rt.Name, stalePID))
		}
		return fmt.Errorf("refusing to stop service %q: pid %d no longer matches the recorded process identity", rt.Name, stalePID)
	}

	if m.IsRunning(rt) {
		if err := terminateManagedProcess(rt.PID); err != nil {
			if logErr == nil {
				writeLogMarker(logFile, fmt.Sprintf("failed to stop service %s: %v", rt.Name, err))
			}
			return err
		}

		deadline := time.Now().Add(5 * time.Second)
		for processMatchesRuntime(rt) && time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(150 * time.Millisecond):
			}
		}
		if processMatchesRuntime(rt) {
			if err := forceKillManagedProcess(rt.PID); err != nil {
				if logErr == nil {
					writeLogMarker(logFile, fmt.Sprintf("failed to force stop service %s: %v", rt.Name, err))
				}
				return err
			}
		}
	}

	now := time.Now().UTC()
	rt.Status = "stopped"
	rt.StoppedAt = &now
	rt.PID = 0
	rt.CreatedAt = 0
	if err := m.writeRuntime(rt); err != nil {
		return err
	}
	if logErr == nil {
		writeLogMarker(logFile, fmt.Sprintf("stopped service %s", rt.Name))
	}
	return nil
}

func (m *Manager) GetRuntime(svc ServiceConfig) (ServiceRuntime, error) {
	data, err := os.ReadFile(m.RuntimePath(svc))
	if err != nil {
		return ServiceRuntime{}, err
	}

	var rt ServiceRuntime
	if err := json.Unmarshal(data, &rt); err != nil {
		return ServiceRuntime{}, err
	}
	return m.refreshRuntime(rt)
}

func (m *Manager) ListRuntimes() ([]ServiceRuntime, error) {
	if err := m.EnsureLayout(); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(m.runDir)
	if err != nil {
		return nil, err
	}

	runtimes := make([]ServiceRuntime, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.runDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var rt ServiceRuntime
		if err := json.Unmarshal(data, &rt); err != nil {
			return nil, err
		}
		refreshed, err := m.refreshRuntime(rt)
		if err != nil {
			return nil, err
		}
		runtimes = append(runtimes, refreshed)
	}
	return runtimes, nil
}

func (m *Manager) IsRunning(rt ServiceRuntime) bool {
	return rt.PID > 0 && processMatchesRuntime(rt)
}

func (m *Manager) refreshRuntime(rt ServiceRuntime) (ServiceRuntime, error) {
	status := "stopped"
	if rt.PID > 0 && processMatchesRuntime(rt) {
		status = "running"
	}
	if rt.Status == status {
		return rt, nil
	}

	rt.Status = status
	if status == "stopped" {
		rt.PID = 0
		rt.CreatedAt = 0
		if rt.StoppedAt == nil {
			now := time.Now().UTC()
			rt.StoppedAt = &now
		}
	}
	if err := m.writeRuntime(rt); err != nil {
		return ServiceRuntime{}, err
	}
	return rt, nil
}

func (m *Manager) writeRuntime(rt ServiceRuntime) error {
	if err := m.EnsureLayout(); err != nil {
		return err
	}

	data, err := json.MarshalIndent(rt, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(m.runDir, rt.ID+".json")
	tmpFile, err := os.CreateTemp(m.runDir, rt.ID+"-*.json.tmp")
	if err != nil {
		return err
	}
	tmpName := tmpFile.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func serviceWorkDir(svc ServiceConfig) (string, error) {
	path := svc.Path
	if path == "" {
		path = "."
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(svc.ProjectPath, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("service path %q is not a directory", path)
	}
	return filepath.Clean(path), nil
}

func startShellCommand(command string) *exec.Cmd {
	if stdruntime.GOOS == "windows" {
		return exec.Command("cmd", "/C", command)
	}
	return exec.Command("sh", "-lc", "exec "+command)
}

func writeLogMarker(file *os.File, message string) {
	_, _ = fmt.Fprintf(file, "=== [%s] %s ===\n", time.Now().UTC().Format(time.RFC3339), message)
}

func serviceSlug(svc ServiceConfig) string {
	prefix := sanitizeSlugPart(svc.ProjectName) + "--" + sanitizeSlugPart(svc.Name)
	prefix = strings.Trim(prefix, "-")
	if prefix == "" {
		prefix = "service"
	}
	hashInput := strings.Join([]string{svc.ProjectPath, svc.Path, svc.Name, svc.ProjectName, svc.ComposeFile}, "\x00")
	digest := sha1.Sum([]byte(hashInput))
	return prefix + "-" + hex.EncodeToString(digest[:4])
}

func sanitizeSlugPart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "service"
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func RuntimeForStoppedService(mgr *Manager, svc ServiceConfig) ServiceRuntime {
	workDir, _ := serviceWorkDir(svc)
	return ServiceRuntime{
		ID:          serviceSlug(svc),
		ProjectName: svc.ProjectName,
		ProjectPath: svc.ProjectPath,
		Name:        svc.Name,
		Path:        svc.Path,
		WorkDir:     workDir,
		Command:     svc.Command,
		LogPath:     mgr.LogPath(svc),
		Status:      "stopped",
	}
}

func IsNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}

func processMatchesRuntime(rt ServiceRuntime) bool {
	if !processExists(rt.PID) {
		return false
	}
	if rt.CreatedAt == 0 {
		return true
	}
	createdAt, err := processCreateTime(rt.PID)
	if err != nil {
		return false
	}
	return createdAt == rt.CreatedAt
}

func processCreateTime(pid int) (int64, error) {
	proc, err := process.NewProcess(int32(pid))
	if err != nil {
		return 0, err
	}
	return proc.CreateTime()
}
