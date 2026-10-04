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
	svc, err := app.New(filestore.New(root), fsmigrations.New(root, noDBs{}.Drivers()), noDBs{})
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

func TestToggleDisableAndEnable(t *testing.T) {
	m := testModel(t)
	st := m.state("a")
	st.loaded, st.items = true, []domain.Item{{Version: 1, Name: "x", State: domain.Applied}}

	// t on the targets list disables the selected target and persists it.
	m.Update(key("t"))
	if a, _ := m.svc.Target("a"); !a.Disabled {
		t.Fatal("a should be disabled")
	}
	if b, _ := m.svc.Target("b"); b.Disabled {
		t.Error("b should be unaffected")
	}
	if st.loaded || len(st.items) != 0 {
		t.Error("stale status kept for a disabled target")
	}
	view := m.View()
	if !strings.Contains(view, "disabled") {
		t.Errorf("targets view does not show the disabled state:\n%s", view)
	}

	// A disabled target is never refreshed or run.
	a, _ := m.svc.Target("a")
	if cmd := m.refresh(a); cmd != nil || st.loading {
		t.Error("refresh must not connect to a disabled target")
	}
	if cmd := m.run(a, domain.Up, 0); cmd != nil || st.running {
		t.Error("run must not start on a disabled target")
	}

	// Its migrations screen explains and ignores action keys.
	m.Update(key("enter"))
	if m.screen != scrMigrations || !strings.Contains(m.View(), "disabled") {
		t.Errorf("migrations view:\n%s", m.View())
	}
	m.Update(key("u"))
	if st.running || !strings.Contains(m.notice, "disabled") {
		t.Errorf("u on a disabled target: running=%v notice=%q", st.running, m.notice)
	}

	// t on the migrations screen enables it again; a refresh is started.
	_, cmd := m.Update(key("t"))
	if a, _ := m.svc.Target("a"); a.Disabled {
		t.Fatal("a should be enabled")
	}
	if cmd == nil || !st.loading {
		t.Error("enabling should start a status refresh")
	}
}

func TestApplyAllSkipsDisabled(t *testing.T) {
	m := testModel(t)
	m.svc.SetTargetEnabled("a", false)
	m.svc.SetTargetEnabled("b", false)
	m.Update(key("u"))
	if m.screen == scrForm || !strings.Contains(m.notice, "no enabled") {
		t.Errorf("screen=%v notice=%q", m.screen, m.notice)
	}

	m.svc.SetTargetEnabled("b", true)
	m.Update(key("u"))
	if m.screen != scrForm || !strings.Contains(m.formTitle, "Confirm") {
		t.Errorf("expected a confirm form, screen=%v", m.screen)
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

func TestRedoSelectedMigration(t *testing.T) {
	m := testModel(t)
	a, _ := m.svc.Target("a")
	st := m.state("a")
	rec := &domain.Record{}
	st.loaded = true
	st.items = []domain.Item{
		{Version: 1, Name: "users", State: domain.Applied, Record: rec},
		{Version: 2, Name: "posts", State: domain.Modified, Record: rec},
		{Version: 3, Name: "tags", State: domain.Pending},
		{Version: 4, Name: "broken", State: domain.Dirty, Record: rec},
		{Version: 5, Name: "ghost", State: domain.Missing, Record: rec},
	}
	m.screen = scrMigrations // target "a" is selected

	// Pending, dirty and missing migrations cannot be redone; the reason is shown.
	for cursor, want := range map[int]string{2: "not applied yet", 3: "dirty", 4: "no migration file"} {
		m.mcur, m.notice = cursor, ""
		m.Update(key("R"))
		if m.screen != scrMigrations || !strings.Contains(m.notice, want) {
			t.Errorf("cursor %d: screen=%v notice=%q, want notice containing %q", cursor, m.screen, m.notice, want)
		}
	}

	// An applied migration asks for confirmation first, and warns about later ones.
	m.mcur = 0
	m.Update(key("R"))
	if m.screen != scrForm || m.formTitle != "Confirm" {
		t.Fatalf("expected a confirm form, screen=%v title=%q", m.screen, m.formTitle)
	}
	if st.running {
		t.Error("nothing may run before the user confirms")
	}
	m.Update(key("esc")) // cancel
	if m.screen != scrMigrations || st.running {
		t.Errorf("cancel: screen=%v running=%v", m.screen, st.running)
	}
	// The warning counts the later applied migrations (2, 4 and 5; not pending 3).
	if got := m.confirmRedo(a, st); got == nil {
		t.Fatal("expected the confirm form command")
	}
	if !strings.Contains(m.View(), "3 later applied") {
		t.Errorf("confirm text missing the later-migrations warning:\n%s", m.View())
	}
	m.Update(key("esc"))

	// Redo is a background run reported as a single "redone" migration.
	cmd := m.redo(a, 1)
	if cmd == nil || !st.running {
		t.Fatal("redo should start a background run")
	}
	done, ok := cmd().(runDoneMsg) // fake DB factory fails; first message is the result
	if !ok || done.dir != domain.Redo || done.err == nil {
		t.Fatalf("done = %+v", done)
	}
	if doneWord(domain.Redo) != "redone" {
		t.Errorf("doneWord(Redo) = %q", doneWord(domain.Redo))
	}
	m.Update(done)
	if st.running || st.runErr == nil {
		t.Errorf("running=%v runErr=%v", st.running, st.runErr)
	}

	// A disabled target ignores R, like the other action keys.
	m.svc.SetTargetEnabled("a", false)
	m.mcur, m.notice = 0, ""
	m.Update(key("R"))
	if m.screen != scrMigrations || !strings.Contains(m.notice, "disabled") {
		t.Errorf("disabled: screen=%v notice=%q", m.screen, m.notice)
	}
}
