package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/puriice/godwit/internal/adapters/filestore"
	"github.com/puriice/godwit/internal/adapters/fsmigrations"
	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

// workspaceModel is a model over a workspace with services users (own
// target prod, one migration file) and orders (no files), and a global target
// dev.
func workspaceModel(t *testing.T) (*Model, string) {
	t.Helper()
	t.Setenv("GODWIT_DEV_PASSWORD", "x")
	t.Setenv("GODWIT_USERS_PROD_PASSWORD", "x")
	root := t.TempDir()
	svc, err := app.New(filestore.New(root), fsmigrations.New(root, noDBs{}.Drivers()), noDBs{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"users", "orders"} {
		dir := "services/" + name
		if err := svc.AddService(name, dir, filestore.NewService(filepath.Join(root, "services", name), name)); err != nil {
			t.Fatal(err)
		}
	}
	tg := domain.Target{Host: "h", Database: "d", User: "u"}
	dev, prod := tg, tg
	dev.Name, dev.Driver = "dev", "postgres"
	prod.Name, prod.Driver = "prod", "mysql"
	if err := svc.AddTarget("", dev, "", false); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddTarget("users", prod, "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateMigration("users", "create_users"); err != nil {
		t.Fatal(err)
	}
	m := New(svc)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return m, root
}

func TestWorkspaceTargetsPanel(t *testing.T) {
	m, _ := workspaceModel(t)
	if len(m.credQueue) != 0 {
		t.Errorf("passwords from env should satisfy credentials: %v", m.credQueue)
	}
	for _, key := range []string{"users/prod", "users/dev", "orders/dev"} {
		if _, ok := m.states[key]; !ok {
			t.Errorf("no state for %s: %v", key, m.states)
		}
	}
	view := m.View()
	users, orders := strings.Index(view, "users "), strings.Index(view, "orders ")
	if users < 0 || orders < users {
		t.Fatalf("service headings missing or out of order:\n%s", view)
	}
	if !strings.Contains(view, "1 migration file(s) in") || !strings.Contains(view, "· global") {
		t.Errorf("view:\n%s", view)
	}
	// The cursor walks targets, not headings: the third row is orders/dev.
	m.Update(key("down"))
	m.Update(key("down"))
	if r, ok := m.currentTarget(); !ok || r.ref != (domain.TargetRef{Service: "orders", Target: "dev"}) {
		t.Errorf("third row = %+v", r.ref)
	}
	// x on a global row explains instead of removing.
	m.Update(key("x"))
	if m.screen == scrForm || !strings.Contains(m.notice, "global") {
		t.Errorf("x on a global row: screen=%v notice=%q", m.screen, m.notice)
	}
}

func TestWorkspaceTargetsPanelFitsShortTerminal(t *testing.T) {
	m, _ := workspaceModel(t)
	for _, h := range []int{10, 14, 20} {
		m.Update(tea.WindowSizeMsg{Width: 100, Height: h})
		for range 3 {
			if lines := strings.Count(m.View(), "\n") + 1; lines > h {
				t.Errorf("height %d: %d lines", h, lines)
			}
			if !strings.Contains(m.View(), "›") {
				t.Errorf("height %d: cursor row not visible:\n%s", h, m.View())
			}
			m.Update(key("down"))
		}
		m.cursor = 0
	}
}

func TestWorkspaceAddTargetFormAsksScope(t *testing.T) {
	m, _ := workspaceModel(t)
	m.openTargetForm(nil)
	if view := m.View(); !strings.Contains(view, "Scope") {
		t.Errorf("no Scope field:\n%s", view)
	}
}

func TestWorkspaceFilesPanel(t *testing.T) {
	m, root := workspaceModel(t)
	m.screen = scrFiles
	view := m.View()
	if !strings.Contains(view, "create_users") || !strings.Contains(view, "orders") || !strings.Contains(view, "no migration files") {
		t.Errorf("files view:\n%s", view)
	}
	rows := m.fileRows()
	if len(rows) != 2 || rows[0].service != "users" || rows[1].service != "orders" || rows[1].file != nil {
		t.Fatalf("rows = %+v", rows)
	}
	// e on the users file disables it by default for users only.
	m.Update(key("e"))
	if !m.svc.MigrationDisabledByDefault("users", rows[0].file.Migration.Version) {
		t.Errorf("not disabled: %q", m.notice)
	}
	// The orders row has nothing to act on.
	m.Update(key("down"))
	m.Update(key("enter"))
	if !strings.Contains(m.notice, "no migration files in orders") {
		t.Errorf("notice = %q", m.notice)
	}
	// n creates in the selected service (the form asks, defaulting to it).
	m.Update(key("n"))
	if m.screen != scrForm || !strings.Contains(m.View(), "Service") {
		t.Fatalf("new migration form:\n%s", m.View())
	}
	if _, err := m.svc.CreateMigration("orders", "x"); err != nil {
		t.Fatal(err)
	}
	if files, _ := os.ReadDir(filepath.Join(root, "services", "orders", "migrations")); len(files) != 1 {
		t.Errorf("orders files = %d", len(files))
	}
}

// withServiceOps lets a model on root add services, as main wires it.
func withServiceOps(m *Model, root string) *Model {
	return m.WithServiceOps(ServiceOps{
		NewStore: func(dir, name string) app.ProjectStore {
			return filestore.NewService(filepath.Join(root, filepath.FromSlash(dir)), name)
		},
		CleanDir: filestore.CleanServiceDir,
	})
}

func TestServicesPanel(t *testing.T) {
	m, root := workspaceModel(t)
	withServiceOps(m, root)
	m.screen = scrServices
	view := m.View()
	for _, want := range []string{"[services]", "users", "services/users", "1 target(s) + 1 global", "godwit_migration_orders", "1 global target(s)"} {
		if !strings.Contains(view, want) {
			t.Errorf("view misses %q:\n%s", want, view)
		}
	}

	// a opens the form; adding a service shows it with the global target.
	m.Update(key("a"))
	if m.screen != scrForm || !strings.Contains(m.View(), "Directory") {
		t.Fatalf("add form:\n%s", m.View())
	}
	m.closeForm()
	m.addService("services/billing", "")
	if got := m.svc.Services(); len(got) != 3 || got[2] != "billing" || m.scur != 2 {
		t.Fatalf("services = %v cursor=%d notice=%q", got, m.scur, m.notice)
	}
	if _, ok := m.states["billing/dev"]; !ok {
		t.Errorf("no row for billing/dev: %v", m.states)
	}
	if _, err := os.Stat(filepath.Join(root, "services", "billing", "migrations")); err != nil {
		t.Errorf("migrations dir: %v", err)
	}
	for _, bad := range [][2]string{{"../outside", ""}, {"x", "Bad-Name"}, {"services/users", "other"}} {
		m.addService(bad[0], bad[1])
		if len(m.svc.Services()) != 3 || m.notice == "" {
			t.Errorf("addService(%q, %q) accepted: %q", bad[0], bad[1], m.notice)
		}
	}

	// x removes after a confirmation, and drops the service's rows.
	m.screen, m.scur = scrServices, 2
	m.Update(key("x"))
	if m.screen != scrForm || !strings.Contains(m.View(), "Remove service billing") {
		t.Fatalf("no confirmation:\n%s", m.View())
	}
	m.closeForm()
	if len(m.svc.Services()) != 3 {
		t.Fatal("removed before confirming")
	}
	m.removeService("billing")
	if got := m.svc.Services(); len(got) != 2 {
		t.Errorf("after remove: %v", got)
	}
	if _, ok := m.states["billing/dev"]; ok {
		t.Error("billing rows kept")
	}
}

func TestServicesPanelTurnsAPlainProjectIntoAWorkspace(t *testing.T) {
	m := testModel(t)
	root := t.TempDir()
	withServiceOps(m, root)
	m.screen = scrServices
	if !strings.Contains(m.View(), "plain project") {
		t.Errorf("view:\n%s", m.View())
	}
	m.addService("api", "")
	if !m.svc.IsWorkspace() || !strings.Contains(m.notice, "now a workspace") {
		t.Errorf("workspace=%v notice=%q", m.svc.IsWorkspace(), m.notice)
	}
	// The project's targets a and b are now global targets of api.
	if _, ok := m.states["api/a"]; !ok {
		t.Errorf("states = %v", m.states)
	}
}

func TestServicesPanelWithoutOps(t *testing.T) {
	m, _ := workspaceModel(t)
	m.screen = scrServices
	m.Update(key("a"))
	if m.screen == scrForm || !strings.Contains(m.notice, "godwit service add") {
		t.Errorf("screen=%v notice=%q", m.screen, m.notice)
	}
}

// A run on a global target is sent with its service, so the worker migrates
// that service's files and state table (not the workspace root, which has
// none).
func TestWorkspaceRunCarriesService(t *testing.T) {
	m, _ := workspaceModel(t)
	bg := &stubRunner{}
	m.WithBackground(bg)
	users, _ := m.row(domain.TargetRef{Service: "users", Target: "dev"})
	m.run(users, domain.Up, 0)
	m.upTo(users, 7)
	orders, _ := m.row(domain.TargetRef{Service: "orders", Target: "dev"})
	m.redo(orders, 3)
	want := []domain.Job{
		{Service: "users", Target: "dev", Op: domain.OpUp},
		{Service: "users", Target: "dev", Op: domain.OpUpTo, Version: 7},
		{Service: "orders", Target: "dev", Op: domain.OpRedo, Version: 3},
	}
	if len(bg.started) != len(want) {
		t.Fatalf("started = %+v", bg.started)
	}
	for i, j := range want {
		if bg.started[i] != j {
			t.Errorf("job %d = %+v, want %+v", i, bg.started[i], j)
		}
	}
}

// In-process runs (no background runner) resolve the service too: the run
// gets as far as opening the database.
func TestWorkspaceInProcessRunResolvesService(t *testing.T) {
	m, _ := workspaceModel(t)
	users, _ := m.row(domain.TargetRef{Service: "users", Target: "dev"})
	cmd := m.run(users, domain.Up, 0)
	var done runDoneMsg
	for msg := cmd(); ; msg = waitMsg(m.state(users.ref).events)() {
		if d, ok := msg.(runDoneMsg); ok {
			done = d
			break
		}
	}
	if done.err == nil || !strings.Contains(done.err.Error(), "no database in tests") {
		t.Errorf("run error = %v, want the fake's open error", done.err)
	}
}
