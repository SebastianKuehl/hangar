package main

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Service is a single runnable unit that belongs to a project.
type Service struct {
	Name string `yaml:"name"`
	Path string `yaml:"path,omitempty"`
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
	return os.Rename(tmpName, path)
}

// addProject appends a new project and persists the config.
func addProject(name, path string) (Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return cfg, err
	}
	cfg.Projects = append(cfg.Projects, Project{Name: name, Path: path})
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
	cfg.Projects[projectIndex].Services = append(
		cfg.Projects[projectIndex].Services,
		Service{Name: name, Path: path},
	)
	return cfg, saveConfig(cfg)
}
