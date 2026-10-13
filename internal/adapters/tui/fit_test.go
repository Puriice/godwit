package tui

import (
	"fmt"
	"os"
	"path/filepath"
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

func TestFilesCursorStaysVisible(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 100, 24
	for i := range 25 {
		if _, err := m.svc.CreateMigration(fmt.Sprintf("m%02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	m.screen = scrFiles
	for m.fcur = 0; m.fcur < 25; m.fcur++ {
		v := m.View()
		if h := lipgloss.Height(v); h > m.height {
			t.Fatalf("cursor %d: view is %d lines in a %d-line terminal", m.fcur, h, m.height)
		}
		marked := 0
		for _, l := range strings.Split(v, "\n") {
			if strings.Contains(l, "› ") {
				marked++
			}
		}
		if marked != 1 {
			t.Fatalf("cursor %d: %d rows carry the cursor, want 1:\n%s", m.fcur, marked, v)
		}
	}
}

func TestFilesCursorStaysVisibleWithBrokenFiles(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 100, 24
	if err := m.svc.EnsureMigrationsDir(); err != nil {
		t.Fatal(err)
	}
	dir := m.svc.MigrationsLocation()
	for i := range 25 {
		body := "-- +godwit Up\nCREATE TABLE t (id INTEGER);\n-- +godwit Down\nDROP TABLE t;\n"
		if i%3 == 0 {
			body = "-- +godwit Bogus\nnonsense\n"
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d_m.sql", i+1)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := m.svc.MigrationFiles()
	if brokenCount(files) == 0 {
		t.Skip("no file failed to parse")
	}
	m.screen = scrFiles
	for m.fcur = 0; m.fcur < len(files); m.fcur++ {
		v := m.View()
		if strings.Count(v, "› ") != 1 {
			t.Fatalf("cursor %d: want exactly one cursor row:\n%s", m.fcur, v)
		}
	}
}
