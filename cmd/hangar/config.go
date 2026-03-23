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

// addService appends a new service to the project at projectIndex and persists.
func addService(projectIndex int, name, path string) (Config, error) {
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

	cfg.Projects[projectIndex].Services = append(
		cfg.Projects[projectIndex].Services,
		Service{Name: name, Path: normalizedPath},
	)
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
	if path == "" {
		return "", fmt.Errorf("project path is required")
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

func isWithinBase(relativePath string) bool {
	if relativePath == "." {
		return true
	}
	parentPrefix := ".." + string(filepath.Separator)
	return relativePath != ".." && !strings.HasPrefix(relativePath, parentPrefix)
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
