package main

import (
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// modalMode distinguishes which creation form is active.
type modalMode int

const (
	modalNone          modalMode = iota
	modalCreateProject           // "c" pressed in projects pane
	modalCreateService           // "c" pressed in services pane
)

// formField holds one labelled text-input slot.
type formField struct {
	label    string
	value    string
	required bool
}

// formModal tracks all state for the create-project / create-service overlay.
type formModal struct {
	mode        modalMode
	fields      []formField
	activeField int
	errMsg      string
	projectName string // set when mode == modalCreateService
}

// isOpen reports whether any modal is currently visible.
func (f *formModal) isOpen() bool {
	return f.mode != modalNone
}

// openCreateProject resets the modal for project creation.
func (f *formModal) openCreateProject() {
	*f = formModal{
		mode: modalCreateProject,
		fields: []formField{
			{label: "Project name", required: true},
			{label: "Project path", required: true},
		},
	}
}

// openCreateService resets the modal for service creation within a named project.
func (f *formModal) openCreateService(projectName string) {
	*f = formModal{
		mode:        modalCreateService,
		projectName: projectName,
		fields: []formField{
			{label: "Service name", required: true},
			{label: "Path (optional)"},
		},
	}
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
		f.activeField = (f.activeField + 1) % len(f.fields)
	case "shift+tab", "up":
		f.errMsg = ""
		f.activeField = (f.activeField - 1 + len(f.fields)) % len(f.fields)
	case "enter":
		if f.activeField < len(f.fields)-1 {
			// Advance to next field.
			f.activeField++
			return false, false
		}
		// Last field — validate and submit.
		for _, fld := range f.fields {
			if fld.required && strings.TrimSpace(fld.value) == "" {
				f.errMsg = "\"" + fld.label + "\" is required."
				return false, false
			}
		}
		return true, false
	case "backspace":
		cur := &f.fields[f.activeField]
		if len(cur.value) > 0 {
			// Safe rune-aware trim of last character.
			runes := []rune(cur.value)
			cur.value = string(runes[:len(runes)-1])
		}
	default:
		// Accept a single Unicode rune (handles multi-byte UTF-8 characters).
		if utf8.RuneCountInString(k) == 1 {
			f.fields[f.activeField].value += k
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

// path returns the trimmed value of the second field (always the optional path).
func (f *formModal) path() string {
	if len(f.fields) < 2 {
		return ""
	}
	return strings.TrimSpace(f.fields[1].value)
}

// configErrMsg is sent to the model when a config I/O error occurs.
type configErrMsg struct{ err error }

// configSavedMsg is sent after a successful save, carrying the updated config.
type configSavedMsg struct{ cfg Config }

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
	if f.mode == modalCreateService {
		title = "Create Service"
	}

	lines := []string{
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7ee787")).Render(title),
		"",
	}

	if f.mode == modalCreateService && f.projectName != "" {
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

		cursor := " "
		if i == f.activeField {
			cursor = "▌"
		}
		display := fld.value + cursor
		// Truncate display to fit field width.
		display = ansi.Truncate(display, 35, "…")

		var input string
		if i == f.activeField {
			input = fieldActiveStyle.Render(display)
		} else {
			input = fieldInactiveStyle.Render(display)
		}
		lines = append(lines, input, "")
	}

	if f.errMsg != "" {
		lines = append(lines, modalErrStyle.Render(f.errMsg), "")
	}

	lines = append(lines,
		modalHintStyle.Render("tab/↑↓ switch field  •  enter submit  •  esc cancel"),
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

// saveServiceCmd returns a tea.Cmd that persists a new service asynchronously.
func saveServiceCmd(projectIndex int, name, path string) tea.Cmd {
	return func() tea.Msg {
		cfg, err := addService(projectIndex, name, path)
		if err != nil {
			return configErrMsg{err}
		}
		return configSavedMsg{cfg}
	}
}
