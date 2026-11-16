package app

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/puriice/godwit/internal/domain"
)

// prefixedStore is a fakeStore whose password keys carry a service, as the
// file store's do.
type prefixedStore struct {
	*fakeStore
	prefix string
}

func (p prefixedStore) PasswordKey(t string) string {
	return "GODWIT_" + p.prefix + strings.ToUpper(t) + "_PASSWORD"
}

// workspace is a Service over a workspace root with the global target "dev",
// a service "users" (in services/users, with its own target "prod" and
// migrations in "src") and a service "orders" (which shadows "dev").
type workspace struct {
	svc                *Service
	db                 *fakeDB
	dbs                *fakeFactory
	src                *fakeSource
	root, users, order *fakeStore
}

func newWorkspace(t *testing.T) *workspace {
	t.Helper()
	w := &workspace{db: newFakeDB(), src: &fakeSource{migs: []*domain.Migration{mig(1, "a")}},
		root: newFakeStore(), users: newFakeStore(), order: newFakeStore()}
	w.dbs = &fakeFactory{db: w.db}
	w.root.project = domain.Project{Services: []string{"services/users", "services/orders"},
		Targets: []domain.Target{{Name: "dev", Driver: "postgres"}}}
	w.root.pw["dev"] = "root-pw"
	w.users.project = domain.Project{Service: "users", MigrationsDir: "src",
		Targets: []domain.Target{{Name: "prod", Driver: "postgres"}}}
	w.users.pw["prod"] = "users-pw"
	w.order.project = domain.Project{Service: "orders", Targets: []domain.Target{{Name: "dev", Driver: "mysql"}}}
	w.order.pw["dev"] = "orders-pw"

	svc, err := New(w.root, w.src, w.dbs)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.LoadService("users", "services/users", prefixedStore{w.users, "USERS_"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.LoadService("orders", "services/orders", prefixedStore{w.order, "ORDERS_"}); err != nil {
		t.Fatal(err)
	}
	w.svc = svc
	return w
}

func refIn(service, target string) domain.TargetRef {
	return domain.TargetRef{Service: service, Target: target}
}

func TestWorkspaceTargets(t *testing.T) {
	w := newWorkspace(t)
	svc := w.svc
	if !svc.IsWorkspace() || !slices.Equal(svc.Services(), []string{"users", "orders"}) {
		t.Fatalf("services = %v", svc.Services())
	}
	want := []domain.TargetRef{refIn("users", "prod"), refIn("users", "dev"), refIn("orders", "dev")}
	if got := svc.TargetRefs(); !slices.Equal(got, want) {
		t.Errorf("TargetRefs = %v, want %v", got, want)
	}

	// users uses the global dev; orders shadows it with its own.
	if tg, _ := svc.Target(refIn("users", "dev")); tg.Driver != "postgres" || !svc.IsGlobal(refIn("users", "dev")) {
		t.Errorf("users/dev = %+v", tg)
	}
	if tg, _ := svc.Target(refIn("orders", "dev")); tg.Driver != "mysql" || svc.IsGlobal(refIn("orders", "dev")) {
		t.Errorf("orders/dev = %+v", tg)
	}
	if _, ok := svc.Target(refIn("orders", "prod")); ok {
		t.Error("orders must not see the users target")
	}

	// Passwords come from where the target is defined.
	for ref, key := range map[domain.TargetRef]string{
		refIn("users", "prod"): "GODWIT_USERS_PROD_PASSWORD",
		refIn("users", "dev"):  "GODWIT_DEV_PASSWORD",
		refIn("orders", "dev"): "GODWIT_ORDERS_DEV_PASSWORD",
	} {
		if got := svc.PasswordKey(ref); got != key {
			t.Errorf("PasswordKey(%v) = %q, want %q", ref, got, key)
		}
	}

	// The current service narrows the default scope.
	if err := svc.SetCurrentService("users"); err != nil {
		t.Fatal(err)
	}
	if got := svc.TargetRefs(); len(got) != 2 || got[0].Service != "users" {
		t.Errorf("inside users: %v", got)
	}
	if svc.SetCurrentService("nope") == nil {
		t.Error("unknown current service accepted")
	}
}

func TestWorkspaceRunsUseTheServiceTableAndFiles(t *testing.T) {
	w := newWorkspace(t)
	ctx := context.Background()
	if _, err := w.svc.Up(ctx, refIn("users", "dev"), 0, nil); err != nil {
		t.Fatal(err)
	}
	if w.dbs.last != "godwit_migration_users" || w.dbs.lastPassword != "root-pw" || w.dbs.lastTarget.Driver != "postgres" {
		t.Errorf("opened %+v pw=%q table=%q", w.dbs.lastTarget, w.dbs.lastPassword, w.dbs.last)
	}
	if got := w.src.loaded[len(w.src.loaded)-1]; got != filepath.Join("services", "users", "src") {
		t.Errorf("migrations read from %q", got)
	}
	if _, err := w.svc.Status(ctx, refIn("orders", "dev")); err != nil {
		t.Fatal(err)
	}
	if w.dbs.last != "godwit_migration_orders" || w.dbs.lastPassword != "orders-pw" {
		t.Errorf("orders opened table=%q pw=%q", w.dbs.last, w.dbs.lastPassword)
	}
	if got := w.src.loaded[len(w.src.loaded)-1]; got != filepath.Join("services", "orders", "migrations") {
		t.Errorf("orders migrations read from %q", got)
	}

	// A global target alone does not say which service to migrate.
	if _, err := w.svc.Up(ctx, refIn("", "dev"), 0, nil); err == nil {
		t.Error("a run without a service in a workspace succeeded")
	}
	if _, err := w.svc.Migrations(""); err == nil {
		t.Error("the workspace root has no migrations")
	}
}

func TestWorkspaceConfigGoesToTheOwner(t *testing.T) {
	w := newWorkspace(t)
	svc := w.svc
	rootSaves, usersSaves := w.root.saves, w.users.saves

	if err := svc.AddTarget("", domain.Target{Name: "staging", Driver: "postgres"}, "pw", true); err != nil {
		t.Fatal(err)
	}
	if w.root.saves != rootSaves+1 || w.users.saves != usersSaves || w.root.pw["staging"] != "pw" {
		t.Errorf("global add: root saves %d users saves %d", w.root.saves, w.users.saves)
	}
	if err := svc.AddTarget("users", domain.Target{Name: "dev", Driver: "mysql"}, "", true); err != nil {
		t.Errorf("a service may shadow a global target: %v", err)
	}
	if err := svc.AddTarget("users", domain.Target{Name: "a/b", Driver: "mysql"}, "", true); err == nil {
		t.Error("a target name with / accepted")
	}

	// Editing or disabling a global target through a service changes it once,
	// at the root.
	if err := svc.SetTargetEnabled(refIn("orders", "staging"), false); err != nil {
		t.Fatal(err)
	}
	if tg, _ := svc.Target(refIn("", "staging")); !tg.Disabled {
		t.Error("global target not disabled")
	}
	if err := svc.UpdateTarget(refIn("orders", "staging"), domain.Target{Name: "staging", Driver: "mysql"}, "", true); err != nil {
		t.Fatal(err)
	}
	if tg, _ := svc.Target(refIn("users", "staging")); tg.Driver != "mysql" {
		t.Errorf("users sees %+v", tg)
	}
	if err := svc.RemoveTarget(refIn("users", "staging")); err == nil || !strings.Contains(err.Error(), "global") {
		t.Errorf("removing a global target through a service: %v", err)
	}
	if err := svc.RemoveTarget(refIn("", "staging")); err != nil {
		t.Fatal(err)
	}

	// Per-service defaults.
	if err := svc.SetMigrationEnabledDefault("users", 1, false); err != nil {
		t.Fatal(err)
	}
	if !svc.MigrationDisabled(refIn("users", "prod"), 1) || svc.MigrationDisabled(refIn("orders", "dev"), 1) {
		t.Error("a service default leaked into another service")
	}

}

func TestGlobalPasswordAskedOnce(t *testing.T) {
	w := newWorkspace(t)
	w.order.project.Targets = nil // orders now uses the global dev too
	w = func() *workspace {
		svc, _ := New(w.root, w.src, w.dbs)
		svc.LoadService("users", "services/users", prefixedStore{w.users, "USERS_"})    //nolint:errcheck
		svc.LoadService("orders", "services/orders", prefixedStore{w.order, "ORDERS_"}) //nolint:errcheck
		w.svc = svc
		return w
	}()
	delete(w.root.pw, "dev")
	if got := w.svc.TargetsMissingPassword(); !slices.Equal(got, []domain.TargetRef{refIn("", "dev")}) {
		t.Errorf("missing = %v", got)
	}
	if err := w.svc.SetPassword(refIn("", "dev"), "pw", true); err != nil || w.root.pw["dev"] != "pw" {
		t.Errorf("set: %v %v", err, w.root.pw)
	}
}

func TestAddAndRemoveService(t *testing.T) {
	plain := newFakeStore()
	plain.project.Targets = []domain.Target{{Name: "dev", Driver: "postgres"}}
	svc, err := New(plain, &fakeSource{}, &fakeFactory{db: newFakeDB()})
	if err != nil {
		t.Fatal(err)
	}
	if svc.IsWorkspace() {
		t.Fatal("plain project reported as a workspace")
	}
	st := newFakeStore()
	if err := svc.AddService("users", "services/users", st); err != nil {
		t.Fatal(err)
	}
	if !svc.IsWorkspace() || !slices.Equal(plain.project.Services, []string{"services/users"}) {
		t.Fatalf("root = %+v", plain.project)
	}
	if st.project.Service != "users" || st.project.MigrationsDir != "migrations" || st.saves != 1 {
		t.Errorf("service config = %+v saves=%d", st.project, st.saves)
	}
	// The plain project's target became a global one.
	if !svc.IsGlobal(refIn("users", "dev")) {
		t.Error("dev should be global now")
	}
	for _, bad := range []struct{ name, dir string }{{"users", "other"}, {"other", "services/users"}, {"Bad", "x"}} {
		if err := svc.AddService(bad.name, bad.dir, newFakeStore()); err == nil {
			t.Errorf("AddService(%q, %q) accepted", bad.name, bad.dir)
		}
	}
	if err := svc.LoadService("users", "again", newFakeStore()); err == nil {
		t.Error("two services with one name accepted")
	}
	if err := svc.RemoveService("users"); err != nil {
		t.Fatal(err)
	}
	if len(plain.project.Services) != 0 || len(svc.Services()) != 0 {
		t.Errorf("after remove: %+v", plain.project.Services)
	}
	if svc.RemoveService("users") == nil {
		t.Error("removing twice succeeded")
	}
}

func TestPlainProjectRefs(t *testing.T) {
	h := newHarness(t)
	if h.svc.IsWorkspace() || !slices.Equal(h.svc.DefaultServices(), []string{""}) {
		t.Errorf("default services = %v", h.svc.DefaultServices())
	}
	if got := h.svc.TargetRefs(); !slices.Equal(got, []domain.TargetRef{ref("t")}) {
		t.Errorf("refs = %v", got)
	}
	if _, err := h.svc.Status(context.Background(), refIn("x", "t")); err == nil || !strings.Contains(err.Error(), "not a workspace") {
		t.Errorf("service ref on a plain project: %v", err)
	}
}
