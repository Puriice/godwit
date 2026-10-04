package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/puriice/godwit/internal/adapters/filestore"
	"github.com/puriice/godwit/internal/adapters/fsmigrations"
	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

// noDBs is a DatabaseFactory for tests that never reach a database.
type noDBs struct{}

func (noDBs) Drivers() []string { return []string{"mysql", "postgres"} }
func (noDBs) Open(context.Context, domain.Target, string) (app.Database, error) {
	return nil, errors.New("no database in tests")
}

// testService builds a Service on a temp project dir with the given targets.
func testService(t *testing.T, targets ...domain.Target) *app.Service {
	t.Helper()
	root := t.TempDir()
	svc, err := app.New(filestore.New(root), fsmigrations.New(root), noDBs{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range targets {
		if err := svc.AddTarget(tg, "", false); err != nil {
			t.Fatal(err)
		}
	}
	return svc
}

func testModel(t *testing.T) *Model {
	t.Helper()
	t.Setenv("GODWIT_A_PASSWORD", "x")
	t.Setenv("GODWIT_B_PASSWORD", "x")
	return New(testService(t,
		domain.Target{Name: "a", Driver: "postgres", Host: "h", Database: "d", User: "u"},
		domain.Target{Name: "b", Driver: "mysql", Host: "h", Database: "d", User: "u"},
	))
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestNavigation(t *testing.T) {
	m := testModel(t)
	if len(m.credQueue) != 0 {
		t.Fatal("passwords from env should satisfy credentials")
	}
	m.Update(key("down"))
	if m.cursor != 1 {
		t.Errorf("cursor = %d", m.cursor)
	}
	m.Update(key("enter"))
	if m.screen != scrMigrations {
		t.Errorf("screen = %v, want migrations", m.screen)
	}
	m.Update(key("esc"))
	if m.screen != scrTargets {
		t.Errorf("screen = %v, want targets", m.screen)
	}
}

func TestMissingPasswordQueuesCredentialForm(t *testing.T) {
	m := New(testService(t, domain.Target{Name: "nopw", Driver: "mysql"}))
	if len(m.credQueue) != 1 {
		t.Fatalf("credQueue = %d", len(m.credQueue))
	}
	if cmd := m.Init(); cmd == nil || m.screen != scrForm {
		t.Errorf("expected credential form to open, screen = %v", m.screen)
	}
	// Esc skips the prompt and returns to the targets list.
	m.Update(key("esc"))
	if m.screen != scrTargets {
		t.Errorf("screen after esc = %v", m.screen)
	}
}

func TestRunMessagesUpdateState(t *testing.T) {
	m := testModel(t)
	st := m.state("a")
	st.running = true
	st.events = make(chan tea.Msg)

	m.Update(statusMsg{target: "a", items: []domain.Item{
		{Version: 1, Name: "x", State: domain.Applied},
		{Version: 2, Name: "y", State: domain.Pending},
	}})
	if !st.loaded || !strings.Contains(summarize(st.items), "1 pending") {
		t.Errorf("summary = %q", summarize(st.items))
	}

	m.Update(runDoneMsg{target: "a", dir: domain.Up, err: errors.New("boom")})
	if st.running || st.runErr == nil {
		t.Errorf("running=%v runErr=%v", st.running, st.runErr)
	}
	// The status refresh after a run must not erase the run error.
	m.Update(statusMsg{target: "a"})
	if st.runErr == nil {
		t.Error("run error cleared by status refresh")
	}
	if !strings.Contains(m.View(), "boom") {
		t.Error("error not shown in targets view")
	}
}
