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

func TestNormalizeProjectPathAllowsEmptyValue(t *testing.T) {
	got, err := normalizeProjectPath("")
	if err != nil {
		t.Fatalf("normalizeProjectPath returned error: %v", err)
	}
	if got != "" {
		t.Fatalf("normalizeProjectPath = %q, want empty string", got)
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

func TestAddProjectAllowsEmptyPathWithoutDiscovery(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("HOME", configHome)

	cfg, err := addProject("Distributed", "")
	if err != nil {
		t.Fatalf("addProject returned error: %v", err)
	}

	if len(cfg.Projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(cfg.Projects))
	}

	project := cfg.Projects[0]
	want := Project{Name: "Distributed"}
	if !reflect.DeepEqual(project, want) {
		t.Fatalf("project = %#v, want %#v", project, want)
	}

	reloaded, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if !reflect.DeepEqual(reloaded, cfg) {
		t.Fatalf("loadConfig() = %#v, want %#v", reloaded, cfg)
	}
}

func TestAddServiceInfersCommand(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("HOME", configHome)

	projectDir := t.TempDir()
	serviceDir := filepath.Join(projectDir, "apps", "web")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		t.Fatalf("mkdir service dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(serviceDir, "bun.lock"), []byte{}, 0o644); err != nil {
		t.Fatalf("write bun lock: %v", err)
	}
	if err := saveConfig(Config{
		Projects: []Project{
			{Name: "Demo", Path: projectDir},
		},
	}); err != nil {
		t.Fatalf("saveConfig returned error: %v", err)
	}

	cfg, err := addService(0, "web", filepath.Join("apps", "web"), "")
	if err != nil {
		t.Fatalf("addService returned error: %v", err)
	}

	got := cfg.Projects[0].Services[0]
	want := Service{Name: "web", Path: filepath.Join("apps", "web"), Command: "bun run start"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("service = %#v, want %#v", got, want)
	}
}

func TestAddServiceRequiresPathForPathlessProject(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("HOME", configHome)

	if err := saveConfig(Config{
		Projects: []Project{
			{Name: "Distributed"},
		},
	}); err != nil {
		t.Fatalf("saveConfig returned error: %v", err)
	}

	if _, err := addService(0, "web", "", ""); err == nil {
		t.Fatal("expected addService to reject a blank service path for a pathless project")
	}
}

func TestServiceCommandOptionsUseRuntimeScripts(t *testing.T) {
	projectDir := t.TempDir()
	writePackageJSON(t, filepath.Join(projectDir, "apps", "api", "package.json"), `{"name":"api","scripts":{"dev":"node dev.js","start":"node server.js","test":"vitest"}}`)

	project := Project{Name: "Demo", Path: projectDir}
	got := serviceCommandOptions(project, filepath.Join("apps", "api"), "")
	want := []string{"npm run dev", "npm run start", "npm run test"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("serviceCommandOptions = %#v, want %#v", got, want)
	}
}

func TestServiceCommandOptionsPreserveCurrentCommand(t *testing.T) {
	projectDir := t.TempDir()
	writePackageJSON(t, filepath.Join(projectDir, "apps", "api", "package.json"), `{"name":"api","scripts":{"start":"node server.js"}}`)

	project := Project{Name: "Demo", Path: projectDir}
	got := serviceCommandOptions(project, filepath.Join("apps", "api"), "npm run dev")
	want := []string{"npm run start", "npm run dev"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("serviceCommandOptions = %#v, want %#v", got, want)
	}
}

func TestUpdateProjectPersistsEditedFields(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("HOME", configHome)

	projectDir := filepath.Join(t.TempDir(), "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	if err := saveConfig(Config{
		Projects: []Project{
			{Name: "Old", Path: projectDir, Services: []Service{{Name: "api", Path: "apps/api", Command: "npm run start"}}},
		},
	}); err != nil {
		t.Fatalf("saveConfig returned error: %v", err)
	}

	cfg, err := updateProject(0, "New", projectDir)
	if err != nil {
		t.Fatalf("updateProject returned error: %v", err)
	}

	got := cfg.Projects[0]
	if got.Name != "New" || got.Path != projectDir {
		t.Fatalf("project = %#v, want name/path updated", got)
	}
	if len(got.Services) != 1 || got.Services[0].Name != "api" {
		t.Fatalf("expected existing services to be preserved, got %#v", got.Services)
	}
}

func TestUpdateServicePersistsEditedFields(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("HOME", configHome)

	projectDir := t.TempDir()
	serviceDir := filepath.Join(projectDir, "apps", "api")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		t.Fatalf("mkdir service dir: %v", err)
	}
	writePackageJSON(t, filepath.Join(serviceDir, "package.json"), `{"name":"api","scripts":{"start":"node server.js","dev":"node dev.js"}}`)
	if err := saveConfig(Config{
		Projects: []Project{
			{
				Name: "Demo",
				Path: projectDir,
				Services: []Service{
					{Name: "api", Path: filepath.Join("apps", "api"), Command: "npm run start"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("saveConfig returned error: %v", err)
	}

	cfg, err := updateService(0, 0, "api-renamed", filepath.Join("apps", "api"), "npm run dev")
	if err != nil {
		t.Fatalf("updateService returned error: %v", err)
	}

	want := Service{Name: "api-renamed", Path: filepath.Join("apps", "api"), Command: "npm run dev"}
	if got := cfg.Projects[0].Services[0]; !reflect.DeepEqual(got, want) {
		t.Fatalf("service = %#v, want %#v", got, want)
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
