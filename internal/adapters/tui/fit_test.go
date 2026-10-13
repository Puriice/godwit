package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/puriice/godwit/internal/domain"
)

func TestClipHeightKeepsTop(t *testing.T) {
	if got := clipHeight("a\nb\nc\nd\n", 2); got != "a\nb" {
		t.Errorf("clipHeight = %q", got)
	}
	if got := clipHeight("a\nb", 5); got != "a\nb" {
		t.Errorf("short input changed: %q", got)
	}
}

func TestLongListsStartAtTop(t *testing.T) {
	var targets []domain.Target
	for i := range 30 {
		t.Setenv(fmt.Sprintf("GODWIT_T%02d_PASSWORD", i), "x")
		targets = append(targets, domain.Target{Name: fmt.Sprintf("t%02d", i), Driver: "postgres", Host: "h", Database: "d", User: "u"})
	}
	m := New(testService(t, targets...))
	m.width, m.height = 80, 12
	m.cursor = len(targets) - 1

	for name, screen := range map[string]screen{"targets": scrTargets, "plugins": scrPlugins, "files": scrFiles} {
		m.screen = screen
		v := m.View()
		if h := lipgloss.Height(v); h > m.height {
			t.Errorf("%s: view is %d lines in a %d-line terminal", name, h, m.height)
		}
		if first, _, _ := strings.Cut(v, "\n"); !strings.Contains(first, "godwit") {
			t.Errorf("%s: first line = %q, want the header", name, first)
		}
	}

	m.screen = scrTargets
	if v := m.View(); !strings.Contains(v, "t29") {
		t.Errorf("selected target scrolled out of view:\n%s", v)
	}
}
