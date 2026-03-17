package main

import (
	"fmt"
	"os"
	"strings"

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

	cfg    Config    // in-memory config; source of truth for UI
	modal  formModal
	errMsg string // transient error displayed in status bar
}

// projectItems converts a Config's projects to display strings.
func projectItems(cfg Config) []string {
	out := make([]string, 0, len(cfg.Projects))
	for _, p := range cfg.Projects {
		out = append(out, p.Name)
	}
	return out
}

// serviceItems returns the service names for the project at idx.
func serviceItems(cfg Config, projectIdx int) []string {
	if projectIdx < 0 || projectIdx >= len(cfg.Projects) {
		return []string{}
	}
	svcs := cfg.Projects[projectIdx].Services
	out := make([]string, 0, len(svcs))
	for _, s := range svcs {
		out = append(out, s.Name)
	}
	return out
}

func newModel() model {
	cfg, _ := loadConfig() // ignore load error on startup; app still works with empty state

	return model{
		cfg:   cfg,
		focus: paneProjects,
		projects: listPane{
			title:       "Projects",
			visible:     true,
			placeholder: "",
			items:       projectItems(cfg),
		},
		services: listPane{
			title:       "Services",
			visible:     true,
			placeholder: "",
			items:       serviceItems(cfg, 0),
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

	case configSavedMsg:
		// Update in-memory config and refresh pane lists.
		m.cfg = msg.cfg
		m.projects.items = projectItems(m.cfg)
		m.services.items = serviceItems(m.cfg, m.projects.selected)
		m.errMsg = ""
		return m, nil

	case configErrMsg:
		m.errMsg = "Error saving config: " + msg.err.Error()
		return m, nil

	case tea.KeyMsg:
		k := msg.String()

		// ctrl+c always quits, even when a modal is open.
		if k == "ctrl+c" {
			return m, tea.Quit
		}

		// Modal intercepts all remaining keys while open.
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
				return m, tea.Quit
			default:
				return m, nil
			}
		}

		switch k {
		case "q":
			return m, tea.Quit
		case "?":
			m.showHelp = true
			return m, nil
		case "c":
			if m.focus == paneProjects {
				m.modal.openCreateProject()
			} else if m.focus == paneServices {
				// A service must belong to a project.
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
		case "h", "left":
			m.focusPrev()
			return m, nil
		case "l", "right":
			m.focusNext()
			return m, nil
		case "j", "down":
			m.moveSelection(1)
			if m.focus == paneProjects {
				// Use in-memory config — no blocking disk read.
				m.services.items = serviceItems(m.cfg, m.projects.selected)
				m.services.selected = 0
			}
			return m, nil
		case "k", "up":
			m.moveSelection(-1)
			if m.focus == paneProjects {
				m.services.items = serviceItems(m.cfg, m.projects.selected)
				m.services.selected = 0
			}
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
				"Hotkeys: h/l focus, j/k move, p/d/a toggle panes, c create, ? help, q quit.")
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
		statusBar = "\n" + errStyle.Render("⚠ " + m.errMsg)
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
			left := m.renderListPane(m.projects, col1, contentH, m.focus == paneProjects, projectHighlight)
			mid := m.renderListPane(m.services, col2, contentH, m.focus == paneServices, serviceHighlight)
			out = lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", gap), mid)
		} else {
			out = m.renderListPane(m.services, m.width, contentH, m.focus == paneServices, serviceHighlight)
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

		left := m.renderListPane(m.projects, col1, contentH, m.focus == paneProjects, projectHighlight)
		mid := m.renderListPane(m.services, col2, contentH, m.focus == paneServices, serviceHighlight)
		right := m.renderRightColumn(col3, contentH)

		out = lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", gap), mid, strings.Repeat(" ", gap), right)
		return clampToViewport(m.width, m.height, out+statusBar)
	}

	col2 := m.width / 3
	col3 := m.width - col2 - gap
	mid := m.renderListPane(m.services, col2, contentH, m.focus == paneServices, serviceHighlight)
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
			return m.renderListPane(m.details, width, height, m.focus == paneDetails, false)
		}
		topH := avail / 2
		botH := avail - topH
		top := m.renderListPane(m.details, width, topH, m.focus == paneDetails, false)
		bot := m.renderListPane(m.logs, width, botH, m.focus == paneLogs, false)
		return lipgloss.JoinVertical(lipgloss.Left, top, bot)
	case 1:
		if m.details.visible {
			return m.renderListPane(m.details, width, height, m.focus == paneDetails, false)
		}
		return m.renderListPane(m.logs, width, height, m.focus == paneLogs, false)
	default:
		empty := listPane{title: "Right Column", visible: true, placeholder: "PLACEHOLDER: Enable Details (d) or Logs (a).", items: []string{"(nothing to show)"}}
		return m.renderListPane(empty, width, height, false, false)
	}
}

func (m model) renderListPane(p listPane, width, height int, focused bool, highlightSel bool) string {
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
		lines = append(lines, renderRow(faintStyle, innerW, p.placeholder))
		lines = append(lines, renderRow(lipgloss.NewStyle(), innerW, ""))
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
				lines = append(lines, renderRow(selectedFocusedStyle, innerW, line))
			case highlightSel:
				lines = append(lines, renderRow(selectedContextStyle, innerW, line))
			default:
				lines = append(lines, renderRow(selectedStyle, innerW, line))
			}
			continue
		}
		lines = append(lines, renderRow(plainLine, innerW, line))
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
			lines = append(lines, renderRow(plainLine, innerW, ""))
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
			},
		},
		{
			name: "Create",
			rows: [][2]string{
				{"c", "Create project or service (context-sensitive)"},
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
