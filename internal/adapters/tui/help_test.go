package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestWrapHelp(t *testing.T) {
	items := strings.Split(targetsHelp, helpSep)

	for _, width := range []int{20, 40, 60, 80, 120, 400} {
		lines := wrapHelp(targetsHelp, width)

		// Nothing lost, nothing split, order preserved.
		var got []string
		for _, l := range lines {
			got = append(got, strings.Split(l, helpSep)...)
		}
		if strings.Join(got, "|") != strings.Join(items, "|") {
			t.Errorf("width %d: shortcuts changed:\n%q", width, lines)
		}
		for _, l := range lines {
			single := !strings.Contains(l, helpSep)
			if lipgloss.Width(l) > width && !single {
				t.Errorf("width %d: line too wide (%d): %q", width, lipgloss.Width(l), l)
			}
		}
	}

	if got := wrapHelp(targetsHelp, 400); len(got) != 1 {
		t.Errorf("wide terminal should keep one line, got %d", len(got))
	}
	// Greedy fill: a line breaks only when the next shortcut would not fit.
	lines := wrapHelp(targetsHelp, 40)
	if len(lines) < 3 {
		t.Fatalf("expected several lines at width 40, got %q", lines)
	}
	for i := 0; i+1 < len(lines); i++ {
		next := strings.Split(lines[i+1], helpSep)[0]
		if lipgloss.Width(lines[i])+lipgloss.Width(helpSep)+lipgloss.Width(next) <= 40 {
			t.Errorf("line %d broke early: %q | %q", i, lines[i], next)
		}
	}
	// A shortcut wider than the terminal still gets shown, on its own line.
	if got := wrapHelp("a · a-very-long-shortcut-name · b", 10); len(got) != 3 || got[1] != "a-very-long-shortcut-name" {
		t.Errorf("overlong shortcut: %q", got)
	}
}

func TestFooterWrapsToTerminalWidth(t *testing.T) {
	m := testModel(t)
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 30})

	for name, view := range map[string]string{"targets": m.View()} {
		for _, l := range strings.Split(view, "\n") {
			if strings.Contains(l, "·") && lipgloss.Width(l) > 40 && strings.Contains(l, "enable") {
				t.Errorf("%s: footer line wider than the terminal: %q", name, l)
			}
		}
		for _, key := range []string{"↵ open", "t enable/disable", "u apply all targets", "q quit"} {
			if !strings.Contains(strings.ReplaceAll(view, "\n", " "), key) {
				t.Errorf("%s: shortcut %q missing after wrapping", name, key)
			}
		}
	}

	// The migrations screen keeps its whole footer on screen even when wrapped.
	m.Update(key("enter"))
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 16})
	view := m.View()
	if n := strings.Count(view, "\n") + 1; n > 16 {
		t.Errorf("migrations view is %d lines in a 16-line terminal:\n%s", n, view)
	}
	if !strings.Contains(strings.ReplaceAll(view, "\n", " "), "q back") {
		t.Errorf("last shortcut cut off:\n%s", view)
	}

	// Long notices wrap too instead of running off the edge.
	m.notice = strings.Repeat("word ", 30)
	for _, l := range strings.Split(m.View(), "\n") {
		if lipgloss.Width(l) > 40 {
			t.Errorf("line wider than the terminal (%d): %q", lipgloss.Width(l), l)
		}
	}
}
