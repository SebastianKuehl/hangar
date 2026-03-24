package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Service is a single runnable unit that belongs to a project.
type Service struct {
	Name        string `yaml:"name"`
	Path        string `yaml:"path,omitempty"`
	Command     string `yaml:"command,omitempty"`
	Runtime     string `yaml:"runtime,omitempty"`      // "node", "bun", "gradle", "docker-compose"
	ComposeFile string `yaml:"compose_file,omitempty"` // relative path to the docker-compose file (docker-compose services only)
	Ignored     bool   `yaml:"ignored,omitempty"`
}

// Project groups a set of services under a named entry.
type Project struct {
	Name     string    `yaml:"name"`
	Path     string    `yaml:"path,omitempty"`
	Services []Service `yaml:"services,omitempty"`
	Default  bool      `yaml:"default,omitempty"`
}

// Config is the root structure persisted to projects.yaml.
type Config struct {
	Projects []Project `yaml:"projects"`
	BasePath string    `yaml:"basePath,omitempty"`
}

// configPath returns the OS-appropriate path to projects.yaml.
// Uses os.UserConfigDir() for cross-platform correctness:
//   - Linux:   ~/.config/hangar/projects.yaml
//   - macOS:   ~/Library/Application Support/hangar/projects.yaml
//   - Windows: %AppData%\hangar\projects.yaml
func configPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "hangar", "projects.yaml"), nil
}

// loadConfig reads and parses projects.yaml. Returns an empty Config if the
// file does not exist yet.
func loadConfig() (Config, error) {
	path, err := configPath()
	if err != nil {
		return Config{}, err
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		data, err = loadConfigBackup(path)
	}
	if os.IsNotExist(err) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// saveConfig writes cfg to projects.yaml atomically (write to temp file then
// rename) to avoid partial-read races with concurrent loadConfig calls.
func saveConfig(cfg Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	// Write to a temp file in the same directory so the rename is atomic.
	tmp, err := os.CreateTemp(dir, ".projects-*.yaml.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after successful rename

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return replaceFileWindows(tmpName, path)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return nil
}

// addProject appends a new project and persists the config.
func addProject(name, path string) (Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return cfg, err
	}

	project, err := newProject(name, path)
	if err != nil {
		return cfg, err
	}

	cfg.Projects = append(cfg.Projects, project)
	return cfg, saveConfig(cfg)
}

func setBasePath(basePath string) (Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return cfg, err
	}

	normalizedPath, err := normalizeAbsolutePath(basePath, "")
	if err != nil {
		return cfg, err
	}

	cfg.BasePath = normalizedPath
	return cfg, saveConfig(cfg)
}

// updateProject updates an existing project's editable fields and persists.
func updateProject(projectIndex int, name, path string) (Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return cfg, err
	}
	if projectIndex < 0 || projectIndex >= len(cfg.Projects) {
		return cfg, fmt.Errorf("project index %d out of range (have %d projects)", projectIndex, len(cfg.Projects))
	}

	projectPath, err := normalizeProjectPath(path)
	if err != nil {
		return cfg, err
	}
	info, err := os.Stat(projectPath)
	if err != nil {
		return cfg, err
	}
	if !info.IsDir() {
		return cfg, fmt.Errorf("project path %q is not a directory", projectPath)
	}

	cfg.Projects[projectIndex].Name = strings.TrimSpace(name)
	cfg.Projects[projectIndex].Path = projectPath
	return cfg, saveConfig(cfg)
}

// setProjectAsDefault marks the project at the given index as the default project.
func setProjectAsDefault(projectIndex int) (Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return cfg, err
	}
	if projectIndex < 0 || projectIndex >= len(cfg.Projects) {
		return cfg, fmt.Errorf("project index %d out of range (have %d projects)", projectIndex, len(cfg.Projects))
	}

	for i := range cfg.Projects {
		cfg.Projects[i].Default = false
	}
	cfg.Projects[projectIndex].Default = true
	return cfg, saveConfig(cfg)
}

// addService appends a new service to the project at projectIndex and persists.
func addService(projectIndex int, name, path, command string) (Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return cfg, err
	}
	if projectIndex < 0 || projectIndex >= len(cfg.Projects) {
		return cfg, fmt.Errorf("project index %d out of range (have %d projects)", projectIndex, len(cfg.Projects))
	}

	normalizedPath, err := normalizeServicePath(path, cfg.Projects[projectIndex].Path)
	if err != nil {
		return cfg, err
	}

	serviceName := strings.TrimSpace(name)
	if normalizedPath != "" && (serviceName == "" || isWorktreePath(normalizedPath, cfg.Projects[projectIndex].Path)) {
		worktreeName, repoPath := getWorktreeInfo(normalizedPath, cfg.Projects[projectIndex].Path)
		if worktreeName != "" && repoPath != "" {
			serviceName = repoPath + ":" + worktreeName
		} else if serviceName == "" {
			serviceName = filepath.Base(normalizedPath)
		}
	}

	command = strings.TrimSpace(command)
	if command == "" {
		command = inferServiceCommand(cfg.Projects[projectIndex], normalizedPath)
	}

	cfg.Projects[projectIndex].Services = append(
		cfg.Projects[projectIndex].Services,
		Service{Name: serviceName, Path: normalizedPath, Command: command},
	)
	return cfg, saveConfig(cfg)
}

// deleteProject removes the project at projectIndex and persists the config.
func deleteProject(projectIndex int) (Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return cfg, err
	}
	if projectIndex < 0 || projectIndex >= len(cfg.Projects) {
		return cfg, fmt.Errorf("project index %d out of range (have %d projects)", projectIndex, len(cfg.Projects))
	}
	cfg.Projects = append(cfg.Projects[:projectIndex], cfg.Projects[projectIndex+1:]...)
	return cfg, saveConfig(cfg)
}

// deleteService removes the service at serviceIndex within the project at projectIndex and persists the config.
func deleteService(projectIndex, serviceIndex int) (Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return cfg, err
	}
	if projectIndex < 0 || projectIndex >= len(cfg.Projects) {
		return cfg, fmt.Errorf("project index %d out of range (have %d projects)", projectIndex, len(cfg.Projects))
	}
	services := cfg.Projects[projectIndex].Services
	if serviceIndex < 0 || serviceIndex >= len(services) {
		return cfg, fmt.Errorf("service index %d out of range (have %d services)", serviceIndex, len(services))
	}
	cfg.Projects[projectIndex].Services = append(services[:serviceIndex], services[serviceIndex+1:]...)
	return cfg, saveConfig(cfg)
}

// moveServiceUp moves the service at serviceIndex up within its runtime group.
func moveServiceUp(projectIndex, serviceIndex int) (Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return cfg, err
	}
	if projectIndex < 0 || projectIndex >= len(cfg.Projects) {
		return cfg, fmt.Errorf("project index %d out of range", projectIndex)
	}
	services := cfg.Projects[projectIndex].Services
	if serviceIndex < 1 || serviceIndex >= len(services) {
		return cfg, fmt.Errorf("cannot move service at index %d", serviceIndex)
	}
	if effectiveGroupKey(services[serviceIndex]) != effectiveGroupKey(services[serviceIndex-1]) {
		return cfg, fmt.Errorf("cannot move service across runtime groups")
	}
	services[serviceIndex-1], services[serviceIndex] = services[serviceIndex], services[serviceIndex-1]
	return cfg, saveConfig(cfg)
}

// moveServiceDown moves the service at serviceIndex down within its runtime group.
func moveServiceDown(projectIndex, serviceIndex int) (Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return cfg, err
	}
	if projectIndex < 0 || projectIndex >= len(cfg.Projects) {
		return cfg, fmt.Errorf("project index %d out of range", projectIndex)
	}
	services := cfg.Projects[projectIndex].Services
	if serviceIndex < 0 || serviceIndex >= len(services)-1 {
		return cfg, fmt.Errorf("cannot move service at index %d", serviceIndex)
	}
	if effectiveGroupKey(services[serviceIndex]) != effectiveGroupKey(services[serviceIndex+1]) {
		return cfg, fmt.Errorf("cannot move service across runtime groups")
	}
	services[serviceIndex], services[serviceIndex+1] = services[serviceIndex+1], services[serviceIndex]
	return cfg, saveConfig(cfg)
}

// swapServices swaps two services at the given indices in a project.
func swapServices(projectIndex, idxA, idxB int) (Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return cfg, err
	}
	if projectIndex < 0 || projectIndex >= len(cfg.Projects) {
		return cfg, fmt.Errorf("project index %d out of range", projectIndex)
	}
	services := cfg.Projects[projectIndex].Services
	if idxA < 0 || idxA >= len(services) || idxB < 0 || idxB >= len(services) {
		return cfg, fmt.Errorf("service index out of range")
	}
	services[idxA], services[idxB] = services[idxB], services[idxA]
	return cfg, saveConfig(cfg)
}

// toggleServiceIgnored flips the Ignored flag for a service and persists.
func toggleServiceIgnored(projectIndex, serviceIndex int) (Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return cfg, err
	}
	if projectIndex < 0 || projectIndex >= len(cfg.Projects) {
		return cfg, fmt.Errorf("project index %d out of range (have %d projects)", projectIndex, len(cfg.Projects))
	}
	if serviceIndex < 0 || serviceIndex >= len(cfg.Projects[projectIndex].Services) {
		return cfg, fmt.Errorf("service index %d out of range (have %d services)", serviceIndex, len(cfg.Projects[projectIndex].Services))
	}
	cfg.Projects[projectIndex].Services[serviceIndex].Ignored = !cfg.Projects[projectIndex].Services[serviceIndex].Ignored
	return cfg, saveConfig(cfg)
}

// updateService updates an existing service's editable fields and persists.
func updateService(projectIndex, serviceIndex int, name, path, command string, ignored bool) (Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return cfg, err
	}
	if projectIndex < 0 || projectIndex >= len(cfg.Projects) {
		return cfg, fmt.Errorf("project index %d out of range (have %d projects)", projectIndex, len(cfg.Projects))
	}
	if serviceIndex < 0 || serviceIndex >= len(cfg.Projects[projectIndex].Services) {
		return cfg, fmt.Errorf("service index %d out of range (have %d services)", serviceIndex, len(cfg.Projects[projectIndex].Services))
	}

	normalizedPath, err := normalizeServicePath(path, cfg.Projects[projectIndex].Path)
	if err != nil {
		return cfg, err
	}

	command = strings.TrimSpace(command)
	if command == "" {
		command = inferServiceCommand(cfg.Projects[projectIndex], normalizedPath)
	}

	cfg.Projects[projectIndex].Services[serviceIndex] = Service{
		Name:        strings.TrimSpace(name),
		Path:        normalizedPath,
		Command:     command,
		Runtime:     cfg.Projects[projectIndex].Services[serviceIndex].Runtime,
		ComposeFile: cfg.Projects[projectIndex].Services[serviceIndex].ComposeFile,
		Ignored:     ignored,
	}
	return cfg, saveConfig(cfg)
}

type packageManifest struct {
	Name           string            `json:"name"`
	PackageManager string            `json:"packageManager"`
	Scripts        map[string]string `json:"scripts"`
}

func newProject(name, inputPath string) (Project, error) {
	projectPath, err := normalizeProjectPath(inputPath)
	if err != nil {
		return Project{}, err
	}
	if projectPath == "" {
		return Project{Name: name}, nil
	}

	info, err := os.Stat(projectPath)
	if err != nil {
		return Project{}, err
	}
	if !info.IsDir() {
		return Project{}, fmt.Errorf("project path %q is not a directory", projectPath)
	}

	services, err := discoverServices(projectPath)
	if err != nil {
		return Project{}, err
	}

	worktreeServices, err := discoverWorktrees(projectPath)
	if err != nil {
		return Project{}, err
	}
	services = append(services, worktreeServices...)

	return Project{
		Name:     name,
		Path:     projectPath,
		Services: services,
	}, nil
}

func normalizeProjectPath(inputPath string) (string, error) {
	path, err := normalizeAbsolutePath(inputPath, "")
	if err != nil {
		return "", err
	}
	return path, nil
}

func normalizeServicePath(inputPath, projectPath string) (string, error) {
	if strings.TrimSpace(inputPath) == "" {
		return "", nil
	}

	path, err := normalizeAbsolutePath(inputPath, projectPath)
	if err != nil {
		return "", err
	}
	if projectPath == "" {
		return path, nil
	}

	relativePath, err := filepath.Rel(projectPath, path)
	if err != nil {
		return path, nil
	}
	relativePath = filepath.Clean(relativePath)
	if !isWithinBase(relativePath) {
		return path, nil
	}
	return relativePath, nil
}

func normalizeAbsolutePath(inputPath, baseDir string) (string, error) {
	path := normalizePathSeparators(strings.TrimSpace(inputPath))
	if path == "" {
		return "", nil
	}

	expandedPath, err := expandHomeDir(path)
	if err != nil {
		return "", err
	}
	path = expandedPath

	if !filepath.IsAbs(path) {
		if baseDir == "" {
			baseDir, err = os.Getwd()
			if err != nil {
				return "", err
			}
		}
		path = filepath.Join(baseDir, path)
	}

	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolutePath), nil
}

func normalizePathSeparators(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	return filepath.FromSlash(path)
}

func expandHomeDir(path string) (string, error) {
	if path == "" || path[0] != '~' {
		return path, nil
	}
	if path != "~" && path[1] != '/' && path[1] != '\\' {
		return "", fmt.Errorf("unsupported home path %q", path)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return homeDir, nil
	}
	return filepath.Join(homeDir, path[2:]), nil
}

func discoverServices(projectPath string) ([]Service, error) {
	services := []Service{}

	err := filepath.WalkDir(projectPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules":
				if path != projectPath {
					return filepath.SkipDir
				}
			}
			return nil
		}

		if d.Name() != "package.json" {
			return nil
		}

		service, ok, err := serviceFromPackageManifest(projectPath, path)
		if err != nil {
			return err
		}
		if ok {
			services = append(services, service)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	gradleServices, err := discoverGradleServices(projectPath)
	if err != nil {
		return nil, err
	}
	services = append(services, gradleServices...)

	composeServices, err := discoverDockerComposeServices(projectPath)
	if err != nil {
		return nil, err
	}
	services = append(services, composeServices...)

	sort.Slice(services, func(i, j int) bool {
		si, sj := services[i], services[j]
		if si.Path != sj.Path {
			return si.Path < sj.Path
		}
		if si.ComposeFile != sj.ComposeFile {
			return si.ComposeFile < sj.ComposeFile
		}
		return si.Name < sj.Name
	})
	return services, nil
}

func serviceFromPackageManifest(projectPath, manifestPath string) (Service, bool, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return Service{}, false, err
	}

	var manifest packageManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Service{}, false, nil
	}

	startScript, ok := manifest.Scripts["start"]
	if !ok || strings.TrimSpace(startScript) == "" {
		return Service{}, false, nil
	}

	serviceDir := filepath.Dir(manifestPath)
	relativePath, err := filepath.Rel(projectPath, serviceDir)
	if err != nil {
		return Service{}, false, err
	}

	serviceName := strings.TrimSpace(manifest.Name)
	if serviceName == "" {
		serviceName = filepath.Base(serviceDir)
	}

	rt := detectRuntime(serviceDir, manifest)
	return Service{
		Name:    serviceName,
		Path:    filepath.Clean(relativePath),
		Command: startCommand(rt),
		Runtime: rt,
	}, true, nil
}

func detectRuntime(serviceDir string, manifest packageManifest) string {
	if strings.HasPrefix(strings.ToLower(manifest.PackageManager), "bun@") {
		return "bun"
	}

	for _, candidate := range []string{"bun.lock", "bun.lockb"} {
		if _, err := os.Stat(filepath.Join(serviceDir, candidate)); err == nil {
			return "bun"
		}
	}
	return "node"
}

// discoverGradleServices finds build.gradle / build.gradle.kts files and
// creates a service entry for each.  It skips standard Gradle output dirs.
func discoverGradleServices(projectPath string) ([]Service, error) {
	var services []Service
	err := filepath.WalkDir(projectPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", ".gradle", "build", "target":
				if path != projectPath {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if d.Name() != "build.gradle" && d.Name() != "build.gradle.kts" {
			return nil
		}
		service, err := serviceFromGradleBuild(projectPath, path)
		if err != nil {
			return err
		}
		services = append(services, service)
		return nil
	})
	return services, err
}

func serviceFromGradleBuild(projectPath, buildFile string) (Service, error) {
	serviceDir := filepath.Dir(buildFile)
	relativePath, err := filepath.Rel(projectPath, serviceDir)
	if err != nil {
		return Service{}, err
	}
	relativePath = filepath.Clean(relativePath)

	serviceName := filepath.Base(serviceDir)
	if relativePath == "." {
		serviceName = filepath.Base(projectPath)
	}

	executable := gradleExecutable(serviceDir, projectPath)
	return Service{
		Name:    serviceName,
		Path:    relativePath,
		Command: executable + " run",
		Runtime: "gradle",
	}, nil
}

// gradleExecutable walks up from serviceDir to projectPath looking for a
// gradlew (or gradlew.bat on Windows) and returns a path relative to
// serviceDir.  Falls back to "gradle" if none is found.
func gradleExecutable(serviceDir, projectPath string) string {
	execName := "gradlew"
	if runtime.GOOS == "windows" {
		execName = "gradlew.bat"
	}
	dir := serviceDir
	for {
		candidate := filepath.Join(dir, execName)
		if _, err := os.Stat(candidate); err == nil {
			rel, err := filepath.Rel(serviceDir, candidate)
			if err == nil {
				return filepath.ToSlash(rel)
			}
		}
		if dir == projectPath {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "gradle"
}

// dockerComposeSpec is used to parse the top-level services map from a
// docker-compose file.
type dockerComposeSpec struct {
	Services map[string]any `yaml:"services"`
}

// discoverDockerComposeServices finds docker-compose files under projectPath
// and creates one service entry per container service.
func discoverDockerComposeServices(projectPath string) ([]Service, error) {
	var services []Service
	err := filepath.WalkDir(projectPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				if path != projectPath {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !isDockerComposeFile(d.Name()) {
			return nil
		}
		discovered, err := servicesFromComposeFile(projectPath, path)
		if err != nil {
			return nil // skip malformed compose files
		}
		services = append(services, discovered...)
		return nil
	})
	return services, err
}

// shellQuote wraps s in single quotes for safe use in sh -lc commands,
// escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func isDockerComposeFile(name string) bool {
	// Accept docker-compose*.yml/yaml and compose*.yml/yaml to handle variants
	// like docker-compose.dev.yml, docker-compose.prod.yaml, etc.
	for _, prefix := range []string{"docker-compose", "compose"} {
		if strings.HasPrefix(name, prefix) &&
			(strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")) {
			return true
		}
	}
	return false
}

func servicesFromComposeFile(projectPath, composePath string) ([]Service, error) {
	data, err := os.ReadFile(composePath)
	if err != nil {
		return nil, err
	}

	var spec dockerComposeSpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, err
	}
	if len(spec.Services) == 0 {
		return nil, nil
	}

	composeDir := filepath.Dir(composePath)
	relPath, err := filepath.Rel(projectPath, composeDir)
	if err != nil {
		return nil, err
	}
	relPath = filepath.Clean(relPath)

	composeFilename := filepath.Base(composePath)
	relComposePath, err := filepath.Rel(projectPath, composePath)
	if err != nil {
		return nil, err
	}
	relComposePath = filepath.ToSlash(filepath.Clean(relComposePath))

	names := make([]string, 0, len(spec.Services))
	for name := range spec.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	var result []Service
	for _, name := range names {
		result = append(result, Service{
			Name:        name,
			Path:        relPath,
			Command:     "docker compose -f " + shellQuote(composeFilename) + " up " + shellQuote(name),
			Runtime:     "docker-compose",
			ComposeFile: relComposePath,
		})
	}
	return result, nil
}

func startCommand(rt string) string {
	if rt == "bun" {
		return "bun run start"
	}
	return "npm run start"
}

func scriptCommands(scripts map[string]string, runtime string) []string {
	if len(scripts) == 0 {
		return nil
	}

	names := make([]string, 0, len(scripts))
	for name, command := range scripts {
		if strings.TrimSpace(command) != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil
	}

	prefix := "npm run "
	if runtime == "bun" {
		prefix = "bun run "
	}

	commands := make([]string, 0, len(names))
	for _, name := range names {
		commands = append(commands, prefix+name)
	}
	return commands
}

func isWithinBase(relativePath string) bool {
	if relativePath == "." {
		return true
	}
	parentPrefix := ".." + string(filepath.Separator)
	return relativePath != ".." && !strings.HasPrefix(relativePath, parentPrefix)
}

func inferServiceCommand(project Project, servicePath string) string {
	serviceDir := resolveServiceDir(project, servicePath)

	manifestPath := filepath.Join(serviceDir, "package.json")
	if data, err := os.ReadFile(manifestPath); err == nil {
		var manifest packageManifest
		if err := json.Unmarshal(data, &manifest); err == nil {
			return startCommand(detectRuntime(serviceDir, manifest))
		}
	}

	if detectRuntime(serviceDir, packageManifest{}) == "bun" {
		return "bun run start"
	}
	return "npm run start"
}

func serviceCommandOptions(project Project, servicePath, currentCommand string) []string {
	serviceDir := resolveServiceDir(project, servicePath)

	info, err := os.Stat(serviceDir)
	if err != nil || !info.IsDir() {
		return nil
	}

	manifestPath := filepath.Join(serviceDir, "package.json")
	if data, err := os.ReadFile(manifestPath); err == nil {
		var manifest packageManifest
		if err := json.Unmarshal(data, &manifest); err == nil {
			options := scriptCommands(manifest.Scripts, detectRuntime(serviceDir, manifest))
			if len(options) > 0 {
				return appendCommandOption(options, currentCommand)
			}
		}
	}

	return nil
}

func appendCommandOption(options []string, currentCommand string) []string {
	currentCommand = strings.TrimSpace(currentCommand)
	if currentCommand == "" {
		return options
	}
	for _, option := range options {
		if option == currentCommand {
			return options
		}
	}
	return append(options, currentCommand)
}

func resolveServiceDir(project Project, servicePath string) string {
	serviceDir := servicePath
	if !filepath.IsAbs(serviceDir) {
		switch serviceDir {
		case "", ".":
			serviceDir = project.Path
		default:
			serviceDir = filepath.Join(project.Path, serviceDir)
		}
	}
	return serviceDir
}

func replaceFileWindows(tmpName, path string) error {
	backupPath := path + ".bak"

	hadOriginal := false
	if _, err := os.Stat(path); err == nil {
		hadOriginal = true
		if err := os.Remove(backupPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(path, backupPath); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	if err := os.Rename(tmpName, path); err != nil {
		if hadOriginal {
			if restoreErr := os.Rename(backupPath, path); restoreErr != nil {
				return fmt.Errorf("replace %q: %w (restore failed: %v)", path, err, restoreErr)
			}
		}
		return err
	}

	if err := os.Remove(backupPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func loadConfigBackup(path string) ([]byte, error) {
	return os.ReadFile(path + ".bak")
}

func discoverWorktrees(repoPath string) ([]Service, error) {
	var services []Service

	gitDir := filepath.Join(repoPath, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		return nil, nil
	}

	if info.IsDir() {
		worktreesDir := filepath.Join(gitDir, "worktrees")
		entries, err := os.ReadDir(worktreesDir)
		if err != nil {
			return nil, nil
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			worktreeName := entry.Name()
			worktreePath := filepath.Join(repoPath, worktreeName)

			if _, err := os.Stat(worktreePath); err != nil {
				continue
			}

			svc := serviceFromWorktree(repoPath, worktreeName, worktreePath)
			services = append(services, svc)
		}
	} else {
		data, err := os.ReadFile(gitDir)
		if err != nil || !strings.HasPrefix(string(data), "gitdir: ") {
			return nil, nil
		}

		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		for _, line := range lines {
			if !strings.HasPrefix(line, "gitdir: ") {
				continue
			}
			gitDirPath := strings.TrimSpace(line[8:])
			if filepath.IsAbs(gitDirPath) {
				worktreesDir := filepath.Join(gitDirPath, "worktrees")
				entries, err := os.ReadDir(worktreesDir)
				if err != nil {
					continue
				}

				for _, entry := range entries {
					if !entry.IsDir() {
						continue
					}
					worktreeName := entry.Name()
					worktreePath := filepath.Join(repoPath, worktreeName)

					if _, err := os.Stat(worktreePath); err != nil {
						continue
					}

					svc := serviceFromWorktree(repoPath, worktreeName, worktreePath)
					services = append(services, svc)
				}
			}
		}
	}

	sort.Slice(services, func(i, j int) bool {
		return services[i].Name < services[j].Name
	})

	return services, nil
}

func serviceFromWorktree(repoPath, worktreeName, worktreePath string) Service {
	relPath, _ := filepath.Rel(repoPath, worktreePath)
	relPath = filepath.Clean(relPath)

	command := inferServiceCommandFromPath(worktreePath)

	return Service{
		Name:    repoPath + ":" + worktreeName,
		Path:    relPath,
		Command: command,
	}
}

func inferServiceCommandFromPath(servicePath string) string {
	manifestPath := filepath.Join(servicePath, "package.json")
	if data, err := os.ReadFile(manifestPath); err == nil {
		var manifest packageManifest
		if err := json.Unmarshal(data, &manifest); err == nil {
			rt := detectRuntime(servicePath, manifest)
			return startCommand(rt)
		}
	}

	if rt := detectRuntime(servicePath, packageManifest{}); rt == "bun" {
		return "bun run start"
	}
	return "npm run start"
}

func isWorktreePath(servicePath, projectPath string) bool {
	if filepath.IsAbs(servicePath) {
		gitFile := filepath.Join(servicePath, ".git")
		data, err := os.ReadFile(gitFile)
		if err != nil {
			return false
		}
		return strings.HasPrefix(string(data), "gitdir: ")
	}

	if projectPath == "" {
		return false
	}

	absPath := filepath.Join(projectPath, servicePath)
	gitFile := filepath.Join(absPath, ".git")
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return false
	}
	return strings.HasPrefix(string(data), "gitdir: ")
}

func getWorktreeInfo(servicePath, projectPath string) (worktreeName, repoPath string) {
	var absPath string
	if filepath.IsAbs(servicePath) {
		absPath = servicePath
	} else if projectPath != "" {
		absPath = filepath.Join(projectPath, servicePath)
	} else {
		return "", ""
	}

	gitFile := filepath.Join(absPath, ".git")
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return "", ""
	}

	if !strings.HasPrefix(string(data), "gitdir: ") {
		return "", ""
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "gitdir: ") {
			continue
		}
		gitDirPath := strings.TrimSpace(line[8:])
		if !filepath.IsAbs(gitDirPath) {
			continue
		}

		gitDir := filepath.Dir(gitDirPath)
		worktreeName = filepath.Base(gitDirPath)
		if filepath.Base(gitDir) == "worktrees" {
			mainGitDir := filepath.Join(gitDir, "..", "gitdir")
			mainGitDir = filepath.Clean(mainGitDir)
			if mainData, err := os.ReadFile(mainGitDir); err == nil {
				mainPath := strings.TrimSpace(string(mainData))
				repoPath = filepath.Dir(mainPath)
			}
		}
		break
	}

	if repoPath == "" {
		repoPath = filepath.Dir(absPath)
	}
	if worktreeName == "" {
		worktreeName = filepath.Base(absPath)
	}

	return worktreeName, repoPath
}

func discoverProjectsFromBasePath(basePath string) ([]Service, error) {
	if strings.TrimSpace(basePath) == "" {
		return nil, nil
	}

	projectPath, err := normalizeAbsolutePath(basePath, "")
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(projectPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}

	return discoverServices(projectPath)
}

const maxPathSuggestions = 5

func discoverSubdirectories(basePath, filter string) []string {
	if strings.TrimSpace(basePath) == "" {
		return nil
	}

	absPath, err := normalizeAbsolutePath(basePath, "")
	if err != nil {
		return nil
	}

	info, err := os.Stat(absPath)
	if err != nil || !info.IsDir() {
		return nil
	}

	var dirs []string
	filterLower := strings.ToLower(filter)

	err = filepath.WalkDir(absPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if len(dirs) >= maxPathSuggestions {
			return io.EOF
		}

		if !d.IsDir() {
			return nil
		}

		switch d.Name() {
		case ".git", "node_modules", ".gradle", "build", "target":
			if path != absPath {
				return filepath.SkipDir
			}
		}

		if path == absPath {
			return nil
		}

		relPath, err := filepath.Rel(absPath, path)
		if err != nil {
			return nil
		}

		relPathLower := strings.ToLower(relPath)

		matches := filterLower == ""
		if !matches && strings.HasPrefix(relPathLower, filterLower) {
			matches = true
		}
		if !matches {
			filterParts := strings.Split(filterLower, string(filepath.Separator))
			relPathParts := strings.Split(relPathLower, string(filepath.Separator))
			if len(filterParts) > 0 && len(relPathParts) > 0 {
				matches = true
				for i, fp := range filterParts {
					if fp == "" {
						continue
					}
					if i >= len(relPathParts) || !strings.HasPrefix(relPathParts[i], fp) {
						matches = false
						break
					}
				}
			}
		}

		if matches {
			dirs = append(dirs, relPath)
		}

		return nil
	})

	if err != nil && err != io.EOF {
		return nil
	}

	sort.Strings(dirs)
	return dirs
}

func discoverAvailableServices(project Project, cfg Config) []string {
	existingPaths := make(map[string]bool)
	for _, svc := range project.Services {
		svcPath := svc.Path
		if !filepath.IsAbs(svcPath) {
			svcPath = filepath.Join(project.Path, svcPath)
		}
		existingPaths[svcPath] = true
	}

	type discoveredService struct {
		name    string
		path    string
		command string
	}
	var discovered []discoveredService

	if project.Path != "" {
		projectPathAbs, err := normalizeAbsolutePath(project.Path, "")
		if err == nil {
			services, err := discoverServices(project.Path)
			if err == nil {
				for _, svc := range services {
					svcPath := svc.Path
					if !filepath.IsAbs(svcPath) {
						svcPath = filepath.Join(project.Path, svcPath)
					}
					svcPathAbs, err2 := normalizeAbsolutePath(svcPath, "")
					if err2 == nil && !existingPaths[svcPathAbs] {
						relPath, _ := filepath.Rel(projectPathAbs, svcPathAbs)
						discovered = append(discovered, discoveredService{
							name:    svc.Name,
							path:    relPath,
							command: svc.Command,
						})
					}
				}
			}
		}
	}

	if cfg.BasePath != "" && project.Path != "" {
		projectPathAbs, err2 := normalizeAbsolutePath(project.Path, "")
		if err2 == nil {
			services, err := discoverProjectsFromBasePath(cfg.BasePath)
			if err == nil {
				for _, svc := range services {
					svcPath := svc.Path
					if !filepath.IsAbs(svcPath) {
						svcPath = filepath.Join(cfg.BasePath, svcPath)
					}
					svcPathAbs, err3 := normalizeAbsolutePath(svcPath, "")
					if err3 == nil && !existingPaths[svcPathAbs] {
						relPath, _ := filepath.Rel(projectPathAbs, svcPathAbs)
						discovered = append(discovered, discoveredService{
							name:    svc.Name,
							path:    relPath,
							command: svc.Command,
						})
					}
				}
			}
		}
	}

	projectPathAbs := ""
	if project.Path != "" {
		var err error
		projectPathAbs, err = normalizeAbsolutePath(project.Path, "")
		if err != nil {
			projectPathAbs = ""
		}
	}

	for _, svc := range project.Services {
		svcPath := svc.Path
		if svcPath == "" || svcPath == "." {
			svcPath = project.Path
		} else if !filepath.IsAbs(svcPath) {
			svcPath = filepath.Join(project.Path, svcPath)
		}

		svcPathAbs, err := normalizeAbsolutePath(svcPath, "")
		if err != nil {
			continue
		}

		info, err := os.Stat(svcPathAbs)
		if err != nil || !info.IsDir() {
			continue
		}

		worktrees, err := discoverWorktrees(svcPathAbs)
		if err != nil {
			continue
		}

		for _, wt := range worktrees {
			wtPath := wt.Path
			if !filepath.IsAbs(wtPath) {
				wtPath = filepath.Join(svcPathAbs, wtPath)
			}
			wtPathAbs, err := normalizeAbsolutePath(wtPath, "")
			if err != nil || existingPaths[wtPathAbs] {
				continue
			}

			if projectPathAbs != "" {
				relPath, _ := filepath.Rel(projectPathAbs, wtPathAbs)
				discovered = append(discovered, discoveredService{
					name:    wt.Name,
					path:    relPath,
					command: wt.Command,
				})
			}
		}
	}

	var suggestions []string
	for _, svc := range discovered {
		suggestions = append(suggestions, svc.name+" | "+svc.path+" | "+svc.command)
	}

	sort.Strings(suggestions)
	return suggestions
}
