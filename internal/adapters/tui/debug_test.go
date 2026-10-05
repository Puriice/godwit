package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/puriice/godwit/internal/adapters/filestore"
	"github.com/puriice/godwit/internal/adapters/fsmigrations"
	"github.com/puriice/godwit/internal/adapters/sqldb"
	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

func arrow(s string) tea.KeyMsg {
	if s == "left" {
		return tea.KeyMsg{Type: tea.KeyLeft}
	}
	return tea.KeyMsg{Type: tea.KeyRight}
}

// debugModel opens the migrations screen of target "a" holding the given items.
func debugModel(t *testing.T, items ...domain.Item) (*Model, *targetState) {
	t.Helper()
	m := testModel(t)
	st := m.state("a")
	st.loaded, st.items = true, items
	m.Update(key("enter"))
	if m.screen != scrMigrations {
		t.Fatalf("screen = %v", m.screen)
	}
	return m, st
}

func item(v int64, name string, state domain.State, rec *domain.Record) domain.Item {
	return domain.Item{Version: v, Name: name, State: state, Record: rec,
		Migration: &domain.Migration{Version: v, Name: name, Checksum: "filesum1234"}}
}

func TestDebugPanelSwitchesWithArrows(t *testing.T) {
	m, _ := debugModel(t, item(1, "x", domain.Pending, nil))
	if m.mdebug || !strings.Contains(m.View(), "[migrations]") {
		t.Fatalf("starts on the list:\n%s", m.View())
	}
	m.Update(arrow("right"))
	if !m.mdebug || !strings.Contains(m.View(), "[debug]") || !strings.Contains(m.View(), "Record only; schema untouched") {
		t.Errorf("right should open the debug panel:\n%s", m.View())
	}
	m.Update(arrow("left"))
	if m.mdebug || strings.Contains(m.View(), "Record only; schema untouched") {
		t.Errorf("left should return to the list:\n%s", m.View())
	}

	// Leaving the target and coming back starts on the list again.
	m.Update(arrow("right"))
	m.Update(key("esc"))
	m.Update(key("enter"))
	if m.mdebug {
		t.Error("debug panel should not survive leaving the target")
	}
}

func TestDebugPanelFollowsCursorAndShowsRecord(t *testing.T) {
	rec := &domain.Record{Version: 2, Name: "two", Checksum: "recsum99999", Batch: 4, DurationMS: 12}
	m, _ := debugModel(t, item(1, "one", domain.Pending, nil), item(2, "two", domain.Applied, rec))
	m.Update(arrow("right"))

	view := m.View()
	if !strings.Contains(view, "1_one") || !strings.Contains(view, "recorded  pending") {
		t.Errorf("first migration:\n%s", view)
	}
	m.Update(key("down"))
	view = m.View()
	for _, want := range []string{"2_two", "recorded  applied", "recsum99…", "batch     4", "12 ms"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	if !strings.Contains(view, "p force pending") {
		t.Errorf("debug help missing:\n%s", view)
	}
}

func TestDebugListKeysAreInertInDebugPanel(t *testing.T) {
	m, st := debugModel(t, item(1, "x", domain.Pending, nil))
	m.Update(arrow("right"))
	m.Update(key("N"))
	if st.running || m.screen != scrMigrations {
		t.Errorf("N must not run anything from the debug panel (running=%v screen=%v)", st.running, m.screen)
	}
}

func TestDebugForceAsksForConfirmation(t *testing.T) {
	applied := item(1, "x", domain.Applied, &domain.Record{Version: 1, Name: "x", Checksum: "filesum1234"})
	for _, tc := range []struct {
		key  string
		item domain.Item
		ask  bool
		note string
	}{
		{"p", applied, true, ""},
		{"d", applied, true, ""},
		{"a", applied, false, "already recorded as applied"},
		{"a", item(1, "x", domain.Pending, nil), true, ""},
		{"p", item(1, "x", domain.Pending, nil), false, "already recorded as pending"},
		// A changed file is still recorded as applied, so forcing applied is a no-op.
		{"a", item(1, "x", domain.Modified, applied.Record), false, "already recorded as applied"},
	} {
		m, _ := debugModel(t, tc.item)
		m.Update(arrow("right"))
		m.Update(key(tc.key))
		if got := m.screen == scrForm; got != tc.ask {
			t.Errorf("%s on %s: confirm shown = %v, want %v", tc.key, tc.item.State, got, tc.ask)
		}
		if !strings.Contains(m.notice, tc.note) {
			t.Errorf("%s on %s: notice %q, want %q", tc.key, tc.item.State, m.notice, tc.note)
		}
	}
}

func TestDebugForceRefusedWhileBusyOrDisabled(t *testing.T) {
	m, st := debugModel(t, item(1, "x", domain.Pending, nil))
	m.Update(arrow("right"))
	st.running = true
	m.Update(key("a"))
	if m.screen == scrForm || !strings.Contains(m.notice, "in progress") {
		t.Errorf("running: screen=%v notice=%q", m.screen, m.notice)
	}
	st.running = false

	m.svc.SetTargetEnabled("a", false)
	st.loaded, st.items = true, []domain.Item{item(1, "x", domain.Pending, nil)}
	m.Update(key("a"))
	if m.screen == scrForm || !strings.Contains(m.notice, "disabled") {
		t.Errorf("disabled: screen=%v notice=%q", m.screen, m.notice)
	}
}

func TestForcedMsg(t *testing.T) {
	m, st := debugModel(t, item(1, "x", domain.Pending, nil))
	m.Update(forcedMsg{target: "a", label: "1_x", to: domain.Applied, err: errors.New("boom")})
	if !strings.Contains(m.notice, "boom") || len(st.log) == 0 || !strings.Contains(st.log[len(st.log)-1], "boom") {
		t.Errorf("error not reported: notice=%q log=%v", m.notice, st.log)
	}
	_, cmd := m.Update(forcedMsg{target: "a", label: "1_x", to: domain.Applied})
	if cmd == nil || !strings.Contains(st.log[len(st.log)-1], "forced 1_x to applied") {
		t.Errorf("success: cmd=%v log=%v", cmd, st.log)
	}
}

// TestDebugForceEndToEnd forces states through the real service on SQLite.
func TestDebugForceEndToEnd(t *testing.T) {
	root := t.TempDir()
	factory := sqldb.NewFactory()
	svc, err := app.New(filestore.New(root), fsmigrations.New(root, factory.Drivers()), factory)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureMigrationsDir(); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, svc.MigrationsDir(), "1_one.sql")
	if err := os.WriteFile(file, []byte("-- +godwit Up\nCREATE TABLE one (id INTEGER);\n-- +godwit Down\nDROP TABLE one;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddTarget(domain.Target{Name: "l", Driver: "sqlite", Database: filepath.Join(root, "l.db")}, "", false); err != nil {
		t.Fatal(err)
	}

	m := New(svc)
	tg, _ := svc.Target("l")
	ctx := context.Background()
	status := func() domain.State {
		t.Helper()
		items, err := svc.Status(ctx, "l")
		if err != nil || len(items) != 1 {
			t.Fatalf("status: %v %v", items, err)
		}
		return items[0].State
	}
	force := func(to domain.State) {
		t.Helper()
		msg := m.forceState(tg, 1, "1_one", to)()
		fm, ok := msg.(forcedMsg)
		if !ok || fm.err != nil {
			t.Fatalf("force %s: %+v", to, msg)
		}
	}

	if got := status(); got != domain.Pending {
		t.Fatalf("start = %s", got)
	}
	for _, to := range []domain.State{domain.Applied, domain.Dirty, domain.Applied, domain.Pending, domain.Dirty} {
		force(to)
		if got := status(); got != to {
			t.Fatalf("after forcing %s, status = %s", to, got)
		}
	}
	// The schema was never touched: the table the migration creates still doesn't exist.
	if _, err := svc.Up(ctx, "l", 0, nil); err == nil {
		t.Error("a dirty migration should block a run")
	}
	force(domain.Pending)
	if n, err := svc.Up(ctx, "l", 0, nil); err != nil || n != 1 {
		t.Errorf("after forcing pending the migration should apply: n=%d err=%v", n, err)
	}
}

func TestDebugDetailIsASecondColumn(t *testing.T) {
	rec := &domain.Record{Version: 2, Name: "two", Checksum: "recsum99999", Batch: 4}
	m, _ := debugModel(t, item(1, "one", domain.Pending, nil), item(2, "two", domain.Applied, rec))
	m.Update(arrow("right"))

	// Wide enough: the selected row and the detail title share a line, and no
	// line is wider than the terminal.
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	var title string
	for _, l := range strings.Split(m.View(), "\n") {
		if strings.Contains(l, "1_one") {
			title = l
		}
		if lipgloss.Width(l) > 80 {
			t.Errorf("line wider than the terminal (%d): %q", lipgloss.Width(l), l)
		}
	}
	if !strings.Contains(title, "›") || !strings.Contains(title, "pending") {
		t.Errorf("detail should sit beside the selected row, got %q", title)
	}

	// Too narrow for two columns: the detail goes below the list instead.
	m.Update(tea.WindowSizeMsg{Width: 50, Height: 40})
	for _, l := range strings.Split(m.View(), "\n") {
		if strings.Contains(l, "1_one") && strings.Contains(l, "›") {
			t.Errorf("narrow terminal should stack the detail: %q", l)
		}
		if lipgloss.Width(l) > 50 {
			t.Errorf("line wider than the terminal (%d): %q", lipgloss.Width(l), l)
		}
	}
	if !strings.Contains(m.View(), "1_one") || !strings.Contains(m.View(), "recorded  pending") {
		t.Errorf("stacked detail missing:\n%s", m.View())
	}
}

// Switching tabs must not move the list: each row's version, name and state sit
// at the same screen columns in both.
func TestListRowsDoNotShiftBetweenTabs(t *testing.T) {
	rec := &domain.Record{Version: 20261006100100, Name: "split_batch_test_full_name", Checksum: "recsum99999", Batch: 4}
	items := []domain.Item{
		item(20261006100000, "create_batch_test_users", domain.Pending, nil),
		item(20261006100100, "split_batch_test_full_name", domain.Applied, rec),
	}
	for _, width := range []int{60, 80, 120} {
		m, _ := debugModel(t, items...)
		m.Update(tea.WindowSizeMsg{Width: width, Height: 30})

		col := func(view, needle string) int {
			for _, l := range strings.Split(view, "\n") {
				if i := strings.Index(l, needle); i >= 0 {
					return lipgloss.Width(l[:i])
				}
			}
			return -1
		}
		list := m.View()
		m.Update(arrow("right"))
		debug := m.View()
		for _, needle := range []string{"20261006100000", "create_batch", "pending"} {
			a, b := col(list, needle), col(debug, needle)
			if a < 0 || a != b {
				t.Errorf("width %d: %q is at column %d in the list tab and %d in the debug tab", width, needle, a, b)
			}
		}
	}
}

// On a very wide terminal the detail column stays next to the list instead of
// drifting to the right edge.
func TestDetailColumnStaysCloseOnWideTerminals(t *testing.T) {
	items := []domain.Item{item(20261006100000, "create_batch_test_users", domain.Pending, nil)}
	var at []int
	for _, width := range []int{120, 200, 400} {
		m, _ := debugModel(t, items...)
		m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		m.Update(arrow("right"))
		for _, l := range strings.Split(m.View(), "\n") {
			if i := strings.Index(l, "20261006100000_create"); i >= 0 {
				at = append(at, lipgloss.Width(l[:i]))
			}
		}
	}
	if len(at) != 3 {
		t.Fatalf("detail title not found: %v", at)
	}
	if at[0] != at[1] || at[1] != at[2] {
		t.Errorf("detail starts at columns %v, want the same at every wide width", at)
	}
	if max := rowWidth(maxName) + sideGap; at[2] > max {
		t.Errorf("detail starts at column %d, want at most %d", at[2], max)
	}
}
