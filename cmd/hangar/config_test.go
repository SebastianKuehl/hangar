package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
		{Name: "root-app", Path: ".", Command: "npm run start", Runtime: "node"},
		{Name: "api", Path: filepath.Join("apps", "api"), Command: "npm run start", Runtime: "node"},
		{Name: "web", Path: filepath.Join("apps", "web"), Command: "bun run start", Runtime: "bun"},
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
		{Name: "demo-root", Path: ".", Command: "npm run start", Runtime: "node"},
		{Name: "web", Path: filepath.Join("apps", "web"), Command: "bun run start", Runtime: "bun"},
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

func writeFile(t *testing.T, path, contents string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestDiscoverGradleServices(t *testing.T) {
	projectDir := t.TempDir()

	// Root gradle project with gradlew
	writeFile(t, filepath.Join(projectDir, "gradlew"), "#!/bin/sh\nexec gradle \"$@\"")
	if err := os.Chmod(filepath.Join(projectDir, "gradlew"), 0o755); err != nil {
		t.Fatalf("chmod gradlew: %v", err)
	}
	writeFile(t, filepath.Join(projectDir, "build.gradle"), `plugins { id 'java' }`)

	// A sub-module build file (gradlew is in the parent)
	writeFile(t, filepath.Join(projectDir, "api", "build.gradle"), `plugins { id 'java' }`)

	// Should be skipped (inside build output dir)
	writeFile(t, filepath.Join(projectDir, "build", "tmp", "build.gradle"), `plugins { id 'java' }`)

	services, err := discoverGradleServices(projectDir)
	if err != nil {
		t.Fatalf("discoverGradleServices returned error: %v", err)
	}

	if len(services) != 2 {
		t.Fatalf("expected 2 services, got %d: %#v", len(services), services)
	}
	for _, s := range services {
		if s.Runtime != "gradle" {
			t.Errorf("service %q: Runtime = %q, want %q", s.Name, s.Runtime, "gradle")
		}
		if !strings.Contains(s.Command, "gradlew") && !strings.Contains(s.Command, "gradle") {
			t.Errorf("service %q: unexpected Command %q", s.Name, s.Command)
		}
	}
}

func TestDiscoverDockerComposeServices(t *testing.T) {
	projectDir := t.TempDir()

	compose := `
services:
  postgres:
    image: postgres:15
  redis:
    image: redis:7
`
	writeFile(t, filepath.Join(projectDir, "docker-compose.yml"), compose)

	composeDev := `
services:
  nginx:
    image: nginx:latest
`
	writeFile(t, filepath.Join(projectDir, "infra", "docker-compose.dev.yml"), composeDev)

	services, err := discoverDockerComposeServices(projectDir)
	if err != nil {
		t.Fatalf("discoverDockerComposeServices returned error: %v", err)
	}

	if len(services) != 3 {
		t.Fatalf("expected 3 services, got %d: %#v", len(services), services)
	}

	// Services should be sorted by path then composeFile then name.
	rootServices := []Service{}
	infraServices := []Service{}
	for _, s := range services {
		if s.Runtime != "docker-compose" {
			t.Errorf("service %q: Runtime = %q, want docker-compose", s.Name, s.Runtime)
		}
		if s.Path == "." {
			rootServices = append(rootServices, s)
		} else {
			infraServices = append(infraServices, s)
		}
	}
	if len(rootServices) != 2 {
		t.Fatalf("expected 2 root services, got %d", len(rootServices))
	}
	if rootServices[0].Name != "postgres" || rootServices[1].Name != "redis" {
		t.Fatalf("unexpected root service names: %v", []string{rootServices[0].Name, rootServices[1].Name})
	}
	if len(infraServices) != 1 || infraServices[0].Name != "nginx" {
		t.Fatalf("unexpected infra services: %#v", infraServices)
	}
	// Verify command format
	if !strings.Contains(rootServices[0].Command, "docker compose") {
		t.Errorf("postgres command %q does not contain 'docker compose'", rootServices[0].Command)
	}
}

func TestBuildServiceDisplayRowsGroupsByRuntime(t *testing.T) {
	project := Project{
		Services: []Service{
			{Name: "api", Runtime: "node"},
			{Name: "web", Runtime: "bun"},
			{Name: "worker", Runtime: "node"},
			{Name: "api", Path: "services", ComposeFile: "docker-compose.yml", Runtime: "docker-compose"},
			{Name: "postgres", Path: "services", ComposeFile: "docker-compose.yml", Runtime: "docker-compose"},
		},
	}

	rows := buildServiceDisplayRows(project)

	// Should have: 2 separators + 3 headers + 5 services = 10 rows
	if len(rows) != 10 {
		t.Fatalf("expected 10 rows (2 separators + 3 headers + 5 services), got %d", len(rows))
	}

	// First should be node header
	if rows[0].kind != serviceRowGroupHeader || rows[0].runtime != "node" {
		t.Errorf("row 0: expected node group header, got %#v", rows[0])
	}

	// Second and third should be api and worker (node services)
	if rows[1].kind != serviceRowService || rows[1].serviceIndex != 0 {
		t.Errorf("row 1: expected node service (api), got %#v", rows[1])
	}
	if rows[2].kind != serviceRowService || rows[2].serviceIndex != 2 {
		t.Errorf("row 2: expected node service (worker), got %#v", rows[2])
	}

	// Fourth should be separator between node and bun
	if rows[3].kind != serviceRowSeparator {
		t.Errorf("row 3: expected separator, got %#v", rows[3])
	}

	// Fifth should be bun header
	if rows[4].kind != serviceRowGroupHeader || rows[4].runtime != "bun" {
		t.Errorf("row 4: expected bun group header, got %#v", rows[4])
	}

	// Sixth should be web (bun service)
	if rows[5].kind != serviceRowService || rows[5].serviceIndex != 1 {
		t.Errorf("row 5: expected bun service (web), got %#v", rows[5])
	}

	// Seventh should be separator between bun and docker-compose
	if rows[6].kind != serviceRowSeparator {
		t.Errorf("row 6: expected separator, got %#v", rows[6])
	}

	// Eighth should be docker-compose header
	if rows[7].kind != serviceRowGroupHeader || rows[7].runtime != "docker-compose" {
		t.Errorf("row 7: expected docker-compose group header, got %#v", rows[7])
	}
	if rows[7].groupLabel != "docker-compose.yml" {
		t.Errorf("row 7: expected group label 'docker-compose.yml', got %q", rows[7].groupLabel)
	}

	// Ninth and tenth should be the docker-compose services
	if rows[8].kind != serviceRowService || rows[8].serviceIndex != 3 {
		t.Errorf("row 8: expected docker-compose service (api), got %#v", rows[8])
	}
	if rows[9].kind != serviceRowService || rows[9].serviceIndex != 4 {
		t.Errorf("row 9: expected docker-compose service (postgres), got %#v", rows[9])
	}
}

func TestBuildServiceDisplayRowsNoHeaderForSingleRuntime(t *testing.T) {
	project := Project{
		Services: []Service{
			{Name: "api", Runtime: "node"},
			{Name: "web", Runtime: "node"},
		},
	}

	rows := buildServiceDisplayRows(project)

	// No headers when all services share the same runtime
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows (no headers), got %d", len(rows))
	}

	for _, row := range rows {
		if row.kind == serviceRowGroupHeader {
			t.Errorf("expected no group headers for single runtime, found header: %#v", row)
		}
	}
}

func TestServiceItemsWithHeadersIndentsContainers(t *testing.T) {
	project := Project{
		Services: []Service{
			{Name: "api", Runtime: "node"},
			{Name: "web", Runtime: "bun"},
		},
	}

	displayRows := buildServiceDisplayRows(project)
	items := serviceItems(project, []serviceRuntime{
		{known: true, running: true},
		{known: true, running: false},
	}, nil, 0, displayRows)

	// Should have: 1 separator + 2 headers + 2 services = 5 items
	if len(items) != 5 {
		t.Fatalf("expected 5 items (1 separator + 2 headers + 2 services), got %d", len(items))
	}

	// First should be node header
	if !strings.HasPrefix(items[0], " ") || strings.HasPrefix(items[0], "   ") {
		t.Errorf("header should have single space indent: %q", items[0])
	}

	// Second should be api service (triple space indent)
	if !strings.HasPrefix(items[1], "   ") {
		t.Errorf("service should have triple space indent when headers exist: %q", items[1])
	}

	// Third should be separator
	if items[2] != "─" {
		t.Errorf("expected separator '─', got: %q", items[2])
	}

	// Fourth should be bun header
	if !strings.HasPrefix(items[3], " ") || strings.HasPrefix(items[3], "   ") {
		t.Errorf("header should have single space indent: %q", items[3])
	}

	// Fifth should be web service (triple space indent)
	if !strings.HasPrefix(items[4], "   ") {
		t.Errorf("service should have triple space indent when headers exist: %q", items[4])
	}
}

func TestRuntimeGroupLabelFormatsCorrectly(t *testing.T) {
	tests := []struct {
		runtime     string
		composeFile string
		want        string
	}{
		{"node", "", "Node"},
		{"bun", "", "Bun"},
		{"gradle", "", "JVM"},
		{"docker-compose", "docker-compose.yml", "docker-compose.yml"},
		{"docker-compose", "infra/docker-compose.dev.yml", "docker-compose.dev.yml"},
		{"unknown", "", "Unknown"},
		{"", "", "Other"},
	}

	for _, tt := range tests {
		got := runtimeGroupLabel(tt.runtime, tt.composeFile)
		if got != tt.want {
			t.Errorf("runtimeGroupLabel(%q, %q) = %q, want %q", tt.runtime, tt.composeFile, got, tt.want)
		}
	}
}

func TestEffectiveGroupKeyForDockerCompose(t *testing.T) {
	s1 := Service{Runtime: "docker-compose", ComposeFile: "docker-compose.yml"}
	s2 := Service{Runtime: "docker-compose", ComposeFile: "infra/docker-compose.dev.yml"}

	got1 := effectiveGroupKey(s1)
	got2 := effectiveGroupKey(s2)

	// Different compose files should have different group keys
	if got1 == got2 {
		t.Errorf("different compose files should have different group keys: %q vs %q", got1, got2)
	}

	// Should contain the compose file path
	if !strings.Contains(got1, "docker-compose.yml") {
		t.Errorf("group key should contain compose file: %q", got1)
	}
}

func TestRuntimeGroupPriorityOrdersGroups(t *testing.T) {
	tests := []struct {
		groupKey string
		priority int
	}{
		{"node", 0},
		{"bun", 1},
		{"gradle", 2},
		{"java", 2},
		{"docker-compose:docker-compose.yml", 3},
		{"unknown", 4},
	}

	for _, tt := range tests {
		got := runtimeGroupPriority(tt.groupKey)
		if got != tt.priority {
			t.Errorf("runtimeGroupPriority(%q) = %d, want %d", tt.groupKey, got, tt.priority)
		}
	}
}

func TestMoveServiceUpWithinGroup(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("HOME", configHome)

	if err := saveConfig(Config{
		Projects: []Project{
			{
				Name: "Demo",
				Path: "/tmp/demo",
				Services: []Service{
					{Name: "first", Runtime: "node"},
					{Name: "second", Runtime: "node"},
					{Name: "third", Runtime: "node"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("saveConfig returned error: %v", err)
	}

	cfg, err := moveServiceUp(0, 2)
	if err != nil {
		t.Fatalf("moveServiceUp returned error: %v", err)
	}

	names := []string{}
	for _, s := range cfg.Projects[0].Services {
		names = append(names, s.Name)
	}
	want := []string{"first", "third", "second"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("services = %v, want %v", names, want)
	}
}

func TestMoveServiceDownWithinGroup(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("HOME", configHome)

	if err := saveConfig(Config{
		Projects: []Project{
			{
				Name: "Demo",
				Path: "/tmp/demo",
				Services: []Service{
					{Name: "first", Runtime: "node"},
					{Name: "second", Runtime: "node"},
					{Name: "third", Runtime: "node"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("saveConfig returned error: %v", err)
	}

	cfg, err := moveServiceDown(0, 0)
	if err != nil {
		t.Fatalf("moveServiceDown returned error: %v", err)
	}

	names := []string{}
	for _, s := range cfg.Projects[0].Services {
		names = append(names, s.Name)
	}
	want := []string{"second", "first", "third"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("services = %v, want %v", names, want)
	}
}

func TestSwapServicesWithinGroup(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("HOME", configHome)

	if err := saveConfig(Config{
		Projects: []Project{
			{
				Name: "Demo",
				Path: "/tmp/demo",
				Services: []Service{
					{Name: "first", Runtime: "node"},
					{Name: "second", Runtime: "node"},
					{Name: "third", Runtime: "node"},
					{Name: "bun-svc", Runtime: "bun"},
					{Name: "fourth", Runtime: "node"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("saveConfig returned error: %v", err)
	}

	// Swap non-adjacent services in the same group (first and fourth, with bun in between)
	cfg, err := swapServices(0, 0, 4)
	if err != nil {
		t.Fatalf("swapServices returned error: %v", err)
	}

	names := []string{}
	for _, s := range cfg.Projects[0].Services {
		names = append(names, s.Name)
	}
	want := []string{"fourth", "second", "third", "bun-svc", "first"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("services = %v, want %v", names, want)
	}
}

func TestMoveServiceCannotCrossGroups(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("HOME", configHome)

	if err := saveConfig(Config{
		Projects: []Project{
			{
				Name: "Demo",
				Path: "/tmp/demo",
				Services: []Service{
					{Name: "node-service", Runtime: "node"},
					{Name: "bun-service", Runtime: "bun"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("saveConfig returned error: %v", err)
	}

	_, err := moveServiceUp(0, 1)
	if err == nil {
		t.Fatal("expected error when moving bun-service up across groups")
	}

	_, err = moveServiceDown(0, 0)
	if err == nil {
		t.Fatal("expected error when moving node-service down across groups")
	}
}

func TestBuildServiceDisplayRowsSortsGroupsByPriority(t *testing.T) {
	project := Project{
		Services: []Service{
			{Name: "docker-svc", Runtime: "docker-compose", ComposeFile: "docker.yml"},
			{Name: "gradle-svc", Runtime: "gradle"},
			{Name: "bun-svc", Runtime: "bun"},
			{Name: "node-svc", Runtime: "node"},
		},
	}

	rows := buildServiceDisplayRows(project)

	// Find the positions of each group header
	nodePos := -1
	bunPos := -1
	gradlePos := -1
	dockerPos := -1

	for i, row := range rows {
		if row.kind == serviceRowGroupHeader {
			switch row.runtime {
			case "node":
				nodePos = i
			case "bun":
				bunPos = i
			case "gradle":
				gradlePos = i
			case "docker-compose":
				dockerPos = i
			}
		}
	}

	if nodePos < 0 || bunPos < 0 || gradlePos < 0 || dockerPos < 0 {
		t.Fatalf("expected all groups to be present, got node=%d, bun=%d, gradle=%d, docker=%d",
			nodePos, bunPos, gradlePos, dockerPos)
	}

	if nodePos > bunPos {
		t.Errorf("node should come before bun")
	}
	if bunPos > gradlePos {
		t.Errorf("bun should come before gradle")
	}
	if gradlePos > dockerPos {
		t.Errorf("gradle should come before docker")
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
