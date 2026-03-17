package main

import (
	"strings"
	"testing"

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
					l := m.renderListPane(m.projects, col1, tc.h, true, false)
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
						m2 := m.renderListPane(m.services, col2, tc.h, true, false)
						mh = lipgloss.Height(m2)
						if parts := strings.SplitN(m2, "\n", 2); len(parts) == 2 {
							mboxH = lipgloss.Height(parts[1])
						}
						rh = lipgloss.Height(m.renderRightColumn(col3, tc.h))
					} else {
						col2 := tc.w / 3
						col3 := tc.w - col2 - gap
						m2 := m.renderListPane(m.services, col2, tc.h, true, false)
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
					lh = lipgloss.Height(m.renderListPane(m.projects, col1, tc.h, true, false))
					mh = lipgloss.Height(m.renderListPane(m.services, col2, tc.h, true, false))
				} else {
					mh = lipgloss.Height(m.renderListPane(m.services, tc.w, tc.h, true, false))
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
