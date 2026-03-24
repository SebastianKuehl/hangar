package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	hangarruntime "github.com/SebastianKuehl/hangar/internal/runtime"
	tea "github.com/charmbracelet/bubbletea"
)

const runtimeRefreshInterval = 2 * time.Second

const maxServiceTransitionPolls = 5

type serviceRuntime struct {
	known   bool
	running bool
	runtime hangarruntime.ServiceRuntime
}

type serviceTransitionPhase string

const (
	transitionPhaseStarting        serviceTransitionPhase = "starting"
	transitionPhaseStopping        serviceTransitionPhase = "stopping"
	transitionPhaseRestartStopping serviceTransitionPhase = "restart-stopping"
	transitionPhaseRestartStarting serviceTransitionPhase = "restart-starting"
)

type serviceTransition struct {
	targetRunning bool
	phase         serviceTransitionPhase
	previousOwner int32
	polls         int
}

func (s serviceTransition) label() string {
	switch s.phase {
	case transitionPhaseRestartStopping, transitionPhaseRestartStarting:
		return "restarting"
	case transitionPhaseStopping:
		return "stopping"
	}
	return "starting"
}

type serviceControlMsg struct {
	projectIndex int
	serviceKey   string
	startedPID   int32
	phase        serviceTransitionPhase
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

type serviceRuntimeRefreshMsg struct {
	projectIndex int
	serviceIndex int
	requestID    int
	serviceKey   string
	runtime      serviceRuntime
	err          error
}

type runtimeTickMsg time.Time

var newRuntimeManager = func() (*hangarruntime.Manager, error) {
	return hangarruntime.NewManager("")
}

func nextRuntimeRefreshCmd() tea.Cmd {
	return tea.Tick(runtimeRefreshInterval, func(t time.Time) tea.Msg {
		return runtimeTickMsg(t)
	})
}

func refreshProjectRuntimeCmd(requestID, projectIndex int, project Project) tea.Cmd {
	return func() tea.Msg {
		mgr, err := newRuntimeManager()
		if err != nil {
			return runtimeRefreshMsg{projectIndex: projectIndex, requestID: requestID, projectPath: project.Path, serviceCount: len(project.Services), err: err}
		}

		runtime := make([]serviceRuntime, len(project.Services))
		for i, service := range project.Services {
			svc := runtimeServiceConfig(project, service)
			rt, err := mgr.GetRuntime(svc)
			if err != nil {
				if hangarruntime.IsNotExist(err) {
					rt = hangarruntime.RuntimeForStoppedService(mgr, svc)
				} else {
					return runtimeRefreshMsg{projectIndex: projectIndex, requestID: requestID, projectPath: project.Path, serviceCount: len(project.Services), err: err}
				}
			}
			runtime[i] = serviceRuntime{known: true, running: mgr.IsRunning(rt), runtime: rt}
		}

		return runtimeRefreshMsg{
			projectIndex: projectIndex,
			requestID:    requestID,
			projectPath:  project.Path,
			serviceCount: len(project.Services),
			runtime:      runtime,
		}
	}
}

func startServiceCmd(projectIndex int, project Project, service Service) tea.Cmd {
	return func() tea.Msg {
		mgr, err := newRuntimeManager()
		if err != nil {
			return serviceControlMsg{projectIndex: projectIndex, serviceKey: serviceKey(project, service), err: err}
		}
		rt, err := mgr.StartService(context.Background(), runtimeServiceConfig(project, service))
		return serviceControlMsg{projectIndex: projectIndex, serviceKey: serviceKey(project, service), startedPID: int32(rt.PID), phase: transitionPhaseStarting, err: err}
	}
}

func refreshServiceRuntimeCmd(requestID, projectIndex, serviceIndex int, project Project, service Service) tea.Cmd {
	return func() tea.Msg {
		mgr, err := newRuntimeManager()
		if err != nil {
			return serviceRuntimeRefreshMsg{projectIndex: projectIndex, serviceIndex: serviceIndex, requestID: requestID, serviceKey: serviceKey(project, service), err: err}
		}
		svc := runtimeServiceConfig(project, service)
		rt, err := mgr.GetRuntime(svc)
		if err != nil {
			if hangarruntime.IsNotExist(err) {
				rt = hangarruntime.RuntimeForStoppedService(mgr, svc)
			} else {
				return serviceRuntimeRefreshMsg{projectIndex: projectIndex, serviceIndex: serviceIndex, requestID: requestID, serviceKey: serviceKey(project, service), err: err}
			}
		}
		return serviceRuntimeRefreshMsg{
			projectIndex: projectIndex,
			serviceIndex: serviceIndex,
			requestID:    requestID,
			serviceKey:   serviceKey(project, service),
			runtime:      serviceRuntime{known: true, running: mgr.IsRunning(rt), runtime: rt},
		}
	}
}

func stopServiceCmd(projectIndex int, project Project, service Service, runtime serviceRuntime, ownedPID int32) tea.Cmd {
	_ = runtime
	_ = ownedPID
	return func() tea.Msg {
		mgr, err := newRuntimeManager()
		if err != nil {
			return serviceControlMsg{projectIndex: projectIndex, serviceKey: serviceKey(project, service), phase: transitionPhaseStopping, err: err}
		}
		err = mgr.StopService(context.Background(), runtimeServiceConfig(project, service))
		return serviceControlMsg{projectIndex: projectIndex, serviceKey: serviceKey(project, service), phase: transitionPhaseStopping, err: err}
	}
}

func restartServiceCmd(projectIndex int, project Project, service Service, runtime serviceRuntime, ownedPID int32) tea.Cmd {
	_ = ownedPID
	return func() tea.Msg {
		mgr, err := newRuntimeManager()
		if err != nil {
			return serviceControlMsg{projectIndex: projectIndex, serviceKey: serviceKey(project, service), phase: transitionPhaseRestartStopping, err: err}
		}
		if runtime.running {
			if err := mgr.StopService(context.Background(), runtimeServiceConfig(project, service)); err != nil {
				return serviceControlMsg{projectIndex: projectIndex, serviceKey: serviceKey(project, service), phase: transitionPhaseRestartStopping, err: err}
			}
		}
		rt, err := mgr.StartService(context.Background(), runtimeServiceConfig(project, service))
		return serviceControlMsg{projectIndex: projectIndex, serviceKey: serviceKey(project, service), startedPID: int32(rt.PID), phase: transitionPhaseRestartStarting, err: err}
	}
}

func serviceKey(project Project, service Service) string {
	return project.Path + "\x00" + service.Path + "\x00" + service.Name
}

func runtimeServiceConfig(project Project, service Service) hangarruntime.ServiceConfig {
	path := service.Path
	if path == "" {
		path = "."
	}
	return hangarruntime.ServiceConfig{
		ProjectName: project.Name,
		ProjectPath: project.Path,
		Name:        service.Name,
		Path:        path,
		Command:     service.Command,
	}
}

func serviceDetailsItems(project Project, service Service, runtime serviceRuntime, transition *serviceTransition) []string {
	rt := runtime.runtime
	status := "stopped"
	if transition != nil {
		status = transition.label()
	} else if runtime.running {
		status = "running"
	}

	items := []string{
		"Project: " + project.Name,
		"Service: " + service.Name,
		"Path: " + servicePath(project, service),
		"Command: " + fallbackValue(service.Command, "unavailable"),
		"Status: " + status,
		"Ignored: " + boolToYesNo(service.Ignored),
		"Log file: " + fallbackValue(rt.LogPath, "unavailable"),
	}
	if rt.PID > 0 {
		items = append(items, fmt.Sprintf("PID: %d", rt.PID))
	}
	if rt.WorkDir != "" {
		items = append(items, "Working directory: "+rt.WorkDir)
	}
	if !rt.StartedAt.IsZero() {
		items = append(items, "Started: "+rt.StartedAt.Format(time.RFC3339))
	}
	if rt.StoppedAt != nil {
		items = append(items, "Stopped: "+rt.StoppedAt.Format(time.RFC3339))
	}
	return items
}

func serviceLogItems(project Project, service Service, runtime serviceRuntime, transition *serviceTransition) []string {
	if transition != nil {
		return []string{
			fmt.Sprintf("Hangar is %s %s.", transition.label(), service.Name),
			"Log file: " + fallbackValue(runtime.runtime.LogPath, "unavailable"),
		}
	}

	logPath := runtime.runtime.LogPath
	if _, err := os.Stat(logPath); err == nil {
		if runtime.running {
			return []string{
				"Waiting for log output...",
				"Log file: " + logPath,
			}
		}
		return []string{
			"No new log lines in the selected backlog.",
			"Log file: " + logPath,
		}
	}

	if runtime.running {
		return []string{
			"No logs yet. The service is running, but the log file is still empty.",
			"Log file: " + logPath,
		}
	}

	return []string{
		"No logs yet. Start the service to begin streaming output.",
		"Expected path: " + servicePath(project, service),
		"Log file: " + logPath,
	}
}

func servicePath(project Project, service Service) string {
	if service.Path == "" || service.Path == "." {
		return project.Path
	}
	if filepath.IsAbs(service.Path) {
		return service.Path
	}
	return filepath.Join(project.Path, service.Path)
}

func fallbackValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
