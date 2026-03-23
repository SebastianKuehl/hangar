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
	runtimeRequest   int
	runtimeManager   *hangarruntime.Manager
	logTailer        *logstream.Tailer
	logEvents        chan tea.Msg
	logLines         map[string][]string
	logCancel        context.CancelFunc
	logListenCtx     context.Context
	logListenCancel  context.CancelFunc
	logWatchID       int
	followingService string
	modal            formModal
	errMsg           string
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
func serviceItems(project Project, runtime []serviceRuntime, states map[string]serviceTransition) []string {
	out := make([]string, 0, len(project.Services))
	for i, s := range project.Services {
		icon := "◌"
		if _, pending := states[serviceKey(project, s)]; pending {
			icon = "◔"
		} else if i < len(runtime) && runtime[i].known {
			if runtime[i].running {
				icon = "●"
			} else {
				icon = "○"
			}
		}
		out = append(out, icon+" "+s.Name)
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
	return m
}

func (m model) Init() tea.Cmd {
	return tea.Batch(nextRuntimeRefreshCmd(), m.startRuntimeRefresh())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case configSavedMsg:
		m.cfg = msg.cfg
		m.serviceRuntime = nil
		m.syncSelectionState()
		m.errMsg = ""
		return m, tea.Batch(m.startRuntimeRefresh(), m.restartLogTail())

	case configErrMsg:
		m.errMsg = "Error saving config: " + msg.err.Error()
		return m, nil

	case runtimeRefreshMsg:
		project, ok := m.selectedProject()
		if !ok || msg.projectIndex != m.projects.selected || msg.requestID != m.runtimeRequest || msg.projectPath != project.Path || msg.serviceCount != len(project.Services) {
			return m, nil
		}
		if msg.err != nil {
			m.errMsg = "Error detecting runtime state: " + msg.err.Error()
			m.advanceServiceTransitionPollsOnError(project)
			m.syncSelectionState()
			return m, nil
		}
		m.serviceRuntime = msg.runtime
		m.errMsg = ""
		m.reconcileServiceTransitions(project, msg.runtime)
		m.syncSelectionState()
		return m, m.ensureLogTail()

	case serviceControlMsg:
		if msg.err != nil {
			delete(m.serviceStates, msg.serviceKey)
			m.errMsg = msg.err.Error()
			m.syncSelectionState()
			return m, nil
		}
		if msg.projectIndex == m.projects.selected {
			return m, tea.Batch(m.startRuntimeRefresh(), m.ensureLogTail())
		}
		return m, nil

	case runtimeTickMsg:
		return m, tea.Batch(nextRuntimeRefreshCmd(), m.startRuntimeRefresh())

	case logChunkMsg:
		if msg.watchID != m.logWatchID || msg.serviceKey != m.followingService {
			return m, listenForLogMessage(m.logEvents, m.logListenCtx)
		}
		if msg.reset {
			m.logLines[msg.serviceKey] = append([]string(nil), msg.lines...)
		} else {
			m.logLines[msg.serviceKey] = trimLogLines(append(m.logLines[msg.serviceKey], msg.lines...))
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

		if m.modal.isOpen() {
			submit, closeModal := m.modal.handleKey(k)
			if closeModal {
				m.modal.close()
				return m, nil
			}
			if submit {
				name := m.modal.name()
				path := m.modal.path()
				mode := m.modal.mode
				m.modal.close()
				if mode == modalCreateProject {
					return m, saveProjectCmd(name, path)
				}
				return m, saveServiceCmd(m.projects.selected, name, path)
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
					projectName := m.cfg.Projects[m.projects.selected].Name
					m.modal.openCreateService(projectName)
				}
			}
			return m, nil
		case "p":
			m.projects.visible = !m.projects.visible
			m.ensureFocusVisible()
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
		case "s":
			project, ok := m.selectedProject()
			if !ok {
				return m, nil
			}
			service, ok := m.selectedService()
			if !ok {
				return m, nil
			}
			runtime := m.selectedServiceRuntime()
			if !runtime.known {
				m.errMsg = "Wait for runtime detection before toggling a service."
				return m, nil
			}
			key := serviceKey(project, service)
			if _, busy := m.serviceStates[key]; busy {
				return m, nil
			}
			m.serviceStates[key] = serviceTransition{targetRunning: !runtime.running}
			m.errMsg = ""
			m.syncSelectionState()
			if runtime.running {
				return m, stopServiceCmd(m.projects.selected, project, service)
			}
			return m, startServiceCmd(m.projects.selected, project, service)
		case "h", "left":
			m.focusPrev()
			return m, nil
		case "l", "right":
			m.focusNext()
			return m, nil
		case "j", "down":
			m.moveSelection(1)
			if m.focus == paneProjects {
				m.services.selected = 0
				m.serviceRuntime = nil
				m.syncSelectionState()
				return m, tea.Batch(m.startRuntimeRefresh(), m.restartLogTail())
			}
			m.syncSelectionState()
			if m.focus == paneServices {
				return m, tea.Batch(m.startRuntimeRefresh(), m.restartLogTail())
			}
			return m, nil
		case "k", "up":
			m.moveSelection(-1)
			if m.focus == paneProjects {
				m.services.selected = 0
				m.serviceRuntime = nil
				m.syncSelectionState()
				return m, tea.Batch(m.startRuntimeRefresh(), m.restartLogTail())
			}
			m.syncSelectionState()
			if m.focus == paneServices {
				return m, tea.Batch(m.startRuntimeRefresh(), m.restartLogTail())
			}
			return m, nil
		}
	}

	return m, nil
}

func (m *model) startRuntimeRefresh() tea.Cmd {
	project, ok := m.selectedProject()
	if !ok {
		return nil
	}
	m.runtimeRequest++
	return refreshProjectRuntimeCmd(m.runtimeRequest, m.projects.selected, project)
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
	m.services.items = serviceItems(project, m.serviceRuntime, m.serviceStates)
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
		return
	}
	m.logs.placeholder = ""
	m.logs.items = serviceLogItems(project, service, runtime, transition)
}

func (m model) selectedProject() (Project, bool) {
	if m.projects.selected < 0 || m.projects.selected >= len(m.cfg.Projects) {
		return Project{}, false
	}
	return m.cfg.Projects[m.projects.selected], true
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
		if i < len(runtime) && runtime[i].known && runtime[i].running == transition.targetRunning {
			delete(m.serviceStates, key)
			continue
		}
		transition.polls++
		if transition.polls >= maxServiceTransitionPolls {
			delete(m.serviceStates, key)
			m.errMsg = fmt.Sprintf("Timed out while %s %s. Press s to try again.", transition.label(), service.Name)
			continue
		}
		m.serviceStates[key] = transition
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
			m.errMsg = fmt.Sprintf("Timed out while %s %s. Press s to try again.", transition.label(), service.Name)
			continue
		}
		m.serviceStates[key] = transition
	}
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
				"Hotkeys: h/l focus, j/k move, p/d/a toggle panes, t wrap, c create, s start/stop, ? help, q quit.")
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

		out = lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", gap), mid, strings.Repeat(" ", gap), right)
		return clampToViewport(m.width, m.height, out+statusBar)
	}

	col2 := m.width / 3
	col3 := m.width - col2 - gap
	mid := m.renderListPane(m.services, col2, contentH, m.focus == paneServices, serviceHighlight, false)
	right := m.renderRightColumn(col3, contentH)
	out = lipgloss.JoinHorizontal(lipgloss.Top, mid, strings.Repeat(" ", gap), right)
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

	lines := make([]string, 0, 2+len(p.items))
	if p.placeholder != "" {
		lines = append(lines, renderRows(faintStyle, innerW, p.placeholder, wrap)...)
		lines = append(lines, renderRows(lipgloss.NewStyle(), innerW, "", false)...)
	}

	plainLine := lipgloss.NewStyle()
	for i, it := range p.items {
		prefix := "  "
		if i == p.selected {
			prefix = "• "
			if focused || highlightSel {
				prefix = "> "
			}
		}

		line := prefix + it
		if i == p.selected {
			switch {
			case focused:
				lines = append(lines, renderRows(selectedFocusedStyle, innerW, line, wrap)...)
			case highlightSel:
				lines = append(lines, renderRows(selectedContextStyle, innerW, line, wrap)...)
			default:
				lines = append(lines, renderRows(selectedStyle, innerW, line, wrap)...)
			}
			continue
		}
		lines = append(lines, renderRows(plainLine, innerW, line, wrap)...)
	}

	// lipgloss.Style.Height() sets a minimum, not a maximum. Clamp and pad the
	// number of rendered lines so panes always fit their allocated viewport.
	if innerH == 0 {
		lines = nil
	} else if len(lines) > innerH {
		lines = lines[:innerH]
		lines[innerH-1] = faintStyle.Width(innerW).Render("…")
	} else {
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
			},
		},
		{
			name: "Services",
			rows: [][2]string{
				{"s", "Start / stop the selected service"},
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
  s        Start the selected service when stopped, or stop it when running
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
