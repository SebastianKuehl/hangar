package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/SebastianKuehl/hangar/internal/logstream"
	hangarruntime "github.com/SebastianKuehl/hangar/internal/runtime"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// version is the current release. Override at build time with:
//
//	go build -ldflags "-X main.version=1.2.3" ./cmd/hangar
var version = "0.0.1"

type paneID int

const (
	paneProjects paneID = iota
	paneServices
	paneDetails
	paneLogs
)

const maxLogLines = 4000

type listPane struct {
	title       string
	visible     bool
	placeholder string
	items       []string
	selected    int
}

type logChunkMsg struct {
	serviceKey string
	watchID    int
	lines      []string
	reset      bool
}

type logErrMsg struct {
	serviceKey string
	watchID    int
	err        error
}

type model struct {
	width  int
	height int

	focus    paneID
	showHelp bool
	wrapText bool

	projects listPane
	services listPane
	details  listPane
	logs     listPane

	cfg              Config
	serviceRuntime   []serviceRuntime
	serviceStates    map[string]serviceTransition
	serviceOwners    map[string]int32
	runtimeRequest   int
	runtimePending   bool
	runtimePaused    bool
	runtimeLoading   bool
	loadingTicker    bool
	loadingGen       int
	loadingFrame     int
	runtimeManager      *hangarruntime.Manager
	logTailer           *logstream.Tailer
	logEvents           chan tea.Msg
	logLines            map[string][]string
	logCancel           context.CancelFunc
	logListenCtx        context.Context
	logListenCancel     context.CancelFunc
	logWatchID          int
	followingService    string
	serviceRuntimeRequest int
	modal               formModal
	confirm             confirmModal
	errMsg              string
}

const loadingFrameInterval = 120 * time.Millisecond

var loadingFrames = []string{"|", "/", "-", "\\"}

type loadingTickMsg struct {
	at  time.Time
	gen int
}

// projectItems converts a Config's projects to display strings.
func projectItems(cfg Config) []string {
	out := make([]string, 0, len(cfg.Projects))
	for _, p := range cfg.Projects {
		out = append(out, p.Name)
	}
	return out
}

// serviceItems returns the service names for a project with runtime icons.
// loadingFrame is the current spinner frame index used for services whose
// runtime status is not yet known.
func serviceItems(project Project, runtime []serviceRuntime, states map[string]serviceTransition, loadingFrame int) []string {
	out := make([]string, 0, len(project.Services))
	for i, s := range project.Services {
		if s.Ignored {
			out = append(out, " ⊘ "+s.Name)
			continue
		}
		var icon string
		if _, pending := states[serviceKey(project, s)]; pending {
			icon = "◔"
		} else if i < len(runtime) && runtime[i].known {
			if runtime[i].running {
				icon = "●"
			} else {
				icon = "○"
			}
		} else {
			icon = loadingFrames[loadingFrame%len(loadingFrames)]
		}
		out = append(out, " "+icon+" "+s.Name)
	}
	return out
}

func newModel() model {
	cfg, _ := loadConfig()
	mgr, err := newRuntimeManager()

	listenCtx, listenCancel := context.WithCancel(context.Background())

	m := model{
		cfg:             cfg,
		focus:           paneProjects,
		serviceStates:   map[string]serviceTransition{},
		serviceOwners:   map[string]int32{},
		projects:        listPane{title: "Projects", visible: true, items: projectItems(cfg)},
		services:        listPane{title: "Services", visible: true},
		details:         listPane{title: "Details", visible: true, placeholder: "Select a service to inspect its runtime state."},
		logs:            listPane{title: "Logs", visible: true, placeholder: "Select a service to inspect its runtime state."},
		runtimeManager:  mgr,
		logTailer:       logstream.NewTailer(250 * time.Millisecond),
		logEvents:       make(chan tea.Msg, 32),
		logLines:        map[string][]string{},
		logListenCtx:    listenCtx,
		logListenCancel: listenCancel,
	}
	if err != nil {
		m.errMsg = "Error initializing hangar runtime: " + err.Error()
	}
	m.syncSelectionState()
	m.runtimeLoading = m.shouldShowRuntimeLoading()
	m.loadingTicker = m.runtimeLoading
	if m.runtimeLoading {
		m.loadingGen = 1
	}
	return m
}

func (m model) Init() tea.Cmd {
	return tea.Batch(nextRuntimeRefreshCmd(), m.startRuntimeRefresh(), m.loadingTickCmd())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case configSavedMsg:
		m.cfg = msg.cfg
		m.invalidateRuntimeRefresh(false)
		m.serviceRuntime = nil
		m.syncSelectionState()
		m.errMsg = ""
		return m, tea.Batch(m.startRuntimeRefresh(), m.beginRuntimeLoading(), m.restartLogTail())

	case serviceUpdatedMsg:
		oldKey := serviceKey(msg.previousProject, msg.previousService)
		m.cfg = msg.cfg
		if oldKey != "" {
			delete(m.serviceStates, oldKey)
			delete(m.serviceOwners, oldKey)
		}
		m.syncSelectionState()
		m.errMsg = ""
		if msg.projectIndex != m.projects.selected {
			return m, nil
		}
		project, ok := m.selectedProject()
		if !ok || msg.serviceIndex < 0 || msg.serviceIndex >= len(project.Services) {
			return m, nil
		}
		newService := project.Services[msg.serviceIndex]
		newKey := serviceKey(project, newService)
		if msg.serviceIndex < len(m.serviceRuntime) {
			m.serviceRuntime[msg.serviceIndex] = serviceRuntime{}
		}
		if msg.previousRuntime.running && serviceRuntimeChanged(msg.previousService, newService) {
			m.serviceStates[newKey] = serviceTransition{targetRunning: true}
			m.syncSelectionState()
			return m, restartEditedServiceCmd(msg.projectIndex, msg.serviceIndex, msg.previousProject, msg.previousService, msg.previousRuntime, msg.previousOwnedPID, project, newService)
		}
		m.serviceRuntimeRequest++
		m.syncSelectionState()
		return m, refreshServiceRuntimeCmd(m.serviceRuntimeRequest, msg.projectIndex, msg.serviceIndex, project, newService)

	case configErrMsg:
		m.errMsg = "Error saving config: " + msg.err.Error()
		return m, nil

	case runtimeRefreshMsg:
		project, ok := m.selectedProject()
		if !ok || msg.projectIndex != m.projects.selected || msg.requestID != m.runtimeRequest || msg.projectPath != project.Path || msg.serviceCount != len(project.Services) {
			return m, nil
		}
		m.runtimePending = false
		m.stopRuntimeLoading()
		if msg.err != nil {
			m.errMsg = "Error detecting runtime state: " + msg.err.Error()
			m.advanceServiceTransitionPollsOnError(project)
			m.syncSelectionState()
			return m, nil
		}
		m.serviceRuntime = msg.runtime
		m.errMsg = ""
		m.reconcileServiceTransitions(project, msg.runtime)
		m.pruneServiceOwners(project, msg.runtime)
		m.syncSelectionState()
		return m, m.ensureLogTail()

	case serviceControlMsg:
		if msg.err != nil {
			if transition, ok := m.serviceStates[msg.serviceKey]; ok && transition.previousOwner != 0 {
				if m.serviceOwners == nil {
					m.serviceOwners = map[string]int32{}
				}
				m.serviceOwners[msg.serviceKey] = transition.previousOwner
			}
			delete(m.serviceStates, msg.serviceKey)
			m.errMsg = msg.err.Error()
			m.syncSelectionState()
			return m, nil
		}
		if transition, ok := m.serviceStates[msg.serviceKey]; ok {
			transition.phase = msg.phase
			transition.polls = 0
			m.serviceStates[msg.serviceKey] = transition
		}
		if msg.startedPID != 0 {
			if m.serviceOwners == nil {
				m.serviceOwners = map[string]int32{}
			}
			m.serviceOwners[msg.serviceKey] = msg.startedPID
		}
		if msg.projectIndex == m.projects.selected {
			return m, tea.Batch(m.startRuntimeRefresh(), m.ensureLogTail())
		}
		return m, nil

	case serviceRestartMsg:
		delete(m.serviceStates, msg.oldServiceKey)
		delete(m.serviceOwners, msg.oldServiceKey)
		if msg.err != nil {
			delete(m.serviceStates, msg.newServiceKey)
			m.errMsg = msg.err.Error()
			m.syncSelectionState()
			return m, nil
		}
		if msg.startedPID != 0 {
			if m.serviceOwners == nil {
				m.serviceOwners = map[string]int32{}
			}
			m.serviceOwners[msg.newServiceKey] = msg.startedPID
		}
		if msg.projectIndex != m.projects.selected {
			return m, nil
		}
		project, ok := m.selectedProject()
		if !ok || msg.serviceIndex < 0 || msg.serviceIndex >= len(project.Services) {
			return m, nil
		}
		m.serviceRuntimeRequest++
		return m, refreshServiceRuntimeCmd(m.serviceRuntimeRequest, msg.projectIndex, msg.serviceIndex, project, project.Services[msg.serviceIndex])

	case serviceRuntimeRefreshMsg:
		project, ok := m.selectedProject()
		if !ok || msg.projectIndex != m.projects.selected || msg.requestID != m.serviceRuntimeRequest {
			return m, nil
		}
		if msg.serviceIndex < 0 || msg.serviceIndex >= len(project.Services) || msg.serviceKey != serviceKey(project, project.Services[msg.serviceIndex]) {
			return m, nil
		}
		if msg.err != nil {
			m.errMsg = "Error detecting runtime state: " + msg.err.Error()
			m.syncSelectionState()
			return m, nil
		}
		if msg.serviceIndex >= len(m.serviceRuntime) {
			return m, nil
		}
		m.serviceRuntime[msg.serviceIndex] = msg.runtime
		if transition, ok := m.serviceStates[msg.serviceKey]; ok && msg.runtime.known && msg.runtime.running == transition.targetRunning {
			delete(m.serviceStates, msg.serviceKey)
		}
		if owner, ok := m.serviceOwners[msg.serviceKey]; ok && owner != 0 && !runtimeContainsPID(msg.runtime, owner) {
			delete(m.serviceOwners, msg.serviceKey)
		}
		m.errMsg = ""
		m.syncSelectionState()
		return m, nil

	case runtimeTickMsg:
		if m.runtimePending || m.runtimePaused {
			return m, nextRuntimeRefreshCmd()
		}
		return m, tea.Batch(nextRuntimeRefreshCmd(), m.startRuntimeRefresh())

	case loadingTickMsg:
		if msg.gen != m.loadingGen {
			return m, nil
		}
		if !m.runtimeLoading || !m.loadingTicker {
			m.loadingTicker = false
			return m, nil
		}
		m.loadingFrame = (m.loadingFrame + 1) % len(loadingFrames)
		m.syncSelectionState()
		return m, m.loadingTickCmd()

	case logChunkMsg:
		if msg.watchID != m.logWatchID || msg.serviceKey != m.followingService {
			return m, listenForLogMessage(m.logEvents, m.logListenCtx)
		}
		shouldAutoFollow := msg.reset || m.shouldAutoFollowLogs(msg.serviceKey)
		if msg.reset {
			m.logLines[msg.serviceKey] = append([]string(nil), msg.lines...)
		} else {
			m.logLines[msg.serviceKey] = trimLogLines(append(m.logLines[msg.serviceKey], msg.lines...))
		}
		if shouldAutoFollow && msg.serviceKey == m.selectedServiceKey() {
			m.scrollLogsToBottom()
		}
		m.syncSelectionState()
		return m, listenForLogMessage(m.logEvents, m.logListenCtx)

	case logErrMsg:
		if msg.watchID == m.logWatchID && msg.serviceKey == m.followingService {
			m.errMsg = "Error tailing logs: " + msg.err.Error()
		}
		return m, listenForLogMessage(m.logEvents, m.logListenCtx)

	case tea.KeyMsg:
		k := msg.String()
		if k == "ctrl+c" {
			if m.logCancel != nil {
				m.logCancel()
			}
			return m, tea.Quit
		}

		if m.confirm.isOpen() {
			confirmed, closed := m.confirm.handleKey(k)
			if closed {
				action := m.confirm.action
				projectIndex := m.confirm.projectIndex
				serviceIndex := m.confirm.serviceIndex
				m.confirm.close()
				if confirmed {
					switch action {
					case confirmDeleteProject:
						return m, deleteProjectCmd(projectIndex)
					case confirmDeleteService:
						return m, deleteServiceCmd(projectIndex, serviceIndex)
					}
				}
			}
			return m, nil
		}

		if m.modal.isOpen() {
			submit, closeModal := m.modal.handleKey(k)
			if closeModal {
				m.modal.close()
				return m, nil
			}
			if submit {
				name := m.modal.name()
				path := m.modal.path()
				command := m.modal.command()
				mode := m.modal.mode
				projectIndex := m.projects.selected
				serviceIndex := m.services.selected
				m.modal.close()
				switch mode {
				case modalCreateProject:
					return m, saveProjectCmd(name, path)
				case modalEditProject:
					return m, updateProjectCmd(projectIndex, name, path)
				case modalCreateService:
					return m, saveServiceCmd(projectIndex, name, path, command)
				case modalEditService:
					project, ok := m.selectedProject()
					if !ok {
						return m, nil
					}
					service, ok := m.selectedService()
					if !ok {
						return m, nil
					}
					return m, updateServiceCmd(projectIndex, serviceIndex, name, path, command, m.modal.ignored(), project, service, m.selectedServiceRuntime(), m.serviceOwners[serviceKey(project, service)])
				}
				return m, nil
			}
			return m, nil
		}

		if m.showHelp {
			switch k {
			case "?", "esc":
				m.showHelp = false
				return m, nil
			case "q":
				if m.logCancel != nil {
					m.logCancel()
				}
				return m, tea.Quit
			default:
				return m, nil
			}
		}

		switch k {
		case "q":
			if m.logCancel != nil {
				m.logCancel()
			}
			return m, tea.Quit
		case "?":
			m.showHelp = true
			return m, nil
		case "c":
			if m.focus == paneProjects {
				m.modal.openCreateProject()
			} else if m.focus == paneServices {
				if len(m.cfg.Projects) == 0 {
					m.errMsg = "Create a project first before adding services."
				} else {
					m.modal.openCreateService(m.cfg.Projects[m.projects.selected])
				}
			}
			return m, nil
		case "e":
			if m.focus == paneProjects {
				project, ok := m.selectedProject()
				if !ok {
					return m, nil
				}
				m.modal.openEditProject(project)
				return m, nil
			}
			if m.focus == paneServices {
				project, ok := m.selectedProject()
				if !ok {
					return m, nil
				}
				service, ok := m.selectedService()
				if !ok {
					return m, nil
				}
				m.modal.openEditService(project, service)
				return m, nil
			}
			return m, nil
		case "p":
			m.projects.visible = !m.projects.visible
			m.ensureFocusVisible()
			return m, nil
		case "backspace":
			if m.focus == paneProjects {
				project, ok := m.selectedProject()
				if !ok {
					return m, nil
				}
				m.confirm.open(confirmDeleteProject,
					"Delete project \""+project.Name+"\"?",
					m.projects.selected, 0)
			} else if m.focus == paneServices {
				project, ok := m.selectedProject()
				if !ok {
					return m, nil
				}
				service, ok := m.selectedService()
				if !ok {
					return m, nil
				}
				m.confirm.open(confirmDeleteService,
					"Delete service \""+service.Name+"\" from \""+project.Name+"\"?",
					m.projects.selected, m.services.selected)
			}
			return m, nil
		case "d":
			m.details.visible = !m.details.visible
			m.ensureFocusVisible()
			return m, nil
		case "a":
			m.logs.visible = !m.logs.visible
			m.ensureFocusVisible()
			return m, nil
		case "t":
			m.wrapText = !m.wrapText
			return m, nil
		case "i":
			if m.focus == paneServices {
				project, ok := m.selectedProject()
				if !ok {
					return m, nil
				}
				_, ok = m.selectedService()
				if !ok {
					return m, nil
				}
				return m, toggleIgnoreServiceCmd(m.projects.selected, m.services.selected, project)
			}
			return m, nil
		case "s":
			return m, m.startStopSelectedService()
		case "r":
			switch m.focus {
			case paneProjects:
				return m, m.restartSelectedProject()
			case paneServices:
				return m, m.restartSelectedService()
			default:
				return m, nil
			}
		case "h", "left":
			m.focusPrev()
			return m, nil
		case "l", "right":
			m.focusNext()
			return m, nil
		case "j", "down":
			m.moveSelection(1)
			if m.focus == paneProjects {
				m.invalidateRuntimeRefresh(false)
				m.services.selected = 0
				m.serviceRuntime = nil
				m.syncSelectionState()
				return m, tea.Batch(m.startRuntimeRefresh(), m.beginRuntimeLoading(), m.restartLogTail())
			}
			m.syncSelectionState()
			if m.focus == paneServices {
				m.invalidateRuntimeRefresh(false)
				return m, tea.Batch(m.startRuntimeRefresh(), m.restartLogTail())
			}
			return m, nil
		case "k", "up":
			m.moveSelection(-1)
			if m.focus == paneProjects {
				m.invalidateRuntimeRefresh(false)
				m.services.selected = 0
				m.serviceRuntime = nil
				m.syncSelectionState()
				return m, tea.Batch(m.startRuntimeRefresh(), m.beginRuntimeLoading(), m.restartLogTail())
			}
			m.syncSelectionState()
			if m.focus == paneServices {
				m.invalidateRuntimeRefresh(false)
				return m, tea.Batch(m.startRuntimeRefresh(), m.restartLogTail())
			}
			return m, nil
		}
	}

	return m, nil
}

func (m *model) startRuntimeRefresh() tea.Cmd {
	project, ok := m.selectedProject()
	if !ok || m.runtimePending || m.runtimePaused {
		return nil
	}
	m.runtimeRequest++
	m.runtimePending = true
	return refreshProjectRuntimeCmd(m.runtimeRequest, m.projects.selected, project)
}

func (m model) loadingTickCmd() tea.Cmd {
	if !m.runtimeLoading || !m.loadingTicker {
		return nil
	}
	return tea.Tick(loadingFrameInterval, func(t time.Time) tea.Msg {
		return loadingTickMsg{at: t, gen: m.loadingGen}
	})
}

func (m *model) beginRuntimeLoading() tea.Cmd {
	m.runtimeLoading = m.shouldShowRuntimeLoading()
	if !m.runtimeLoading {
		m.loadingTicker = false
		m.loadingGen++
		m.loadingFrame = 0
		return nil
	}
	if m.loadingTicker {
		return nil
	}
	m.loadingGen++
	m.loadingTicker = true
	return m.loadingTickCmd()
}

func (m *model) stopRuntimeLoading() {
	m.runtimeLoading = false
	m.loadingTicker = false
	m.loadingGen++
	m.loadingFrame = 0
}

func (m *model) invalidateRuntimeRefresh(pause bool) {
	m.runtimeRequest++
	m.runtimePending = false
	m.runtimePaused = pause
	m.stopRuntimeLoading()
}

func (m model) shouldShowRuntimeLoading() bool {
	project, ok := m.selectedProject()
	if !ok || len(project.Services) == 0 || m.runtimePaused {
		return false
	}
	if len(m.serviceRuntime) != len(project.Services) {
		return true
	}
	for _, runtime := range m.serviceRuntime {
		if !runtime.known {
			return true
		}
	}
	return false
}

func (m *model) syncSelectionState() {
	m.projects.items = projectItems(m.cfg)
	m.projects.selected = clamp(m.projects.selected, 0, len(m.projects.items)-1)

	project, ok := m.selectedProject()
	if !ok {
		m.serviceRuntime = nil
		m.services.items = nil
		m.services.selected = 0
		m.details.placeholder = "Create a project and select a service to inspect it."
		m.details.items = []string{"(no service selected)"}
		m.logs.placeholder = "Create a project and select a service to inspect it."
		m.logs.items = []string{"(no service selected)"}
		return
	}

	if len(m.serviceRuntime) != len(project.Services) {
		m.serviceRuntime = make([]serviceRuntime, len(project.Services))
	}
	m.services.items = serviceItems(project, m.serviceRuntime, m.serviceStates, m.loadingFrame)
	m.services.selected = clamp(m.services.selected, 0, len(project.Services)-1)

	service, ok := m.selectedService()
	if !ok {
		m.details.placeholder = "Select a service to inspect its runtime state."
		m.details.items = []string{"(no service selected)"}
		m.logs.placeholder = "Select a service to inspect its runtime state."
		m.logs.items = []string{"(no service selected)"}
		return
	}

	runtime := m.selectedServiceRuntime()
	transition := m.selectedServiceTransition()
	m.details.placeholder = ""
	m.details.items = serviceDetailsItems(project, service, runtime, transition)

	key := serviceKey(project, service)
	if lines := m.logLines[key]; len(lines) > 0 {
		m.logs.placeholder = ""
		m.logs.items = lines
		m.logs.selected = clamp(m.logs.selected, 0, len(lines)-1)
		return
	}
	m.logs.placeholder = ""
	m.logs.items = serviceLogItems(project, service, runtime, transition)
	m.logs.selected = clamp(m.logs.selected, 0, len(m.logs.items)-1)
}

func (m *model) startStopSelectedService() tea.Cmd {
	project, ok := m.selectedProject()
	if !ok {
		return nil
	}
	if m.focus == paneProjects {
		return m.toggleProject(project)
	}
	service, ok := m.selectedService()
	if !ok {
		return nil
	}
	return m.toggleService(project, service, m.selectedServiceRuntime())
}

// toggleIgnoreServiceCmd flips the Ignored flag on a service and reloads the config.
func toggleIgnoreServiceCmd(projectIndex, serviceIndex int, project Project) tea.Cmd {
	return func() tea.Msg {
		cfg, err := toggleServiceIgnored(projectIndex, serviceIndex)
		if err != nil {
			return configErrMsg{err}
		}
		return configSavedMsg{cfg}
	}
}

func (m *model) restartSelectedService() tea.Cmd {
	project, ok := m.selectedProject()
	if !ok {
		return nil
	}
	service, ok := m.selectedService()
	if !ok {
		return nil
	}
	runtime := m.selectedServiceRuntime()
	if !runtime.known {
		m.errMsg = "Wait for runtime detection before restarting a service."
		return nil
	}
	if runtime.running {
		return m.queueServiceAction(m.projects.selected, project, service, runtime, transitionPhaseRestartStopping)
	}
	return m.queueServiceAction(m.projects.selected, project, service, runtime, transitionPhaseStarting)
}

func (m *model) restartSelectedProject() tea.Cmd {
	project, ok := m.selectedProject()
	if !ok {
		return nil
	}
	if len(project.Services) == 0 {
		m.errMsg = "Selected project has no services to restart."
		return nil
	}

	cmds := make([]tea.Cmd, 0, len(project.Services))
	skippedUnknown := 0
	for i, service := range project.Services {
		if service.Ignored {
			continue
		}
		runtime := m.runtimeForService(i)
		if !runtime.known {
			skippedUnknown++
			continue
		}
		cmd := m.queueServiceAction(m.projects.selected, project, service, runtime, transitionPhaseRestartStopping)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if len(cmds) == 0 {
		if skippedUnknown > 0 {
			m.errMsg = "Wait for runtime detection before restarting this project's services."
		} else {
			m.errMsg = "No project services are ready to restart right now."
		}
		m.syncSelectionState()
		return nil
	}
	return tea.Batch(cmds...)
}

func (m *model) queueServiceAction(projectIndex int, project Project, service Service, runtime serviceRuntime, phase serviceTransitionPhase) tea.Cmd {
	key := serviceKey(project, service)
	if _, busy := m.serviceStates[key]; busy {
		return nil
	}

	ownedPID := m.serviceOwners[key]
	targetRunning := phase != transitionPhaseStopping
	m.serviceStates[key] = serviceTransition{targetRunning: targetRunning, phase: phase, previousOwner: ownedPID}
	if phase == transitionPhaseRestartStopping {
		delete(m.serviceOwners, key)
	}
	m.errMsg = ""
	m.syncSelectionState()

	switch phase {
	case transitionPhaseStopping:
		return stopServiceCmd(projectIndex, project, service, runtime, ownedPID)
	case transitionPhaseRestartStopping:
		if runtime.running {
			return restartServiceCmd(projectIndex, project, service, runtime, ownedPID)
		}
		m.serviceStates[key] = serviceTransition{targetRunning: true, phase: transitionPhaseStarting}
		return startServiceCmd(projectIndex, project, service)
	default:
		return startServiceCmd(projectIndex, project, service)
	}
}

func (m model) runtimeForService(index int) serviceRuntime {
	if index < 0 || index >= len(m.serviceRuntime) {
		return serviceRuntime{}
	}
	return m.serviceRuntime[index]
}

func (m model) selectedProject() (Project, bool) {
	if m.projects.selected < 0 || m.projects.selected >= len(m.cfg.Projects) {
		return Project{}, false
	}
	return m.cfg.Projects[m.projects.selected], true
}

// serviceRuntimeChanged reports whether fields that affect the running process
// (name, path, command) differ between two service definitions. Changes to
// metadata-only fields like Ignored do not require a restart.
func serviceRuntimeChanged(prev, next Service) bool {
	return prev.Name != next.Name || prev.Path != next.Path || prev.Command != next.Command
}

func (m model) selectedService() (Service, bool) {
	project, ok := m.selectedProject()
	if !ok || m.services.selected < 0 || m.services.selected >= len(project.Services) {
		return Service{}, false
	}
	return project.Services[m.services.selected], true
}

func (m model) selectedServiceRuntime() serviceRuntime {
	if m.services.selected < 0 || m.services.selected >= len(m.serviceRuntime) {
		return serviceRuntime{}
	}
	return m.serviceRuntime[m.services.selected]
}

func (m *model) toggleProject(project Project) tea.Cmd {
	if len(project.Services) == 0 {
		return nil
	}
	if len(m.serviceRuntime) != len(project.Services) {
		m.errMsg = "Wait for runtime detection before toggling a project."
		return nil
	}

	shouldStop := false
	for i, service := range project.Services {
		if !m.serviceRuntime[i].known {
			m.errMsg = "Wait for runtime detection before toggling a project."
			return nil
		}
		if _, busy := m.serviceStates[serviceKey(project, service)]; busy {
			m.errMsg = "Wait for pending service transitions before toggling a project."
			return nil
		}
		// Ignored services don't influence the start/stop decision.
		if service.Ignored {
			continue
		}
		if m.serviceRuntime[i].running {
			shouldStop = true
		}
	}

	cmds := make([]tea.Cmd, 0, len(project.Services))
	for i, service := range project.Services {
		// When starting a project, skip ignored services entirely.
		if !shouldStop && service.Ignored {
			continue
		}
		runtime := m.serviceRuntime[i]
		if shouldStop && !runtime.running {
			continue
		}
		if !shouldStop && runtime.running {
			continue
		}

		if shouldStop {
			cmd := m.queueServiceAction(m.projects.selected, project, service, runtime, transitionPhaseStopping)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
			continue
		}
		cmd := m.queueServiceAction(m.projects.selected, project, service, runtime, transitionPhaseStarting)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

func (m *model) toggleService(project Project, service Service, runtime serviceRuntime) tea.Cmd {
	if !runtime.known {
		m.errMsg = "Wait for runtime detection before toggling a service."
		return nil
	}
	serviceKey := serviceKey(project, service)
	if _, busy := m.serviceStates[serviceKey]; busy {
		return nil
	}
	if runtime.running {
		return m.queueServiceAction(m.projects.selected, project, service, runtime, transitionPhaseStopping)
	}
	return m.queueServiceAction(m.projects.selected, project, service, runtime, transitionPhaseStarting)
}

func (m model) selectedServiceTransition() *serviceTransition {
	project, ok := m.selectedProject()
	if !ok {
		return nil
	}
	service, ok := m.selectedService()
	if !ok {
		return nil
	}
	transition, ok := m.serviceStates[serviceKey(project, service)]
	if !ok {
		return nil
	}
	return &transition
}

func (m model) selectedServiceKey() string {
	project, ok := m.selectedProject()
	if !ok {
		return ""
	}
	service, ok := m.selectedService()
	if !ok {
		return ""
	}
	return serviceKey(project, service)
}

func (m *model) selectedLogPath() (string, string, bool) {
	project, ok := m.selectedProject()
	if !ok || m.runtimeManager == nil {
		return "", "", false
	}
	service, ok := m.selectedService()
	if !ok {
		return "", "", false
	}
	key := serviceKey(project, service)
	if m.services.selected >= 0 && m.services.selected < len(m.serviceRuntime) {
		if path := m.serviceRuntime[m.services.selected].runtime.LogPath; path != "" {
			return key, path, true
		}
	}
	return key, m.runtimeManager.LogPath(runtimeServiceConfig(project, service)), true
}

func (m *model) ensureLogTail() tea.Cmd {
	key, _, ok := m.selectedLogPath()
	if !ok {
		if m.logCancel != nil {
			m.logCancel()
			m.logCancel = nil
		}
		m.followingService = ""
		return nil
	}
	if key == m.followingService && m.logCancel != nil {
		return nil
	}
	return m.restartLogTail()
}

func (m *model) restartLogTail() tea.Cmd {
	if m.logCancel != nil {
		m.logCancel()
		m.logCancel = nil
	}
	if m.logListenCancel != nil {
		m.logListenCancel()
	}
	listenCtx, listenCancel := context.WithCancel(context.Background())
	m.logListenCtx = listenCtx
	m.logListenCancel = listenCancel
	key, logPath, ok := m.selectedLogPath()
	if !ok || m.logTailer == nil {
		m.followingService = ""
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.logCancel = cancel
	m.logWatchID++
	watchID := m.logWatchID
	m.followingService = key
	if key == m.selectedServiceKey() {
		m.scrollLogsToBottom()
	}
	if m.logEvents == nil {
		m.logEvents = make(chan tea.Msg, 32)
	}
	outbox := m.logEvents
	tailer := m.logTailer

	go func() {
		events := make(chan logstream.LogEvent, 8)
		go func() {
			defer close(events)
			_ = tailer.TailFile(ctx, logPath, true, 200, events)
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-events:
				if !ok {
					return
				}
				var msg tea.Msg
				if event.Err != nil {
					msg = logErrMsg{serviceKey: key, watchID: watchID, err: event.Err}
				} else {
					msg = logChunkMsg{serviceKey: key, watchID: watchID, lines: event.Lines, reset: event.Reset}
				}
				select {
				case <-ctx.Done():
					return
				case outbox <- msg:
				}
			}
		}
	}()

	return listenForLogMessage(m.logEvents, m.logListenCtx)
}

func listenForLogMessage(ch <-chan tea.Msg, ctx context.Context) tea.Cmd {
	if ch == nil || ctx == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			return msg
		}
	}
}

func (m *model) shouldAutoFollowLogs(serviceKey string) bool {
	if serviceKey != m.selectedServiceKey() {
		return false
	}
	if len(m.logs.items) == 0 {
		return true
	}
	return m.logs.selected >= len(m.logs.items)-1
}

func (m *model) scrollLogsToBottom() {
	key := m.selectedServiceKey()
	if key == "" {
		m.logs.selected = clamp(m.logs.selected, 0, len(m.logs.items)-1)
		return
	}
	if lines := m.logLines[key]; len(lines) > 0 {
		m.logs.selected = len(lines) - 1
		return
	}
	m.logs.selected = clamp(m.logs.selected, 0, len(m.logs.items)-1)
}

func trimLogLines(lines []string) []string {
	if len(lines) <= maxLogLines {
		return lines
	}
	return append([]string(nil), lines[len(lines)-maxLogLines:]...)
}

func (m *model) reconcileServiceTransitions(project Project, runtime []serviceRuntime) {
	for i, service := range project.Services {
		key := serviceKey(project, service)
		transition, ok := m.serviceStates[key]
		if !ok {
			continue
		}
		if i < len(runtime) && runtime[i].known {
			ownedPID := m.serviceOwners[key]
			switch transition.phase {
			case transitionPhaseStopping:
				if !runtime[i].running {
					delete(m.serviceStates, key)
					delete(m.serviceOwners, key)
					continue
				}
			case transitionPhaseStarting, transitionPhaseRestartStarting:
				if runtime[i].running && (ownedPID == 0 || runtimeContainsPID(runtime[i], ownedPID)) {
					delete(m.serviceStates, key)
					continue
				}
			}
		}
		transition.polls++
		if transition.polls >= maxServiceTransitionPolls {
			delete(m.serviceStates, key)
			delete(m.serviceOwners, key)
			m.errMsg = fmt.Sprintf("Timed out while %s %s. Press %s to try again.", transition.label(), service.Name, retryHotkey(transition.phase))
			continue
		}
		m.serviceStates[key] = transition
	}
}

func (m *model) pruneServiceOwners(project Project, runtime []serviceRuntime) {
	for i, service := range project.Services {
		key := serviceKey(project, service)
		if _, pending := m.serviceStates[key]; pending {
			continue
		}
		ownedPID := m.serviceOwners[key]
		if ownedPID == 0 {
			continue
		}
		if i >= len(runtime) || !runtimeContainsPID(runtime[i], ownedPID) {
			delete(m.serviceOwners, key)
		}
	}
}

func (m *model) advanceServiceTransitionPollsOnError(project Project) {
	for _, service := range project.Services {
		key := serviceKey(project, service)
		transition, ok := m.serviceStates[key]
		if !ok {
			continue
		}
		transition.polls++
		if transition.polls >= maxServiceTransitionPolls {
			delete(m.serviceStates, key)
			delete(m.serviceOwners, key)
			m.errMsg = fmt.Sprintf("Timed out while %s %s. Press %s to try again.", transition.label(), service.Name, retryHotkey(transition.phase))
			continue
		}
		m.serviceStates[key] = transition
	}
}

func retryHotkey(phase serviceTransitionPhase) string {
	if phase == transitionPhaseRestartStopping || phase == transitionPhaseRestartStarting {
		return "r"
	}
	return "s"
}

func runtimeContainsPID(runtime serviceRuntime, pid int32) bool {
	return runtime.runtime.PID == int(pid)
}

func (m *model) focusOrder() []paneID {
	order := make([]paneID, 0, 4)
	if m.projects.visible {
		order = append(order, paneProjects)
	}
	order = append(order, paneServices)
	if m.details.visible {
		order = append(order, paneDetails)
	}
	if m.logs.visible {
		order = append(order, paneLogs)
	}
	return order
}

func (m *model) ensureFocusVisible() {
	order := m.focusOrder()
	if len(order) == 0 {
		m.focus = paneServices
		return
	}
	for _, id := range order {
		if id == m.focus {
			return
		}
	}
	m.focus = order[0]
}

func (m *model) focusPrev() {
	order := m.focusOrder()
	if len(order) == 0 {
		return
	}
	idx := indexOf(order, m.focus)
	if idx == -1 {
		m.focus = order[0]
		return
	}
	if idx > 0 {
		idx--
	}
	m.focus = order[idx]
}

func (m *model) focusNext() {
	order := m.focusOrder()
	if len(order) == 0 {
		return
	}
	idx := indexOf(order, m.focus)
	if idx == -1 {
		m.focus = order[0]
		return
	}
	if idx < len(order)-1 {
		idx++
	}
	m.focus = order[idx]
}

func indexOf[T comparable](s []T, v T) int {
	for i := range s {
		if s[i] == v {
			return i
		}
	}
	return -1
}

func (m *model) moveSelection(delta int) {
	switch m.focus {
	case paneProjects:
		m.projects.selected = clamp(m.projects.selected+delta, 0, len(m.projects.items)-1)
	case paneServices:
		m.services.selected = clamp(m.services.selected+delta, 0, len(m.services.items)-1)
	case paneDetails:
		m.details.selected = clamp(m.details.selected+delta, 0, len(m.details.items)-1)
	case paneLogs:
		m.logs.selected = clamp(m.logs.selected+delta, 0, len(m.logs.items)-1)
	}
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampToViewport(w, h int, s string) string {
	out := lipgloss.Place(w, h, lipgloss.Left, lipgloss.Top, s)
	for lipgloss.Height(out) > h {
		next := strings.TrimSuffix(out, "\n")
		if next == out {
			break
		}
		out = next
	}
	return out
}

func renderRow(st lipgloss.Style, w int, s string) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	return st.Width(w).Render(s)
}

var (
	colorBorder        = lipgloss.Color("#3b3b3b")
	colorBorderFocused = lipgloss.Color("#3fb950")
	colorTitle         = lipgloss.Color("#c9d1d9")
	colorTitleFocused  = lipgloss.Color("#7ee787")
	colorSelBgFocused  = lipgloss.Color("#238636")
	colorSelBgContext  = lipgloss.Color("#6e40c9") // project stays highlighted when services pane is focused
	colorSelBgFaint    = lipgloss.Color("#30363d")
	colorSelFg         = lipgloss.Color("#ffffff")

	basePaneStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(0, 1)

	focusedPaneStyle = basePaneStyle.Copy().
				BorderForeground(colorBorderFocused)

	titleStyle = lipgloss.NewStyle().
			Foreground(colorTitle).
			Bold(true)

	titleFocusedStyle = titleStyle.Copy().
				Foreground(colorTitleFocused)

	selectedStyle = lipgloss.NewStyle().
			Foreground(colorSelFg).
			Background(colorSelBgFaint)

	selectedFocusedStyle = selectedStyle.Copy().
				Background(colorSelBgFocused).
				Bold(true)

	selectedContextStyle = selectedStyle.Copy().
				Background(colorSelBgContext).
				Bold(true)

	faintStyle = lipgloss.NewStyle().Faint(true)
)

func (m model) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}

	if m.width < 60 || m.height < 10 {
		return lipgloss.NewStyle().Padding(1, 2).Render(
			"Terminal too small. Resize to at least 60x10.\n\n" +
				"Hotkeys: h/l focus, j/k move, p/d/a toggle panes, t wrap, c create, e edit, i ignore, s start/stop, r restart, ? help, q quit.")
	}

	var base string
	if m.showHelp {
		base = m.viewMain()
		box := m.renderHelpBox()
		base = overlayBoxCentered(m.width, m.height, base, box)
	} else {
		base = m.viewMain()
	}

	if m.modal.isOpen() {
		box := m.renderModal(m.width, m.height)
		base = overlayBoxCentered(m.width, m.height, base, box)
	}

	if m.confirm.isOpen() {
		box := m.confirm.render()
		base = overlayBoxCentered(m.width, m.height, base, box)
	}

	return clampToViewport(m.width, m.height, base)
}

func (m model) viewMain() string {
	gap := 1

	// Reserve 1 row for the status/error bar at the bottom when there's a message.
	contentH := m.height
	statusBar := ""
	if m.errMsg != "" {
		contentH = max(0, m.height-1)
		errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f85149")).Bold(true).Width(m.width)
		statusBar = "\n" + errStyle.Render("⚠ "+m.errMsg)
	}

	rightVisible := m.details.visible || m.logs.visible

	// Context highlights: a pane stays purple when focus moves to a pane to its right.
	projectHighlight := m.focus == paneServices || m.focus == paneDetails || m.focus == paneLogs
	serviceHighlight := m.focus == paneDetails || m.focus == paneLogs

	var out string

	// If the right column is completely hidden, let Services expand to use that space.
	if !rightVisible {
		if m.projects.visible {
			col1 := m.width / 4
			col2 := m.width - col1 - gap
			left := m.renderListPane(m.projects, col1, contentH, m.focus == paneProjects, projectHighlight, false)
			mid := m.renderListPane(m.services, col2, contentH, m.focus == paneServices, serviceHighlight, false)
			out = lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", gap), mid)
		} else {
			out = m.renderListPane(m.services, m.width, contentH, m.focus == paneServices, serviceHighlight, false)
		}
		return clampToViewport(m.width, m.height, out+statusBar)
	}

	if m.projects.visible {
		col1 := m.width / 4
		col2 := m.width / 4
		col3 := m.width - col1 - col2 - 2*gap
		if col3 < 20 {
			col3 = 20
		}

		left := m.renderListPane(m.projects, col1, contentH, m.focus == paneProjects, projectHighlight, false)
		mid := m.renderListPane(m.services, col2, contentH, m.focus == paneServices, serviceHighlight, false)
		right := m.renderRightColumn(col3, contentH)
		section := lipgloss.JoinHorizontal(lipgloss.Top, mid, strings.Repeat(" ", gap), right)
		out = lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", gap), section)
		return clampToViewport(m.width, m.height, out+statusBar)
	}

	col2 := m.width / 3
	col3 := m.width - col2 - gap
	mid := m.renderListPane(m.services, col2, contentH, m.focus == paneServices, serviceHighlight, false)
	right := m.renderRightColumn(col3, contentH)
	section := lipgloss.JoinHorizontal(lipgloss.Top, mid, strings.Repeat(" ", gap), right)
	out = section
	return clampToViewport(m.width, m.height, out+statusBar)
}

func (m model) renderRightColumn(width, height int) string {
	visible := 0
	if m.details.visible {
		visible++
	}
	if m.logs.visible {
		visible++
	}

	switch visible {
	case 2:
		// lipgloss.JoinVertical inserts a single newline separator between its arguments.
		// If we also add "\n" as a spacer, we exceed the terminal height and the top of the UI gets clipped.
		sep := 1
		avail := height - sep
		if avail < 2 {
			// Too small to split: just render one pane.
			return m.renderListPane(m.details, width, height, m.focus == paneDetails, false, m.wrapText)
		}
		topH := avail / 2
		botH := avail - topH
		top := m.renderListPane(m.details, width, topH, m.focus == paneDetails, false, m.wrapText)
		bot := m.renderListPane(m.logs, width, botH, m.focus == paneLogs, false, m.wrapText)
		return lipgloss.JoinVertical(lipgloss.Left, top, bot)
	case 1:
		if m.details.visible {
			return m.renderListPane(m.details, width, height, m.focus == paneDetails, false, m.wrapText)
		}
		return m.renderListPane(m.logs, width, height, m.focus == paneLogs, false, m.wrapText)
	default:
		empty := listPane{title: "Right Column", visible: true, placeholder: "PLACEHOLDER: Enable Details (d) or Logs (a).", items: []string{"(nothing to show)"}}
		return m.renderListPane(empty, width, height, false, false, false)
	}
}

func (m model) renderListPane(p listPane, width, height int, focused bool, highlightSel bool, wrap bool) string {
	if width <= 0 || height <= 0 {
		return ""
	}

	// Render the title above the bordered box so it doesn't get clipped by viewport math.
	ts := titleStyle
	if focused {
		ts = titleFocusedStyle
	}
	title := ts.Width(width).Padding(0, 1).Render(p.title)
	if height == 1 {
		return title
	}

	boxH := height - 1
	style := basePaneStyle
	if focused {
		style = focusedPaneStyle
	}

	innerW := max(0, width-4) // border (2) + horizontal padding (2)
	innerH := max(0, boxH-2)  // border (2)

	plainLine := lipgloss.NewStyle()
	lines, selectedStart, selectedEnd := paneRows(p, innerW, focused, highlightSel, wrap)

	// lipgloss.Style.Height() sets a minimum, not a maximum. Clamp and pad the
	// number of rendered lines so panes always fit their allocated viewport.
	if innerH == 0 {
		lines = nil
	} else {
		lines = visiblePaneRows(lines, innerH, selectedStart, selectedEnd)
		for len(lines) < innerH {
			lines = append(lines, renderRows(plainLine, innerW, "", false)...)
		}
	}

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	// Note: Pane content is intentionally placeholder data for now.
	box := style.Render(content)
	for strings.HasSuffix(box, "\n") {
		box = strings.TrimSuffix(box, "\n")
	}
	return title + "\n" + box
}

func paneRows(p listPane, innerW int, focused bool, highlightSel bool, wrap bool) ([]string, int, int) {
	lines := make([]string, 0, 2+len(p.items))
	selectedStart, selectedEnd := -1, -1
	if p.placeholder != "" {
		lines = append(lines, renderRows(faintStyle, innerW, p.placeholder, wrap)...)
		lines = append(lines, renderRows(lipgloss.NewStyle(), innerW, "", false)...)
	}

	plainLine := lipgloss.NewStyle()
	for i, it := range p.items {
		line := it
		style := plainLine
		if i == p.selected {
			switch {
			case focused:
				style = selectedFocusedStyle
			case highlightSel:
				style = selectedContextStyle
			default:
				style = selectedStyle
			}
		}

		itemRows := renderRows(style, innerW, line, wrap)
		if i == p.selected {
			selectedStart = len(lines)
			selectedEnd = len(lines) + len(itemRows) - 1
		}
		lines = append(lines, itemRows...)
	}
	return lines, selectedStart, selectedEnd
}

func visiblePaneRows(lines []string, height, selectedStart, selectedEnd int) []string {
	if height <= 0 {
		return nil
	}
	if len(lines) <= height {
		return append([]string(nil), lines...)
	}

	start := 0
	if selectedEnd >= height {
		start = selectedEnd - height + 1
	}
	if selectedStart >= 0 && selectedStart < start {
		start = selectedStart
	}
	maxStart := len(lines) - height
	if start > maxStart {
		start = maxStart
	}
	if start < 0 {
		start = 0
	}

	return append([]string(nil), lines[start:start+height]...)
}

func renderRows(st lipgloss.Style, w int, s string, wrap bool) []string {
	if !wrap {
		return []string{renderRow(st, w, s)}
	}
	if w <= 0 {
		return []string{""}
	}

	wrapped := ansi.Wrap(s, w, "")
	parts := strings.Split(wrapped, "\n")
	rows := make([]string, 0, len(parts))
	for _, part := range parts {
		rows = append(rows, st.Width(w).Render(part))
	}
	return rows
}

func (m model) renderHelpBox() string {
	maxBoxW := min(74, m.width-6)
	if maxBoxW < 30 {
		maxBoxW = min(30, m.width)
	}

	appBg := lipgloss.Color("#30363d")
	fg := lipgloss.Color("#c9d1d9")

	header := lipgloss.NewStyle().Bold(true).Foreground(fg).Render("Hotkeys")

	groups := []struct {
		name string
		rows [][2]string
	}{
		{
			name: "Panes",
			rows: [][2]string{
				{"p", "Toggle Projects"},
				{"d", "Toggle Details"},
				{"a", "Toggle Logs"},
				{"t", "Toggle wrap for Details and Logs"},
			},
		},
		{
			name: "Create",
			rows: [][2]string{
				{"c", "Create project or service (context-sensitive)"},
				{"e", "Edit selected project or service"},
				{"backspace", "Delete selected project or service"},
			},
		},
		{
			name: "Services",
			rows: [][2]string{
				{"s", "Start / stop the selected project or service"},
				{"r", "Restart selected service, or all services from Projects"},
				{"i", "Toggle ignore for the selected service (ignored services are not auto-started)"},
			},
		},
		{
			name: "Navigation",
			rows: [][2]string{
				{"h/l (←/→)", "Move focus between panes"},
				{"j/k (↓/↑)", "Move selection within focused pane"},
			},
		},
		{
			name: "Help / Quit",
			rows: [][2]string{
				{"?", "Show/close this help"},
				{"esc", "Close help"},
				{"q", "Quit"},
			},
		},
	}

	bodyLines := make([]string, 0, 32)
	bodyLines = append(bodyLines, header, "")

	keyStyle := lipgloss.NewStyle().Bold(true).Foreground(fg)
	groupTitleStyle := lipgloss.NewStyle().Foreground(colorTitleFocused).Bold(true)

	for gi, g := range groups {
		if gi > 0 {
			bodyLines = append(bodyLines, "")
		}
		bodyLines = append(bodyLines, groupTitleStyle.Render(g.name))

		keyW := 0
		for _, r := range g.rows {
			keyW = max(keyW, ansi.StringWidth(r[0]))
		}

		for _, r := range g.rows {
			left := keyStyle.Render(r[0])
			pad := keyW - ansi.StringWidth(r[0])
			if pad < 0 {
				pad = 0
			}
			line := left + strings.Repeat(" ", pad) + "  " + r[1]
			bodyLines = append(bodyLines, line)
		}
	}

	body := strings.Join(bodyLines, "\n")

	box := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(colorBorderFocused).
		Background(appBg).
		Foreground(fg).
		Padding(1, 2).
		Width(maxBoxW).
		Render(body)

	for strings.HasSuffix(box, "\n") {
		box = strings.TrimSuffix(box, "\n")
	}
	return box
}

func overlayBoxCentered(w, h int, base, box string) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	base = lipgloss.Place(w, h, lipgloss.Left, lipgloss.Top, base)
	box = strings.TrimSuffix(box, "\n")
	bw, bh := lipgloss.Width(box), lipgloss.Height(box)
	if bw <= 0 || bh <= 0 {
		return base
	}

	x := (w - bw) / 2
	y := (h - bh) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	bLines := strings.Split(base, "\n")
	boxLines := strings.Split(box, "\n")
	for i := 0; i < bh && (y+i) < len(bLines); i++ {
		ins := lipgloss.Place(bw, 1, lipgloss.Left, lipgloss.Top, boxLines[i])
		left := ansi.Cut(bLines[y+i], 0, x)
		right := ansi.Cut(bLines[y+i], x+bw, w)
		bLines[y+i] = left + ins + right
	}

	return strings.Join(bLines, "\n")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

const helpText = `hangar – terminal dashboard TUI

USAGE
  hangar [--help]

DESCRIPTION
  hangar launches an interactive terminal UI with four navigation panes:

    Projects   Left column  – list of projects to browse
    Services   Center       – services belonging to the selected project
    Details    Top-right    – metadata for the selected item
    Logs       Bottom-right – live log stream for the selected service

NAVIGATION
  h / ←    Move focus to the left pane
  l / →    Move focus to the right pane
  j / ↓    Move selection down within the focused pane
  k / ↑    Move selection up within the focused pane

TOGGLES
  p        Show / hide the Projects pane
  d        Show / hide the Details pane
  a        Show / hide the Logs pane
  t        Toggle wrapped text in Details and Logs
  e        Edit the selected project or service
  s        Start / stop the selected project or service
  r        Restart the selected service, or restart all services from Projects
  ?        Show / hide the in-app hotkey help overlay

GENERAL
  q        Quit
  Ctrl+C   Quit
  Esc      Close the help overlay

OPTIONS
  --help   Print this help message and exit
`

func main() {
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--help", "-help", "-h":
			fmt.Print(helpText)
			return
		case "--version", "-v":
			fmt.Println("v" + version)
			return
		}
	}

	p := tea.NewProgram(newModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
