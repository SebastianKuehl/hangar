package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNormalizeProjectPath(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	projectDir := filepath.Join(homeDir, "workspace", "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	got, err := normalizeProjectPath("~/workspace/demo")
	if err != nil {
		t.Fatalf("normalizeProjectPath returned error: %v", err)
	}
	if got != projectDir {
		t.Fatalf("normalizeProjectPath = %q, want %q", got, projectDir)
	}
}

func TestNormalizeServicePathUsesProjectBase(t *testing.T) {
	projectDir := t.TempDir()
	serviceDir := filepath.Join(projectDir, "services", "api")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		t.Fatalf("mkdir service: %v", err)
	}

	got, err := normalizeServicePath(filepath.Join("services", "api"), projectDir)
	if err != nil {
		t.Fatalf("normalizeServicePath returned error: %v", err)
	}

	want := filepath.Join("services", "api")
	if got != want {
		t.Fatalf("normalizeServicePath = %q, want %q", got, want)
	}
}

func TestDiscoverServices(t *testing.T) {
	projectDir := t.TempDir()

	writePackageJSON(t, filepath.Join(projectDir, "package.json"), `{"name":"root-app","scripts":{"start":"node server.js"}}`)
	writePackageJSON(t, filepath.Join(projectDir, "apps", "api", "package.json"), `{"name":"api","scripts":{"start":"node api.js"}}`)
	writePackageJSON(t, filepath.Join(projectDir, "apps", "web", "package.json"), `{"name":"web","packageManager":"bun@1.2.0","scripts":{"start":"bun run dev"}}`)
	writePackageJSON(t, filepath.Join(projectDir, "apps", "broken", "package.json"), `{`)
	writePackageJSON(t, filepath.Join(projectDir, "packages", "shared", "package.json"), `{"name":"shared","scripts":{"build":"tsc"}}`)
	writePackageJSON(t, filepath.Join(projectDir, "node_modules", "ignore-me", "package.json"), `{"name":"ignored","scripts":{"start":"node index.js"}}`)

	services, err := discoverServices(projectDir)
	if err != nil {
		t.Fatalf("discoverServices returned error: %v", err)
	}

	want := []Service{
		{Name: "root-app", Path: ".", Command: "npm run start"},
		{Name: "api", Path: filepath.Join("apps", "api"), Command: "npm run start"},
		{Name: "web", Path: filepath.Join("apps", "web"), Command: "bun run start"},
	}
	if !reflect.DeepEqual(services, want) {
		t.Fatalf("discoverServices = %#v, want %#v", services, want)
	}
}

func TestAddProjectDiscoversServicesAndPersistsConfig(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("HOME", configHome)

	workingDir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		if chdirErr := os.Chdir(oldWD); chdirErr != nil {
			t.Fatalf("restore wd: %v", chdirErr)
		}
	})
	if err := os.Chdir(workingDir); err != nil {
		t.Fatalf("chdir working dir: %v", err)
	}

	projectDir := filepath.Join(workingDir, "demo")
	writePackageJSON(t, filepath.Join(projectDir, "package.json"), `{"name":"demo-root","scripts":{"start":"node server.js"}}`)
	writePackageJSON(t, filepath.Join(projectDir, "apps", "web", "package.json"), `{"name":"web","packageManager":"bun@1.1.0","scripts":{"start":"bun run dev"}}`)

	cfg, err := addProject("Demo", "demo")
	if err != nil {
		t.Fatalf("addProject returned error: %v", err)
	}

	if len(cfg.Projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(cfg.Projects))
	}

	project := cfg.Projects[0]
	gotProjectPath := canonicalPath(t, project.Path)
	wantProjectPath := canonicalPath(t, projectDir)
	if gotProjectPath != wantProjectPath {
		t.Fatalf("project.Path = %q, want %q", gotProjectPath, wantProjectPath)
	}

	wantServices := []Service{
		{Name: "demo-root", Path: ".", Command: "npm run start"},
		{Name: "web", Path: filepath.Join("apps", "web"), Command: "bun run start"},
	}
	if !reflect.DeepEqual(project.Services, wantServices) {
		t.Fatalf("project.Services = %#v, want %#v", project.Services, wantServices)
	}

	reloaded, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if !reflect.DeepEqual(reloaded, cfg) {
		t.Fatalf("loadConfig() = %#v, want %#v", reloaded, cfg)
	}
}

func TestLoadConfigFallsBackToBackup(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("HOME", configHome)

	configPath, err := configPath()
	if err != nil {
		t.Fatalf("configPath returned error: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	backupContents := "projects:\n  - name: Demo\n    path: /tmp/demo\n"
	if err := os.WriteFile(configPath+".bak", []byte(backupContents), 0o644); err != nil {
		t.Fatalf("write backup config: %v", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}

	want := Config{
		Projects: []Project{
			{Name: "Demo", Path: "/tmp/demo"},
		},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("loadConfig() = %#v, want %#v", cfg, want)
	}
}

func TestReplaceFileWindowsUsesBackupSafely(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "projects.yaml")
	tmpPath := filepath.Join(dir, "projects.tmp")

	if err := os.WriteFile(configPath+".bak", []byte("old"), 0o644); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	if err := os.WriteFile(tmpPath, []byte("new"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	if err := replaceFileWindows(tmpPath, configPath); err != nil {
		t.Fatalf("replaceFileWindows returned error: %v", err)
	}

	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("config contents = %q, want %q", got, "new")
	}

	if _, err := os.Stat(configPath + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("expected backup to be removed after successful replace, got err=%v", err)
	}
}

func writePackageJSON(t *testing.T, path, contents string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func canonicalPath(t *testing.T, path string) string {
	t.Helper()

	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(resolvedPath)
}
