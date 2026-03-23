package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestViewFitsViewport(t *testing.T) {
	cases := []struct {
		w, h int
	}{
		{w: 120, h: 40},
		{w: 100, h: 24},
		{w: 80, h: 15},
	}

	vis := []struct {
		projects bool
		details  bool
		logs     bool
	}{
		{true, true, true},
		{true, true, false},
		{true, false, true},
		{true, false, false},
		{false, true, true},
		{false, true, false},
		{false, false, true},
		{false, false, false},
	}

	for _, tc := range cases {
		for _, v := range vis {
			m := newModel()
			m.width = tc.w
			m.height = tc.h
			m.projects.visible = v.projects
			m.details.visible = v.details
			m.logs.visible = v.logs
			m.ensureFocusVisible()

			out := m.View()
			if gotW := lipgloss.Width(out); gotW > tc.w {
				t.Fatalf("view width overflow: got %d > %d (projects=%v details=%v logs=%v)", gotW, tc.w, v.projects, v.details, v.logs)
			}
			if gotH := lipgloss.Height(out); gotH > tc.h {
				gap := 1
				rightVisible := v.details || v.logs

				lh, mh, rh := 0, 0, 0
				lboxH, mboxH := 0, 0
				if v.projects {
					col1 := tc.w / 4
					l := m.renderListPane(m.projects, col1, tc.h, true, false, false)
					lh = lipgloss.Height(l)
					if parts := strings.SplitN(l, "\n", 2); len(parts) == 2 {
						lboxH = lipgloss.Height(parts[1])
					}
				}

				if rightVisible {
					if v.projects {
						col1 := tc.w / 4
						col2 := tc.w / 4
						col3 := tc.w - col1 - col2 - 2*gap
						m2 := m.renderListPane(m.services, col2, tc.h, true, false, false)
						mh = lipgloss.Height(m2)
						if parts := strings.SplitN(m2, "\n", 2); len(parts) == 2 {
							mboxH = lipgloss.Height(parts[1])
						}
						rh = lipgloss.Height(m.renderRightColumn(col3, tc.h))
					} else {
						col2 := tc.w / 3
						col3 := tc.w - col2 - gap
						m2 := m.renderListPane(m.services, col2, tc.h, true, false, false)
						mh = lipgloss.Height(m2)
						if parts := strings.SplitN(m2, "\n", 2); len(parts) == 2 {
							mboxH = lipgloss.Height(parts[1])
						}
						rh = lipgloss.Height(m.renderRightColumn(col3, tc.h))
					}
					t.Fatalf("view height overflow: got %d > %d (projects=%v details=%v logs=%v leftH=%d leftBoxH=%d midH=%d midBoxH=%d rightH=%d)", gotH, tc.h, v.projects, v.details, v.logs, lh, lboxH, mh, mboxH, rh)
				}

				if v.projects {
					col1 := tc.w / 4
					col2 := tc.w - col1 - gap
					lh = lipgloss.Height(m.renderListPane(m.projects, col1, tc.h, true, false, false))
					mh = lipgloss.Height(m.renderListPane(m.services, col2, tc.h, true, false, false))
				} else {
					mh = lipgloss.Height(m.renderListPane(m.services, tc.w, tc.h, true, false, false))
				}
				t.Fatalf("view height overflow: got %d > %d (projects=%v details=%v logs=%v leftH=%d midH=%d)", gotH, tc.h, v.projects, v.details, v.logs, lh, mh)
			}

			// For normal-sized terminals, the UI should fully occupy the viewport height.
			if tc.w >= 60 && tc.h >= 10 {
				if gotH := lipgloss.Height(out); gotH != tc.h {
					t.Fatalf("view height mismatch: got %d != %d (projects=%v details=%v logs=%v)", gotH, tc.h, v.projects, v.details, v.logs)
				}
			}
		}
	}
}

func TestWrapToggleAffectsRightPanesOnly(t *testing.T) {
	m := newModel()
	m.details.items = []string{"this is a very long details line that should wrap when enabled"}
	m.logs.items = []string{"this is a very long logs line that should wrap when enabled"}
	m.wrapText = false

	unwrapped := m.renderListPane(m.details, 20, 8, false, false, false)
	if !strings.Contains(unwrapped, "…") {
		t.Fatalf("expected truncated right-pane text when wrap is disabled, got %q", unwrapped)
	}

	wrapped := m.renderListPane(m.details, 20, 8, false, false, true)
	if strings.Contains(wrapped, "…") {
		t.Fatalf("expected wrapped right-pane text without truncation marker, got %q", wrapped)
	}

	updatedModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	got := updatedModel.(model)
	if !got.wrapText {
		t.Fatalf("expected wrap toggle to enable on 't'")
	}
}

func TestHelpIncludesEditAndServiceToggleHotkeys(t *testing.T) {
	m := newModel()
	m.width = 100
	help := m.renderHelpBox()
	if !strings.Contains(help, "Edit selected project or service") {
		t.Fatalf("expected in-app help to describe the e hotkey, got %q", help)
	}
	if !strings.Contains(help, "Start / stop the selected service") {
		t.Fatalf("expected in-app help to describe the s hotkey, got %q", help)
	}
	if !strings.Contains(help, "Interrupt a running service check") {
		t.Fatalf("expected in-app help to describe the i hotkey, got %q", help)
	}
	if !strings.Contains(help, "Retry an interrupted service check") {
		t.Fatalf("expected in-app help to describe the r hotkey, got %q", help)
	}
	if !strings.Contains(helpText, "e        Edit the selected project or service") {
		t.Fatalf("expected CLI help text to describe the e hotkey, got %q", helpText)
	}
	if !strings.Contains(helpText, "s        Start the selected service when stopped, or stop it when running") {
		t.Fatalf("expected CLI help text to describe the s hotkey, got %q", helpText)
	}
	if !strings.Contains(helpText, "i        Interrupt the current service check") {
		t.Fatalf("expected CLI help text to describe the i hotkey, got %q", helpText)
	}
	if !strings.Contains(helpText, "r        Retry an interrupted service check") {
		t.Fatalf("expected CLI help text to describe the r hotkey, got %q", helpText)
	}
}

func TestViewShowsRuntimeLoadingOverlay(t *testing.T) {
	m := model{
		width:  100,
		height: 24,
		cfg: Config{
			Projects: []Project{
				{
					Name: "Demo",
					Path: "/workspace/demo",
					Services: []Service{
						{Name: "api", Path: "apps/api", Command: "npm run start"},
					},
				},
			},
		},
		projects: listPane{title: "Projects", visible: true},
		services: listPane{title: "Services", visible: true},
		details:  listPane{title: "Details", visible: true},
		logs:     listPane{title: "Logs", visible: true},
	}
	m.syncSelectionState()
	m.runtimeLoading = true

	out := m.View()
	if !strings.Contains(out, "Checking running services") {
		t.Fatalf("expected loading overlay in view, got %q", out)
	}
	if !strings.Contains(out, "Projects") {
		t.Fatalf("expected projects pane to remain visible during runtime loading, got %q", out)
	}
}
