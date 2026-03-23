package main

import (
	"context"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// modalMode distinguishes which form is active.
type modalMode int

const (
	modalNone modalMode = iota
	modalCreateProject
	modalEditProject
	modalCreateService
	modalEditService
)

type formFieldKind int

const (
	fieldText formFieldKind = iota
	fieldSelect
)

// formField holds one labelled form slot.
type formField struct {
	label       string
	value       string
	required    bool
	kind        formFieldKind
	options     []string
	optionIndex int
	customValue string
}

func newSelectField(label string, required bool, options []string, selected string) formField {
	field := formField{
		label:    label,
		required: required,
		kind:     fieldSelect,
	}
	field.setOptions(options, selected)
	return field
}

func newCommandField(label string, required bool, options []string, command string) formField {
	field := formField{
		label:    label,
		required: required,
		kind:     fieldSelect,
	}
	field.setCommandOptions(options, command)
	return field
}

func (f *formField) setOptions(options []string, selected string) {
	if len(options) == 0 {
		options = []string{""}
	}
	f.options = append([]string(nil), options...)
	f.optionIndex = 0

	selected = strings.TrimSpace(selected)
	if selected != "" {
		for i, option := range f.options {
			if option == selected {
				f.optionIndex = i
				f.value = option
				return
			}
		}
		f.options = append(f.options, selected)
		f.optionIndex = len(f.options) - 1
	}

	f.value = f.options[f.optionIndex]
}

func (f *formField) setCommandOptions(options []string, command string) {
	f.options = append([]string(nil), options...)
	f.options = append(f.options, "__custom__")
	f.optionIndex = 0
	f.value = ""
	f.customValue = ""

	command = strings.TrimSpace(command)
	if command == "" {
		if len(f.options) > 0 {
			f.value = f.options[f.optionIndex]
		}
		return
	}

	for i, option := range f.options {
		if option == command {
			f.optionIndex = i
			f.value = option
			return
		}
	}

	f.optionIndex = len(f.options) - 1
	f.value = "__custom__"
	f.customValue = command
}

func (f *formField) cycleOption(step int) {
	if f.kind != fieldSelect || len(f.options) == 0 {
		return
	}
	f.optionIndex = (f.optionIndex + step + len(f.options)) % len(f.options)
	f.value = f.options[f.optionIndex]
}

func (f formField) clone() formField {
	cloned := f
	if f.options != nil {
		cloned.options = append([]string(nil), f.options...)
	}
	return cloned
}

func (f *formField) selectedOption() string {
	if f.kind != fieldSelect || f.optionIndex < 0 || f.optionIndex >= len(f.options) {
		return ""
	}
	return f.options[f.optionIndex]
}

func (f *formField) isCustomSelected() bool {
	return f.selectedOption() == "__custom__"
}

// formModal tracks all state for the project / service overlay.
type formModal struct {
	mode        modalMode
	fields      []formField
	activeField int
	errMsg      string
	project     Project
	projectName string
}

// isOpen reports whether any modal is currently visible.
func (f *formModal) isOpen() bool {
	return f.mode != modalNone
}

func (f *formModal) isServiceMode() bool {
	return f.mode == modalCreateService || f.mode == modalEditService
}

func (f *formModal) isEditMode() bool {
	return f.mode == modalEditProject || f.mode == modalEditService
}

// openCreateProject resets the modal for project creation.
func (f *formModal) openCreateProject() {
	*f = formModal{
		mode: modalCreateProject,
		fields: []formField{
			{label: "Project name", required: true},
			{label: "Project path (optional)"},
		},
	}
}

// openEditProject resets the modal for project editing.
func (f *formModal) openEditProject(project Project) {
	*f = formModal{
		mode: modalEditProject,
		fields: []formField{
			{label: "Project name", value: project.Name, required: true},
			{label: "Project path (optional)", value: project.Path},
		},
	}
}

// openCreateService resets the modal for service creation within a project.
func (f *formModal) openCreateService(project Project) {
	pathLabel := "Path (optional)"
	pathRequired := strings.TrimSpace(project.Path) == ""
	if pathRequired {
		pathLabel = "Path"
	}
	*f = formModal{
		mode:        modalCreateService,
		project:     project,
		projectName: project.Name,
		fields: []formField{
			{label: "Service name", required: true},
			{label: pathLabel, required: pathRequired},
			{label: "Custom command", required: true},
		},
	}
	f.syncServiceCommandField("")
}

// openEditService resets the modal for service editing within a project.
func (f *formModal) openEditService(project Project, service Service) {
	*f = formModal{
		mode:        modalEditService,
		project:     project,
		projectName: project.Name,
		fields: []formField{
			{label: "Service name", value: service.Name, required: true},
			{label: "Path (optional)", value: service.Path},
			newCommandField("Command", true, nil, service.Command),
		},
	}
	f.syncServiceCommandField(service.Command)
}

func (f *formModal) commandField() *formField {
	if !f.isServiceMode() || len(f.fields) < 3 {
		return nil
	}
	return &f.fields[2]
}

func (f *formModal) selectedCommand() string {
	field := f.commandField()
	if field == nil || field.isCustomSelected() {
		return ""
	}
	return strings.TrimSpace(field.selectedOption())
}

func (f *formModal) customCommand() string {
	field := f.commandField()
	if field == nil {
		return ""
	}
	if !field.isCustomSelected() {
		return ""
	}
	return strings.TrimSpace(field.customValue)
}

func (f *formModal) syncServiceCommandField(existingCommand string) {
	if !f.isServiceMode() || len(f.fields) < 3 {
		return
	}

	commandField := &f.fields[2]
	existingCustom := commandField.customValue
	if existingCustom == "" {
		existingCustom = strings.TrimSpace(existingCommand)
	}

	options := []string(nil)
	if strings.TrimSpace(f.path()) != "" {
		options = serviceCommandOptions(f.project, f.path(), "")
	}

	selected := commandField.selectedOption()
	command := strings.TrimSpace(existingCommand)
	if commandField.isCustomSelected() {
		command = existingCustom
	} else if selected != "" && selected != "__custom__" {
		command = selected
	}

	commandField.label = "Command"
	commandField.required = true
	commandField.kind = fieldSelect
	commandField.setCommandOptions(options, command)
}

// close dismisses the modal without submitting.
func (f *formModal) close() {
	f.mode = modalNone
}

// handleKey processes a key event while the modal is open.
// Returns (shouldSubmit, shouldClose).
func (f *formModal) handleKey(k string) (submit bool, close bool) {
	if f.activeField >= 0 && f.activeField < len(f.fields) {
		cur := &f.fields[f.activeField]
		if cur.kind == fieldSelect && cur.isCustomSelected() {
			switch k {
			case "backspace":
				if len(cur.customValue) == 0 {
					return false, false
				}
				runes := []rune(cur.customValue)
				cur.customValue = string(runes[:len(runes)-1])
				return false, false
			case "left", "right":
				f.errMsg = ""
				if k == "left" {
					cur.cycleOption(-1)
				} else {
					cur.cycleOption(1)
				}
				return false, false
			default:
				if utf8.RuneCountInString(k) == 1 {
					cur.customValue += k
					f.errMsg = ""
					return false, false
				}
			}
		}
	}

	switch k {
	case "esc":
		return false, true
	case "tab", "down":
		f.errMsg = ""
		if f.fields[f.activeField].kind == fieldSelect && f.fields[f.activeField].optionIndex < len(f.fields[f.activeField].options)-1 {
			f.fields[f.activeField].cycleOption(1)
			return false, false
		}
		f.activeField = (f.activeField + 1) % len(f.fields)
	case "shift+tab", "up":
		f.errMsg = ""
		if f.fields[f.activeField].kind == fieldSelect && f.fields[f.activeField].optionIndex > 0 {
			f.fields[f.activeField].cycleOption(-1)
			return false, false
		}
		f.activeField = (f.activeField - 1 + len(f.fields)) % len(f.fields)
	case "left", "k":
		if f.fields[f.activeField].kind == fieldSelect {
			f.errMsg = ""
			f.fields[f.activeField].cycleOption(-1)
		}
	case "right", "j", " ":
		if f.fields[f.activeField].kind == fieldSelect {
			f.errMsg = ""
			f.fields[f.activeField].cycleOption(1)
		}
	case "enter":
		if f.activeField < len(f.fields)-1 {
			f.activeField++
			return false, false
		}
		for _, fld := range f.fields {
			if fld.kind == fieldSelect && fld.required && fld.isCustomSelected() && strings.TrimSpace(fld.customValue) == "" {
				f.errMsg = "\"" + fld.label + "\" is required."
				return false, false
			}
			if fld.kind != fieldSelect && fld.required && strings.TrimSpace(fld.value) == "" {
				f.errMsg = "\"" + fld.label + "\" is required."
				return false, false
			}
		}
		return true, false
	case "backspace":
		cur := &f.fields[f.activeField]
		if cur.kind == fieldSelect && cur.isCustomSelected() {
			if len(cur.customValue) == 0 {
				return false, false
			}
			runes := []rune(cur.customValue)
			cur.customValue = string(runes[:len(runes)-1])
			return false, false
		}
		if cur.kind != fieldText || len(cur.value) == 0 {
			return false, false
		}
		runes := []rune(cur.value)
		cur.value = string(runes[:len(runes)-1])
		if f.isServiceMode() && f.activeField == 1 {
			f.syncServiceCommandField("")
		}
	default:
		cur := &f.fields[f.activeField]
		if cur.kind == fieldSelect && cur.isCustomSelected() && utf8.RuneCountInString(k) == 1 {
			cur.customValue += k
			return false, false
		}
		if cur.kind == fieldText && utf8.RuneCountInString(k) == 1 {
			cur.value += k
			if f.isServiceMode() && f.activeField == 1 {
				f.syncServiceCommandField("")
			}
		}
	}
	return false, false
}

// name returns the trimmed value of the first field (always the name).
func (f *formModal) name() string {
	if len(f.fields) == 0 {
		return ""
	}
	return strings.TrimSpace(f.fields[0].value)
}

// path returns the trimmed value of the second field (always the path).
func (f *formModal) path() string {
	if len(f.fields) < 2 {
		return ""
	}
	return strings.TrimSpace(f.fields[1].value)
}

// command returns the custom command when present, otherwise the selected runtime command.
func (f *formModal) command() string {
	if custom := f.customCommand(); custom != "" {
		return custom
	}
	return f.selectedCommand()
}

// configErrMsg is sent to the model when a config I/O error occurs.
type configErrMsg struct{ err error }

// configSavedMsg is sent after a successful save, carrying the updated config.
type configSavedMsg struct{ cfg Config }

type serviceUpdatedMsg struct {
	cfg              Config
	projectIndex     int
	serviceIndex     int
	previousProject  Project
	previousService  Service
	previousRuntime  serviceRuntime
	previousOwnedPID int32
}

type serviceRestartMsg struct {
	projectIndex  int
	serviceIndex  int
	oldServiceKey string
	newServiceKey string
	startedPID    int32
	err           error
}

// ---- rendering ----------------------------------------------------------------

var (
	modalBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.DoubleBorder()).
				BorderForeground(colorBorderFocused).
				Background(lipgloss.Color("#30363d")).
				Foreground(lipgloss.Color("#c9d1d9")).
				Padding(1, 2)

	fieldLabelStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#c9d1d9"))

	fieldActiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#ffffff")).
				Background(lipgloss.Color("#238636")).
				Width(36)

	fieldInactiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#c9d1d9")).
				Background(lipgloss.Color("#21262d")).
				Width(36)

	fieldRequiredMark = lipgloss.NewStyle().Foreground(lipgloss.Color("#f85149")).Render(" *")

	modalErrStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#f85149")).Bold(true)
	modalHintStyle = lipgloss.NewStyle().Faint(true)
)

func (m model) renderModal(screenW, screenH int) string {
	f := &m.modal

	title := "Create Project"
	switch f.mode {
	case modalEditProject:
		title = "Edit Project"
	case modalCreateService:
		title = "Create Service"
	case modalEditService:
		title = "Edit Service"
	}

	lines := []string{
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7ee787")).Render(title),
		"",
	}

	if f.isServiceMode() && f.projectName != "" {
		projectLabelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#8b949e"))
		projectValueStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#c9d1d9"))
		lines = append(lines,
			projectLabelStyle.Render("Project: ")+projectValueStyle.Render(f.projectName),
			"",
		)
	}

	for i, fld := range f.fields {
		label := fieldLabelStyle.Render(fld.label)
		if fld.required {
			label += fieldRequiredMark
		}
		lines = append(lines, label)

		if fld.kind == fieldSelect {
			for optionIndex, option := range fld.options {
				marker := "○ "
				if optionIndex == fld.optionIndex {
					marker = "◉ "
				}
				optionLabel := option
				if option == "__custom__" {
					cursor := ""
					if i == f.activeField && optionIndex == fld.optionIndex {
						cursor = "▌"
					}
					optionLabel = "Custom: " + fld.customValue + cursor
				}
				display := ansi.Truncate(marker+optionLabel, 35, "…")
				style := fieldInactiveStyle
				if i == f.activeField && optionIndex == fld.optionIndex {
					style = fieldActiveStyle
				}
				lines = append(lines, style.Render(display))
			}
			lines = append(lines, "")
			continue
		}

		cursor := " "
		if i == f.activeField {
			cursor = "▌"
		}
		display := ansi.Truncate(fld.value+cursor, 35, "…")

		style := fieldInactiveStyle
		if i == f.activeField {
			style = fieldActiveStyle
		}
		lines = append(lines, style.Render(display), "")
	}

	if f.errMsg != "" {
		lines = append(lines, modalErrStyle.Render(f.errMsg), "")
	}

	lines = append(lines,
		modalHintStyle.Render("tab/↑↓ switch field  •  j/k or ←/→ choose option  •  enter submit  •  esc cancel"),
	)

	body := strings.Join(lines, "\n")
	box := modalBorderStyle.Render(body)
	for strings.HasSuffix(box, "\n") {
		box = strings.TrimSuffix(box, "\n")
	}
	return box
}

// saveProjectCmd returns a tea.Cmd that persists a new project asynchronously.
func saveProjectCmd(name, path string) tea.Cmd {
	return func() tea.Msg {
		cfg, err := addProject(name, path)
		if err != nil {
			return configErrMsg{err}
		}
		return configSavedMsg{cfg}
	}
}

func updateProjectCmd(projectIndex int, name, path string) tea.Cmd {
	return func() tea.Msg {
		cfg, err := updateProject(projectIndex, name, path)
		if err != nil {
			return configErrMsg{err}
		}
		return configSavedMsg{cfg}
	}
}

// saveServiceCmd returns a tea.Cmd that persists a new service asynchronously.
func saveServiceCmd(projectIndex int, name, path, command string) tea.Cmd {
	return func() tea.Msg {
		cfg, err := addService(projectIndex, name, path, command)
		if err != nil {
			return configErrMsg{err}
		}
		return configSavedMsg{cfg}
	}
}

func updateServiceCmd(projectIndex, serviceIndex int, name, path, command string, previousProject Project, previousService Service, previousRuntime serviceRuntime, previousOwnedPID int32) tea.Cmd {
	return func() tea.Msg {
		cfg, err := updateService(projectIndex, serviceIndex, name, path, command)
		if err != nil {
			return configErrMsg{err}
		}
		return serviceUpdatedMsg{
			cfg:              cfg,
			projectIndex:     projectIndex,
			serviceIndex:     serviceIndex,
			previousProject:  previousProject,
			previousService:  previousService,
			previousRuntime:  previousRuntime,
			previousOwnedPID: previousOwnedPID,
		}
	}
}

// deleteProjectCmd returns a tea.Cmd that removes a project asynchronously.
func deleteProjectCmd(projectIndex int) tea.Cmd {
	return func() tea.Msg {
		cfg, err := deleteProject(projectIndex)
		if err != nil {
			return configErrMsg{err}
		}
		return configSavedMsg{cfg}
	}
}

// deleteServiceCmd returns a tea.Cmd that removes a service asynchronously.
func deleteServiceCmd(projectIndex, serviceIndex int) tea.Cmd {
	return func() tea.Msg {
		cfg, err := deleteService(projectIndex, serviceIndex)
		if err != nil {
			return configErrMsg{err}
		}
		return configSavedMsg{cfg}
	}
}

// ---- confirm modal ------------------------------------------------------------

// confirmAction identifies what action the confirm modal is confirming.
type confirmAction int

const (
	confirmNone confirmAction = iota
	confirmDeleteProject
	confirmDeleteService
)

// confirmModal is a lightweight yes/no dialog.
// selected: 0 = No (default), 1 = Yes.
type confirmModal struct {
	action       confirmAction
	message      string
	selected     int // 0=No, 1=Yes
	projectIndex int
	serviceIndex int
}

func (c *confirmModal) isOpen() bool {
	return c.action != confirmNone
}

func (c *confirmModal) open(action confirmAction, message string, projectIndex, serviceIndex int) {
	*c = confirmModal{
		action:       action,
		message:      message,
		selected:     0, // No is the default
		projectIndex: projectIndex,
		serviceIndex: serviceIndex,
	}
}

func (c *confirmModal) close() {
	c.action = confirmNone
}

// handleKey handles a key event for the confirm modal.
// Returns (confirmed, closed).
func (c *confirmModal) handleKey(k string) (confirmed bool, closed bool) {
	switch k {
	case "y", "Y":
		return true, true
	case "n", "N", "esc":
		return false, true
	case "left", "h":
		c.selected = 0 // No
	case "right", "l":
		c.selected = 1 // Yes
	case "enter":
		return c.selected == 1, true
	}
	return false, false
}

var (
	confirmBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.DoubleBorder()).
				BorderForeground(lipgloss.Color("#f85149")).
				Background(lipgloss.Color("#30363d")).
				Foreground(lipgloss.Color("#c9d1d9")).
				Padding(1, 2)

	confirmBtnActiveStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#ffffff")).
				Background(lipgloss.Color("#238636")).
				Padding(0, 2)

	confirmBtnDangerStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#ffffff")).
				Background(lipgloss.Color("#da3633")).
				Padding(0, 2)

	confirmBtnInactiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#8b949e")).
				Background(lipgloss.Color("#21262d")).
				Padding(0, 2)
)

func (c *confirmModal) render() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f85149")).Render("⚠  Confirm Deletion")

	var noBtn, yesBtn string
	if c.selected == 0 {
		noBtn = confirmBtnActiveStyle.Render("No")
		yesBtn = confirmBtnInactiveStyle.Render("Yes")
	} else {
		noBtn = confirmBtnInactiveStyle.Render("No")
		yesBtn = confirmBtnDangerStyle.Render("Yes")
	}

	buttons := lipgloss.JoinHorizontal(lipgloss.Top, noBtn, "  ", yesBtn)
	hint := modalHintStyle.Render("←/→ or h/l choose  •  y/n quick-confirm  •  enter confirm  •  esc cancel")

	body := strings.Join([]string{
		title,
		"",
		c.message,
		"",
		buttons,
		"",
		hint,
	}, "\n")

	box := confirmBorderStyle.Render(body)
	for strings.HasSuffix(box, "\n") {
		box = strings.TrimSuffix(box, "\n")
	}
	return box
}

func restartEditedServiceCmd(projectIndex, serviceIndex int, oldProject Project, oldService Service, oldRuntime serviceRuntime, oldOwnedPID int32, newProject Project, newService Service) tea.Cmd {
	return func() tea.Msg {
		oldKey := serviceKey(oldProject, oldService)
		newKey := serviceKey(newProject, newService)
		mgr, err := newRuntimeManager()
		if err != nil {
			return serviceRestartMsg{projectIndex: projectIndex, serviceIndex: serviceIndex, oldServiceKey: oldKey, newServiceKey: newKey, err: err}
		}
		if oldRuntime.running {
			if err := mgr.StopService(context.Background(), runtimeServiceConfig(oldProject, oldService)); err != nil {
				return serviceRestartMsg{projectIndex: projectIndex, serviceIndex: serviceIndex, oldServiceKey: oldKey, newServiceKey: newKey, err: err}
			}
		}
		rt, err := mgr.StartService(context.Background(), runtimeServiceConfig(newProject, newService))
		return serviceRestartMsg{projectIndex: projectIndex, serviceIndex: serviceIndex, oldServiceKey: oldKey, newServiceKey: newKey, startedPID: int32(rt.PID), err: err}
	}
}
