package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	f.optionIndex = 0
	f.value = ""
	f.customValue = ""

	if len(f.options) == 0 {
		f.kind = fieldText
		f.value = command
		return
	}

	f.kind = fieldSelect
	command = strings.TrimSpace(command)
	if command == "" {
		f.value = f.options[f.optionIndex]
		return
	}

	for i, option := range f.options {
		if option == command {
			f.optionIndex = i
			f.value = option
			return
		}
	}

	f.value = command
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

// serviceSuggestion holds a display name and the actual service data.
type serviceSuggestion struct {
	displayName string
	name        string
	path        string
	command     string
}

// formModal tracks all state for the project / service overlay.
type formModal struct {
	mode        modalMode
	fields      []formField
	activeField int
	errMsg      string
	project     Project
	projectName string
	suggestions []serviceSuggestion
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
func (f *formModal) openCreateService(project Project, cfg Config) {
	suggestions := discoverAvailableServices(project, cfg)

	fields := []formField{
		{label: "Service name", required: true},
		{label: "Path (optional)", required: false},
		{kind: fieldText, label: "Command", required: true, value: ""},
	}

	if len(suggestions) > 0 {
		displayOptions := make([]string, len(suggestions))
		for i, s := range suggestions {
			displayOptions[i] = s.displayName
		}
		fields = append(fields, newSelectField("Suggestions", false, displayOptions, ""))
	}

	*f = formModal{
		mode:        modalCreateService,
		project:     project,
		projectName: project.Name,
		fields:      fields,
		suggestions: suggestions,
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
			newSelectField("Ignore", false, []string{"no", "yes"}, boolToYesNo(service.Ignored)),
		},
	}
	f.syncServiceCommandField(service.Command)
}

func boolToYesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func (f *formModal) commandField() *formField {
	if !f.isServiceMode() || len(f.fields) < 3 {
		return nil
	}
	return &f.fields[2]
}

func (f *formModal) selectedCommand() string {
	field := f.commandField()
	if field == nil {
		return ""
	}
	if field.kind == fieldText {
		return strings.TrimSpace(field.value)
	}
	return strings.TrimSpace(field.selectedOption())
}

func (f *formModal) syncServiceCommandField(existingCommand string) {
	if !f.isServiceMode() || len(f.fields) < 3 {
		return
	}

	commandField := &f.fields[2]

	options := []string(nil)
	if strings.TrimSpace(f.path()) != "" {
		options = serviceCommandOptions(f.project, f.path(), "")
	}

	command := strings.TrimSpace(existingCommand)
	if commandField.kind == fieldSelect && commandField.optionIndex >= 0 && commandField.optionIndex < len(commandField.options) {
		selected := commandField.selectedOption()
		if selected != "" {
			command = selected
		}
	} else if commandField.kind == fieldText {
		command = commandField.value
	}

	commandField.label = "Command"
	commandField.required = true
	commandField.setCommandOptions(options, command)
}

func (f *formModal) applySuggestion() {
	if f.mode != modalCreateService || len(f.fields) < 4 || len(f.suggestions) == 0 {
		return
	}

	suggestionField := &f.fields[3]
	if suggestionField.optionIndex < 0 || suggestionField.optionIndex >= len(f.suggestions) {
		return
	}

	f.errMsg = ""

	suggestion := f.suggestions[suggestionField.optionIndex]
	f.fields[0].value = suggestion.name
	f.fields[1].value = suggestion.path

	f.syncServiceCommandField(suggestion.command)
}

// close dismisses the modal without submitting.
func (f *formModal) close() {
	f.mode = modalNone
}

// handleKey processes a key event while the modal is open.
// Returns (shouldSubmit, shouldClose).
func (f *formModal) handleKey(k string) (submit bool, close bool) {
	switch k {
	case "esc":
		return false, true
	case "tab", "down":
		f.errMsg = ""
		if f.fields[f.activeField].kind == fieldSelect && f.fields[f.activeField].optionIndex < len(f.fields[f.activeField].options)-1 {
			f.fields[f.activeField].cycleOption(1)
			return false, false
		}
		if f.isServiceMode() && f.activeField == 3 {
			f.fields[3].optionIndex = 0
		}
		f.activeField = (f.activeField + 1) % len(f.fields)
	case "shift+tab", "up":
		f.errMsg = ""
		if f.fields[f.activeField].kind == fieldSelect && f.fields[f.activeField].optionIndex > 0 {
			f.fields[f.activeField].cycleOption(-1)
			return false, false
		}
		if f.isServiceMode() && f.activeField == 3 {
			f.fields[3].optionIndex = 0
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
		if f.isServiceMode() && f.activeField == 3 && len(f.fields) >= 4 && len(f.suggestions) > 0 {
			suggestionField := &f.fields[3]
			if suggestionField.optionIndex >= 0 && suggestionField.optionIndex < len(f.suggestions) {
				suggestion := f.suggestions[suggestionField.optionIndex]
				f.fields[0].value = suggestion.name
				f.fields[1].value = suggestion.path
				command := f.inferServiceCommand(suggestion.path)
				f.syncServiceCommandField(command)
				return true, false
			}
		}
		if f.activeField < len(f.fields)-1 {
			f.activeField++
			return false, false
		}
		for _, fld := range f.fields {
			if fld.kind != fieldSelect && fld.required && strings.TrimSpace(fld.value) == "" {
				f.errMsg = "\"" + fld.label + "\" is required."
				return false, false
			}
		}
		return true, false
	case "backspace":
		cur := &f.fields[f.activeField]
		if cur.kind != fieldText || len(cur.value) == 0 {
			return false, false
		}
		runes := []rune(cur.value)
		cur.value = string(runes[:len(runes)-1])
	default:
		cur := &f.fields[f.activeField]
		if cur.kind == fieldText && utf8.RuneCountInString(k) == 1 {
			cur.value += k
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

// command returns the selected or custom command.
func (f *formModal) command() string {
	return f.selectedCommand()
}

// ignored returns whether the Ignore toggle is set to "yes" in the edit service form.
func (f *formModal) ignored() bool {
	if f.mode != modalEditService || len(f.fields) < 4 {
		return false
	}
	return f.fields[3].selectedOption() == "yes"
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

func (f *formModal) getSuggestionBaseName(index int) string {
	if index < 0 || index >= len(f.suggestions) {
		return ""
	}
	name := f.suggestions[index].name
	if idx := strings.Index(name, ":"); idx != -1 {
		return name[:idx]
	}
	return name
}

func (f *formModal) inferServiceCommand(servicePath string) string {
	if f.project.Path == "" {
		return ""
	}

	serviceDir := servicePath
	if !filepath.IsAbs(serviceDir) {
		switch serviceDir {
		case "", ".":
			serviceDir = f.project.Path
		default:
			serviceDir = filepath.Join(f.project.Path, servicePath)
		}
	}

	if _, err := os.Stat(filepath.Join(serviceDir, "build.gradle")); err == nil {
		return f.gradleExecutable(serviceDir) + " run"
	}
	if _, err := os.Stat(filepath.Join(serviceDir, "build.gradle.kts")); err == nil {
		return f.gradleExecutable(serviceDir) + " run"
	}

	manifestPath := filepath.Join(serviceDir, "package.json")
	if data, err := os.ReadFile(manifestPath); err == nil {
		var manifest packageManifest
		if err := json.Unmarshal(data, &manifest); err == nil {
			rt := detectRuntime(serviceDir, manifest)
			if rt == "bun" {
				return "bun run start"
			}
			return "npm run start"
		}
	}

	if rt := detectRuntime(serviceDir, packageManifest{}); rt == "bun" {
		return "bun run start"
	}
	return "npm run start"
}

func (f *formModal) gradleExecutable(serviceDir string) string {
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
		if dir == f.project.Path {
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

func (f *formModal) wrapText(text string, width int) string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return text
	}

	var lines []string
	var currentLine strings.Builder
	currentLen := 0

	for _, word := range words {
		wordLen := utf8.RuneCountInString(word)
		if currentLen == 0 {
			currentLine.WriteString(word)
			currentLen = wordLen
		} else if currentLen+1+wordLen <= width {
			currentLine.WriteString(" " + word)
			currentLen += 1 + wordLen
		} else {
			lines = append(lines, currentLine.String())
			currentLine.Reset()
			currentLine.WriteString(word)
			currentLen = wordLen
		}
	}

	if currentLine.Len() > 0 {
		lines = append(lines, currentLine.String())
	}

	return strings.Join(lines, "\n")
}

func (f *formModal) wrapSuggestion(marker, option string, maxWidth int) []string {
	iconWidth := utf8.RuneCountInString(marker)
	contentWidth := maxWidth - iconWidth

	if utf8.RuneCountInString(option) <= contentWidth {
		return []string{marker + option}
	}

	firstLine := marker + option[:contentWidth-1]
	remaining := option[contentWidth-1:]

	var lines []string
	lines = append(lines, firstLine)

	indent := strings.Repeat(" ", iconWidth)
	for utf8.RuneCountInString(remaining) > contentWidth {
		lines = append(lines, indent+remaining[:contentWidth])
		remaining = remaining[contentWidth:]
	}
	if len(remaining) > 0 {
		lines = append(lines, indent+remaining)
	}

	return lines
}

// ---- rendering ----------------------------------------------------------------

var (
	modalBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.DoubleBorder()).
				BorderForeground(colorBorderFocused).
				Foreground(lipgloss.Color("#c9d1d9")).
				Padding(1, 2)

	fieldLabelStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#c9d1d9"))

	fieldActiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#ffffff")).
				Background(lipgloss.Color("#238636")).
				Width(55)

	fieldInactiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#c9d1d9")).
				Width(55)

	fieldMutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6e7681")).
			Width(55)

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
			isServiceSuggestions := f.isServiceMode() && i == 3
			visibleCount := 4
			startIndex := 0
			if isServiceSuggestions && len(fld.options) > visibleCount {
				selected := fld.optionIndex
				if selected <= 1 {
					startIndex = 0
				} else if selected >= len(fld.options)-2 {
					startIndex = len(fld.options) - visibleCount
				} else {
					startIndex = selected - 1
				}
			}

			suggestionLineCount := 0
			maxSuggestionLines := visibleCount
			for optionIndex := startIndex; optionIndex < startIndex+visibleCount && optionIndex < len(fld.options); optionIndex++ {
				option := fld.options[optionIndex]
				marker := "○ "
				if optionIndex == fld.optionIndex {
					marker = "◉ "
				}
				style := fieldInactiveStyle
				if isServiceSuggestions && i == f.activeField && optionIndex == fld.optionIndex {
					style = fieldActiveStyle
				}
				suggestionLines := f.wrapSuggestion(marker, option, 55)
				for _, line := range suggestionLines {
					if suggestionLineCount >= maxSuggestionLines {
						break
					}
					lines = append(lines, style.Render(line))
					suggestionLineCount++
				}
				if suggestionLineCount >= maxSuggestionLines {
					break
				}
			}
			lines = append(lines, "")
			continue
		}

		cursor := " "
		if i == f.activeField {
			cursor = "▌"
		}
		display := f.wrapText(fld.value+cursor, 55)

		style := fieldInactiveStyle
		if i == f.activeField {
			style = fieldActiveStyle
		}
		lines = append(lines, style.Render(display), "")
	}

	if f.errMsg != "" {
		lines = append(lines, modalErrStyle.Render(f.errMsg), "")
	}

	hint := "tab/enter next field  •  ↑↓ navigate  •  enter submit  •  esc cancel"
	lines = append(lines, modalHintStyle.Render(hint))

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

func setProjectAsDefaultCmd(projectIndex int) tea.Cmd {
	return func() tea.Msg {
		cfg, err := setProjectAsDefault(projectIndex)
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

func updateServiceCmd(projectIndex, serviceIndex int, name, path, command string, ignored bool, previousProject Project, previousService Service, previousRuntime serviceRuntime, previousOwnedPID int32) tea.Cmd {
	return func() tea.Msg {
		cfg, err := updateService(projectIndex, serviceIndex, name, path, command, ignored)
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

// moveServiceUpCmd returns a tea.Cmd that moves a service up within its group.
func moveServiceUpCmd(projectIndex, serviceIndex int) tea.Cmd {
	return func() tea.Msg {
		cfg, err := moveServiceUp(projectIndex, serviceIndex)
		if err != nil {
			return configErrMsg{err}
		}
		return configSavedMsg{cfg}
	}
}

// moveServiceDownCmd returns a tea.Cmd that moves a service down within its group.
func moveServiceDownCmd(projectIndex, serviceIndex int) tea.Cmd {
	return func() tea.Msg {
		cfg, err := moveServiceDown(projectIndex, serviceIndex)
		if err != nil {
			return configErrMsg{err}
		}
		return configSavedMsg{cfg}
	}
}

// swapServicesCmd returns a tea.Cmd that swaps two services in a project.
func swapServicesCmd(projectIndex, idxA, idxB int) tea.Cmd {
	return func() tea.Msg {
		cfg, err := swapServices(projectIndex, idxA, idxB)
		if err != nil {
			return configErrMsg{err}
		}
		return configSavedMsg{cfg}
	}
}

// serviceReorderedMsg is sent after a successful service reorder, carrying the
// new services pane selection index so the cursor follows the moved service.
type serviceReorderedMsg struct {
	cfg           Config
	projectIndex  int
	newServiceIdx int // new index into project.Services for the moved service
}

// reorderServiceUpCmd returns a tea.Cmd that moves a service up and returns the new cursor position.
func reorderServiceUpCmd(projectIndex, serviceIdx int) tea.Cmd {
	return func() tea.Msg {
		cfg, err := swapServices(projectIndex, serviceIdx-1, serviceIdx)
		if err != nil {
			return configErrMsg{err}
		}
		return serviceReorderedMsg{cfg: cfg, projectIndex: projectIndex, newServiceIdx: serviceIdx - 1}
	}
}

// reorderServiceDownCmd returns a tea.Cmd that moves a service down and returns the new cursor position.
func reorderServiceDownCmd(projectIndex, serviceIdx int) tea.Cmd {
	return func() tea.Msg {
		cfg, err := swapServices(projectIndex, serviceIdx, serviceIdx+1)
		if err != nil {
			return configErrMsg{err}
		}
		return serviceReorderedMsg{cfg: cfg, projectIndex: projectIndex, newServiceIdx: serviceIdx + 1}
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
