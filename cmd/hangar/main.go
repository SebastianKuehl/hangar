package main

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type paneID int

const (
	paneProjects paneID = iota
	paneServices
	paneDetails
	paneLogs
)

type listPane struct {
	title       string
	visible     bool
	placeholder string
	items       []string
	selected    int
}

type model struct {
	width  int
	height int

	focus    paneID
	showHelp bool

	projects listPane
	services listPane
	details  listPane
	logs     listPane
}

func newModel() model {
	return model{
		focus: paneProjects,
		projects: listPane{
			title:       "Projects",
			visible:     true,
			placeholder: "PLACEHOLDER: Replace with real project data.",
			items: []string{
				"Project Alpha",
				"Project Beta",
				"Project Gamma",
				"Project Delta",
			},
		},
		services: listPane{
			title:       "Services",
			visible:     true,
			placeholder: "PLACEHOLDER: Replace with real service data.",
			items: []string{
				"api",
				"web",
				"worker",
				"cron",
			},
		},
		details: listPane{
			title:       "Details",
			visible:     true,
			placeholder: "PLACEHOLDER: Replace with real details view.",
			items: []string{
				"Owner: you",
				"Repo: github.com/SebastianKuehl/hangar",
				"Env: dev",
				"Status: healthy",
			},
		},
		logs: listPane{
			title:       "Logs",
			visible:     true,
			placeholder: "PLACEHOLDER: Replace with real log stream.",
			items: []string{
				"[info] starting...",
				"[info] loading config",
				"[warn] using placeholder data",
				"[info] ready",
				"[debug] focus navigation enabled",
			},
		},
	}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		k := msg.String()

		if m.showHelp {
			switch k {
			case "?", "esc":
				m.showHelp = false
				return m, nil
			case "ctrl+c", "q":
				return m, tea.Quit
			default:
				return m, nil
			}
		}

		switch k {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "?":
			m.showHelp = true
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
		case "h", "left":
			m.focusPrev()
			return m, nil
		case "l", "right":
			m.focusNext()
			return m, nil
		case "j", "down":
			m.moveSelection(1)
			return m, nil
		case "k", "up":
			m.moveSelection(-1)
			return m, nil
		}
	}

	return m, nil
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
	idx--
	if idx < 0 {
		idx = len(order) - 1
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
	idx++
	if idx >= len(order) {
		idx = 0
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

var (
	basePaneStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(0, 1)

	focusedPaneStyle = basePaneStyle.Copy().
				BorderForeground(lipgloss.Color("62")).
				Bold(true)

	faintStyle = lipgloss.NewStyle().Faint(true)
)

func (m model) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}

	if m.showHelp {
		return m.renderHelp()
	}

	if m.width < 60 || m.height < 10 {
		return lipgloss.NewStyle().Padding(1, 2).Render(
			"Terminal too small. Resize to at least 60x10.\n\n" +
				"Hotkeys: h/l focus, j/k move, p/d/a toggle panes, ? help, q quit.")
	}

	gap := 1
	if m.projects.visible {
		col1 := m.width / 4
		col2 := m.width / 4
		col3 := m.width - col1 - col2 - 2*gap
		if col3 < 20 {
			col3 = 20
		}

		left := m.renderListPane(m.projects, col1, m.height, m.focus == paneProjects)
		mid := m.renderListPane(m.services, col2, m.height, m.focus == paneServices)
		right := m.renderRightColumn(col3, m.height)

		return lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", gap), mid, strings.Repeat(" ", gap), right)
	}

	col2 := m.width / 3
	col3 := m.width - col2 - gap
	mid := m.renderListPane(m.services, col2, m.height, m.focus == paneServices)
	right := m.renderRightColumn(col3, m.height)
	return lipgloss.JoinHorizontal(lipgloss.Top, mid, strings.Repeat(" ", gap), right)
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
			return m.renderListPane(m.details, width, height, m.focus == paneDetails)
		}
		topH := avail / 2
		botH := avail - topH
		top := m.renderListPane(m.details, width, topH, m.focus == paneDetails)
		bot := m.renderListPane(m.logs, width, botH, m.focus == paneLogs)
		return lipgloss.JoinVertical(lipgloss.Left, top, bot)
	case 1:
		if m.details.visible {
			return m.renderListPane(m.details, width, height, m.focus == paneDetails)
		}
		return m.renderListPane(m.logs, width, height, m.focus == paneLogs)
	default:
		empty := listPane{title: "Right Column", visible: true, placeholder: "PLACEHOLDER: Enable Details (d) or Logs (a).", items: []string{"(nothing to show)"}}
		return m.renderListPane(empty, width, height, false)
	}
}

func (m model) renderListPane(p listPane, width, height int, focused bool) string {
	style := basePaneStyle
	if focused {
		style = focusedPaneStyle
	}
	innerW := max(0, width-4)  // account for left/right border (2) + horizontal padding (2)
	innerH := max(0, height-2) // account for top/bottom border
	style = style.Width(innerW).Height(innerH)

	lines := make([]string, 0, 2+len(p.items))
	lines = append(lines, faintStyle.Render(p.placeholder))
	lines = append(lines, "")

	for i, it := range p.items {
		prefix := "  "
		if i == p.selected {
			if focused {
				prefix = "> "
			} else {
				prefix = "• "
			}
		}
		lines = append(lines, prefix+it)
	}

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	// Note: Pane content is intentionally placeholder data for now.
	title := lipgloss.NewStyle().Bold(true).Render(p.title)
	return style.Render(title + "\n" + content)
}

func (m model) renderHelp() string {
	help := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		Padding(1, 2).
		Width(min(70, m.width-4)).
		Render(
			"Hotkeys\n\n" +
				"p  Toggle Projects pane\n" +
				"d  Toggle Details pane\n" +
				"a  Toggle Logs pane\n\n" +
				"h/l (or ←/→)  Move focus between panes\n" +
				"j/k (or ↓/↑)  Move selection within focused pane\n\n" +
				"?  Show/close this help\n" +
				"esc  Close help\n" +
				"q  Quit\n\n" +
				"Notes\n" +
				"- Pane contents are intentionally placeholder data for now.\n" +
				"- When a focused pane is toggled off, focus jumps to the nearest visible pane.")

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, help)
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

func main() {
	p := tea.NewProgram(newModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
