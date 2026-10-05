package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
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
	m.Update(key("N"))
	if st.running || !strings.Contains(m.notice, "disabled") {
		t.Errorf("N on a disabled target: running=%v notice=%q", st.running, m.notice)
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

	// Dirty and missing migrations cannot be redone; the reason is shown.
	for cursor, want := range map[int]string{3: "dirty", 4: "no migration file"} {
		m.mcur, m.notice = cursor, ""
		m.Update(key(" "))
		if m.screen != scrMigrations || !strings.Contains(m.notice, want) {
			t.Errorf("cursor %d: screen=%v notice=%q, want notice containing %q", cursor, m.screen, m.notice, want)
		}
	}

	// An applied migration asks for confirmation first, and warns about later ones.
	m.mcur = 0
	m.Update(key(" "))
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

	// A disabled target ignores space, like the other action keys.
	m.svc.SetTargetEnabled("a", false)
	m.mcur, m.notice = 0, ""
	m.Update(key(" "))
	if m.screen != scrMigrations || !strings.Contains(m.notice, "disabled") {
		t.Errorf("disabled: screen=%v notice=%q", m.screen, m.notice)
	}
}

func TestGoToSelectedMigration(t *testing.T) {
	m := testModel(t)
	a, _ := m.svc.Target("a")
	st := m.state("a")
	rec := &domain.Record{}
	st.loaded = true
	st.items = []domain.Item{
		{Version: 1, Name: "users", State: domain.Applied, Record: rec},
		{Version: 2, Name: "posts", State: domain.Applied, Record: rec},
		{Version: 3, Name: "tags", State: domain.Modified, Record: rec},
		{Version: 4, Name: "comments", State: domain.Pending},
		{Version: 5, Name: "indexes", State: domain.Pending},
		{Version: 6, Name: "broken", State: domain.Dirty, Record: rec},
		{Version: 7, Name: "ghost", State: domain.Missing, Record: rec},
	}
	m.screen = scrMigrations // target "a" is selected
	press := func(cursor int) {
		m.mcur, m.notice = cursor, ""
		m.Update(key("enter"))
	}

	// Applied (or modified) and not the newest: asks before rolling back, and says how many.
	press(0)
	if m.screen != scrForm || m.formTitle != "Confirm" || st.running {
		t.Fatalf("expected a confirm form: screen=%v title=%q running=%v", m.screen, m.formTitle, st.running)
	}
	// 2, 3, 6 and 7 are applied and newer than 1; pending 4 and 5 are not counted.
	if !strings.Contains(m.View(), "Roll back 4 migration(s)") || !strings.Contains(m.View(), "1_users is the latest applied") {
		t.Errorf("confirm text:\n%s", m.View())
	}
	m.Update(key("esc"))
	if m.screen != scrMigrations || st.running {
		t.Errorf("cancel: screen=%v running=%v", m.screen, st.running)
	}

	// Already the latest applied (nothing newer applied): nothing to do.
	st.items = st.items[:3]
	press(2)
	if m.screen != scrMigrations || st.running || !strings.Contains(m.notice, "already the latest") {
		t.Errorf("latest: screen=%v running=%v notice=%q", m.screen, st.running, m.notice)
	}
	st.items = append(st.items,
		domain.Item{Version: 4, Name: "comments", State: domain.Pending},
		domain.Item{Version: 6, Name: "broken", State: domain.Dirty, Record: rec},
		domain.Item{Version: 7, Name: "ghost", State: domain.Missing, Record: rec})

	// Dirty and missing migrations cannot be used as a target.
	press(4)
	if !strings.Contains(m.notice, "dirty") || m.screen != scrMigrations {
		t.Errorf("dirty: screen=%v notice=%q", m.screen, m.notice)
	}
	press(5)
	if !strings.Contains(m.notice, "no migration file") || m.screen != scrMigrations {
		t.Errorf("missing: screen=%v notice=%q", m.screen, m.notice)
	}

	// Pending: asks first too (Enter is easy to hit), saying how many it will apply.
	press(3)
	if m.screen != scrForm || m.formTitle != "Confirm" || st.running {
		t.Fatalf("pending: expected a confirm form, screen=%v title=%q running=%v", m.screen, m.formTitle, st.running)
	}
	if !strings.Contains(m.View(), "Apply 1 pending migration(s)") || !strings.Contains(m.View(), "4_comments") {
		t.Errorf("confirm text:\n%s", m.View())
	}
	m.Update(key("esc"))
	if m.screen != scrMigrations || st.running {
		t.Errorf("cancel: screen=%v running=%v", m.screen, st.running)
	}
	// Confirming starts an "up to" background run.
	cmd := m.upTo(a, 4)
	if cmd == nil || !st.running {
		t.Fatal("upTo should start a background run")
	}
	done, ok := cmd().(runDoneMsg) // the fake database factory fails right away
	if !ok || done.dir != domain.Up || done.err == nil {
		t.Fatalf("done = %+v", done)
	}
	m.Update(done)

	// downTo is also a background "down" run.
	if cmd := m.downTo(a, 1); cmd == nil || !st.running {
		t.Fatal("downTo should start a background run")
	} else if d, ok := cmd().(runDoneMsg); !ok || d.dir != domain.Down {
		t.Errorf("downTo done = %+v", d)
	} else {
		m.Update(d)
	}

	// A disabled target ignores g like the other action keys.
	m.svc.SetTargetEnabled("a", false)
	press(0)
	if m.screen != scrMigrations || !strings.Contains(m.notice, "disabled") {
		t.Errorf("disabled: screen=%v notice=%q", m.screen, m.notice)
	}
}

func TestPanelCycling(t *testing.T) {
	m := testModel(t)
	left, right := tea.KeyMsg{Type: tea.KeyLeft}, tea.KeyMsg{Type: tea.KeyRight}

	for _, want := range []screen{scrFiles, scrPlugins, scrTargets} {
		m.Update(right)
		if m.screen != want {
			t.Fatalf("right: screen = %v, want %v", m.screen, want)
		}
	}
	m.Update(left)
	if m.screen != scrPlugins {
		t.Fatalf("left from targets should wrap to plugins, got %v", m.screen)
	}
	if v := m.View(); !strings.Contains(v, "[plugins]") || !strings.Contains(v, "No plugins") {
		t.Errorf("plugins panel not shown:\n%s", v)
	}
}

func TestMigrationFilesPanel(t *testing.T) {
	m := testModel(t)
	if strings.Contains(m.View(), "new migration") {
		t.Error("targets panel should no longer offer n")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if v := m.View(); !strings.Contains(v, "[migrations]") || !strings.Contains(v, "No migration files") {
		t.Fatalf("files panel not shown:\n%s", v)
	}
	m.Update(key("n"))
	if m.screen != scrForm {
		t.Fatalf("n should open the new migration form, screen = %v", m.screen)
	}
	m.closeForm()
	if m.screen != scrFiles {
		t.Errorf("closing the form should return to the files panel, got %v", m.screen)
	}
	if _, err := m.svc.CreateMigration("add_things"); err != nil {
		t.Fatal(err)
	}
	if v := m.View(); !strings.Contains(v, "add_things") {
		t.Errorf("new file not listed:\n%s", v)
	}
}

func TestPluginsPanelAddAndRemove(t *testing.T) {
	m := testModel(t)
	m.screen = scrPlugins

	msg, err := m.registerPlugin(domain.PluginSpec{Name: "Demo", Command: "demo-bin"}, scopeProject)
	if err != nil || !strings.Contains(msg, "added") {
		t.Fatalf("register: %q, %v", msg, err)
	}
	if again, err := m.registerPlugin(domain.PluginSpec{Name: "demo", Command: "demo-bin"}, scopeProject); err != nil || !strings.Contains(again, "already") {
		t.Errorf("same plugin again should be a no-op: %q, %v", again, err)
	}
	if _, err := m.registerPlugin(domain.PluginSpec{Name: "demo", Command: "other"}, scopeProject); err == nil {
		t.Error("a different command under the same name should be refused")
	}
	if _, err := m.registerPlugin(domain.PluginSpec{Command: "x"}, scopeProject); err == nil {
		t.Error("without a probe, a name is required")
	}
	if v := m.View(); !strings.Contains(v, "demo") || !strings.Contains(v, "demo-bin") {
		t.Errorf("plugin not listed:\n%s", v)
	}

	m.Update(key("x"))
	if m.screen != scrForm {
		t.Fatalf("x should ask for confirmation, screen = %v", m.screen)
	}
	m.closeForm()
	if len(m.svc.Plugins()) != 1 {
		t.Error("cancelling must keep the plugin")
	}
}

func TestMigrateAllToSelectedFile(t *testing.T) {
	m := testModel(t)
	for _, n := range []string{"one", "two"} {
		if _, err := m.svc.CreateMigration(n); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1100 * time.Millisecond) // versions are timestamps
	}
	migs, _ := m.svc.Migrations()
	if len(migs) != 2 {
		t.Fatalf("migrations = %d", len(migs))
	}
	// Nothing loaded yet: nothing can be planned.
	m.screen, m.fcur = scrFiles, 0
	m.Update(key("enter"))
	if m.screen != scrFiles || !strings.Contains(m.notice, "already") {
		t.Fatalf("unloaded targets should be skipped, screen %v notice %q", m.screen, m.notice)
	}
	// a: first pending; b: both applied, so it rolls back to the first.
	m.state("a").loaded = true
	m.state("a").items = []domain.Item{{Version: migs[0].Version, State: domain.Pending}, {Version: migs[1].Version, State: domain.Pending}}
	rec := &domain.Record{}
	m.state("b").loaded = true
	m.state("b").items = []domain.Item{{Version: migs[0].Version, State: domain.Applied, Record: rec}, {Version: migs[1].Version, State: domain.Applied, Record: rec}}
	m.Update(key("enter"))
	if m.screen != scrForm {
		t.Fatalf("enter should ask for confirmation, screen = %v", m.screen)
	}
	if v := m.View(); !strings.Contains(strings.ReplaceAll(v, "\n", " "), "Roll back") {
		t.Errorf("confirmation should name the rollback:\n%s", v)
	}
}

func TestRollbackBatchAsksFirst(t *testing.T) {
	m := testModel(t)
	st := m.state("a")
	st.loaded = true
	mk := func(v int64, batch int64, at int) domain.Item {
		return domain.Item{Version: v, Name: "m", State: domain.Applied, Migration: &domain.Migration{Version: v},
			Record: &domain.Record{Version: v, Batch: batch, AppliedAt: time.Unix(int64(at), 0)}}
	}
	st.items = []domain.Item{mk(1, 1, 10), mk(2, 2, 20), mk(3, 2, 20)}
	m.screen = scrMigrations

	m.Update(key("B"))
	if m.screen != scrForm || m.formTitle != "Confirm" {
		t.Fatalf("expected a confirm form, screen=%v title=%q", m.screen, m.formTitle)
	}
	if v := m.View(); !strings.Contains(v, "2 migration(s)") || !strings.Contains(v, "3_m") {
		t.Errorf("confirm should list the latest batch:\n%s", v)
	}
	if st.running {
		t.Error("nothing may run before the user confirms")
	}
	m.Update(key("esc"))

	st.items = nil
	m.Update(key("B"))
	if m.screen != scrMigrations || !strings.Contains(m.notice, "nothing to roll back") {
		t.Errorf("empty: screen=%v notice=%q", m.screen, m.notice)
	}
}
