package main

import (
	"encoding/json"
	"fmt"
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
	Name    string `yaml:"name"`
	Path    string `yaml:"path,omitempty"`
	Command string `yaml:"command,omitempty"`
	Ignored bool   `yaml:"ignored,omitempty"`
}

// Project groups a set of services under a named entry.
type Project struct {
	Name     string    `yaml:"name"`
	Path     string    `yaml:"path,omitempty"`
	Services []Service `yaml:"services,omitempty"`
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

// addService appends a new service to the project at projectIndex and persists.
func addService(projectIndex int, name, path, command string) (Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return cfg, err
	}
	if projectIndex < 0 || projectIndex >= len(cfg.Projects) {
		return cfg, fmt.Errorf("project index %d out of range (have %d projects)", projectIndex, len(cfg.Projects))
	}
	if strings.TrimSpace(cfg.Projects[projectIndex].Path) == "" && strings.TrimSpace(path) == "" {
		return cfg, fmt.Errorf("service path is required when the project has no path")
	}

	normalizedPath, err := normalizeServicePath(path, cfg.Projects[projectIndex].Path)
	if err != nil {
		return cfg, err
	}

	command = strings.TrimSpace(command)
	if command == "" {
		command = inferServiceCommand(cfg.Projects[projectIndex], normalizedPath)
	}

	cfg.Projects[projectIndex].Services = append(
		cfg.Projects[projectIndex].Services,
		Service{Name: name, Path: normalizedPath, Command: command},
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
		Name:    strings.TrimSpace(name),
		Path:    normalizedPath,
		Command: command,
		Ignored: ignored,
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

	sort.Slice(services, func(i, j int) bool {
		if services[i].Path == services[j].Path {
			return services[i].Name < services[j].Name
		}
		return services[i].Path < services[j].Path
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

	return Service{
		Name:    serviceName,
		Path:    filepath.Clean(relativePath),
		Command: startCommand(detectRuntime(serviceDir, manifest)),
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

func startCommand(runtime string) string {
	if runtime == "bun" {
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

	if strings.TrimSpace(currentCommand) != "" {
		return []string{strings.TrimSpace(currentCommand)}
	}
	return []string{inferServiceCommand(project, servicePath)}
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

	var suggestions []string
	for _, svc := range discovered {
		suggestions = append(suggestions, svc.name+" | "+svc.path+" | "+svc.command)
	}

	sort.Strings(suggestions)
	return suggestions
}
