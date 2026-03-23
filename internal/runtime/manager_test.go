package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagerStartStopPersistsRuntimeAndLogs(t *testing.T) {
	rootDir := t.TempDir()
	projectDir := t.TempDir()

	mgr, err := NewManager(rootDir)
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}

	svc := ServiceConfig{
		ProjectName: "Demo",
		ProjectPath: projectDir,
		Name:        "api",
		Path:        ".",
		Command:     `python3 -c "import sys,time; print('boot'); sys.stdout.flush(); time.sleep(30)"`,
	}

	rt, err := mgr.StartService(context.Background(), svc)
	if err != nil {
		t.Fatalf("StartService returned error: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = mgr.StopService(ctx, svc)
	})

	if rt.PID <= 0 {
		t.Fatalf("expected pid > 0, got %d", rt.PID)
	}
	if _, err := os.Stat(mgr.LogPath(svc)); err != nil {
		t.Fatalf("expected log file to exist: %v", err)
	}
	if _, err := os.Stat(mgr.RuntimePath(svc)); err != nil {
		t.Fatalf("expected runtime file to exist: %v", err)
	}

	waitFor(t, 4*time.Second, func() bool {
		data, err := os.ReadFile(mgr.LogPath(svc))
		return err == nil && strings.Contains(string(data), "boot")
	})

	current, err := mgr.GetRuntime(svc)
	if err != nil {
		t.Fatalf("GetRuntime returned error: %v", err)
	}
	if !mgr.IsRunning(current) {
		t.Fatalf("expected service to be running, got %#v", current)
	}
	if current.LogPath != mgr.LogPath(svc) {
		t.Fatalf("LogPath = %q, want %q", current.LogPath, mgr.LogPath(svc))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := mgr.StopService(ctx, svc); err != nil {
		t.Fatalf("StopService returned error: %v", err)
	}

	waitFor(t, 4*time.Second, func() bool {
		refreshed, err := mgr.GetRuntime(svc)
		return err == nil && !mgr.IsRunning(refreshed) && refreshed.Status == "stopped"
	})

	data, err := os.ReadFile(mgr.LogPath(svc))
	if err != nil {
		t.Fatalf("ReadFile(log) returned error: %v", err)
	}
	contents := string(data)
	if !strings.Contains(contents, "starting service api") {
		t.Fatalf("expected start marker in log, got %q", contents)
	}
	if !strings.Contains(contents, "stopping service api") {
		t.Fatalf("expected stop marker in log, got %q", contents)
	}
}

func TestProcessMatchesRuntimeUsesCreateTime(t *testing.T) {
	createdAt, err := processCreateTime(os.Getpid())
	if err != nil {
		t.Fatalf("processCreateTime returned error: %v", err)
	}
	if !processMatchesRuntime(ServiceRuntime{PID: os.Getpid(), CreatedAt: createdAt}) {
		t.Fatal("expected matching pid/create time to be treated as running")
	}
	if processMatchesRuntime(ServiceRuntime{PID: os.Getpid(), CreatedAt: createdAt + 1}) {
		t.Fatal("expected mismatched create time to invalidate the runtime")
	}
}

func TestManagerNamespacesPathsPerProject(t *testing.T) {
	rootDir := t.TempDir()
	mgr, err := NewManager(rootDir)
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}

	one := ServiceConfig{ProjectName: "App One", ProjectPath: filepath.Join(rootDir, "one"), Name: "api", Path: "."}
	two := ServiceConfig{ProjectName: "App Two", ProjectPath: filepath.Join(rootDir, "two"), Name: "api", Path: "."}

	if got, want := mgr.LogPath(one), mgr.LogPath(two); got == want {
		t.Fatalf("expected namespaced log paths, both were %q", got)
	}
	if got, want := mgr.RuntimePath(one), mgr.RuntimePath(two); got == want {
		t.Fatalf("expected namespaced runtime paths, both were %q", got)
	}
}

func waitFor(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}
