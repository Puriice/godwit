package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/puriice/godwit/internal/adapters/filestore"
	"github.com/puriice/godwit/internal/adapters/fsmigrations"
	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

func serviceOps(root string) ServiceOps {
	return ServiceOps{
		NewStore: func(dir, name string) app.ProjectStore {
			return filestore.NewService(filepath.Join(root, filepath.FromSlash(dir)), name)
		},
		CleanDir: filestore.CleanServiceDir,
	}
}

// newWorkspace makes a workspace in a temp dir with services users and
// orders, a global target dev and a users target prod, then loads it the way
// main does, as seen from cwd (relative to the root; "" is the root).
func newWorkspace(t *testing.T) (root string, load func(cwd string) *app.Service) {
	t.Helper()
	svc, root := newService(t)
	run := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	run(Service(svc, serviceOps(root), []string{"add", "services/users"}, &out))
	run(Service(svc, serviceOps(root), []string{"add", "services/orders"}, &out))
	run(Auth(svc, []string{"add", "dev", "postgres", "postgres://app:pw@localhost/dev"}, &out))
	run(Auth(svc, []string{"add", "-s", "users", "prod", "postgres", "postgres://app:pw@prod/users"}, &out))

	load = func(cwd string) *app.Service {
		t.Helper()
		layout, err := filestore.Discover(filepath.Join(root, filepath.FromSlash(cwd)))
		run(err)
		s, err := app.New(filestore.New(layout.Root), fsmigrations.New(layout.Root, noDBs{}.Drivers()), noDBs{})
		run(err)
		for _, si := range layout.Services {
			run(s.LoadService(si.Name, si.Dir, filestore.NewService(layout.Dir(si), si.Name)))
		}
		run(s.SetCurrentService(layout.Current))
		return s
	}
	return root, load
}

func TestServiceCommands(t *testing.T) {
	root, load := newWorkspace(t)
	svc := load("")
	var out bytes.Buffer
	if err := Service(svc, serviceOps(root), []string{"list"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"users", "services/users", "1 target(s) + 1 global", "orders", "0 target(s) + 1 global"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("list misses %q:\n%s", want, out.String())
		}
	}
	if _, err := os.Stat(filepath.Join(root, "services", "users", "migrations")); err != nil {
		t.Errorf("service add did not create the migrations dir: %v", err)
	}
	for _, bad := range [][]string{{"add", "../outside"}, {"add", "x", "Bad-Name"}, {"add", "services/users"}, {"remove", "nope"}} {
		if err := Service(svc, serviceOps(root), bad, &bytes.Buffer{}); err == nil {
			t.Errorf("service %v succeeded", bad)
		}
	}
	out.Reset()
	if err := Service(svc, serviceOps(root), []string{"remove", "orders"}, &out); err != nil {
		t.Fatal(err)
	}
	if got := load("").Services(); len(got) != 1 || got[0] != "users" {
		t.Errorf("after remove: %v", got)
	}
}

func TestAuthInWorkspace(t *testing.T) {
	_, load := newWorkspace(t)
	var out bytes.Buffer
	if err := Auth(load(""), []string{"list"}, &out); err != nil {
		t.Fatal(err)
	}
	want := []string{"== global ==", "dev", "== users ==", "prod", "(global)", "== orders =="}
	pos := 0
	for _, w := range want {
		i := strings.Index(out.String()[pos:], w)
		if i < 0 {
			t.Fatalf("auth list misses %q after position %d:\n%s", w, pos, out.String())
		}
		pos += i
	}

	// Inside a service, auth add adds to that service, with its own key.
	svc := load("services/orders")
	out.Reset()
	if err := Auth(svc, []string{"add", "dev", "postgres", "postgres://o:secret@orders/db"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "to service orders") || !strings.Contains(out.String(), "GODWIT_ORDERS_DEV_PASSWORD") {
		t.Errorf("add in service: %s", out.String())
	}
	out.Reset()
	Auth(load("services/orders"), []string{"list"}, &out) //nolint:errcheck
	if !strings.Contains(out.String(), "shadows global") || strings.Contains(out.String(), "== global ==") {
		t.Errorf("list in service:\n%s", out.String())
	}

	// A global target cannot be removed through a service.
	if err := Auth(load(""), []string{"remove", "-s", "users", "dev"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "global") {
		t.Errorf("remove global via service: %v", err)
	}
}

func TestMigrateInWorkspace(t *testing.T) {
	root, load := newWorkspace(t)
	ctx := context.Background()
	var out bytes.Buffer

	// migrate new needs a service at the root, and uses the current one inside.
	if err := Migrate(ctx, load(""), []string{"new", "x"}, &out); err == nil || !strings.Contains(err.Error(), "-s") {
		t.Errorf("new at the root: %v", err)
	}
	if err := Migrate(ctx, load(""), []string{"new", "-s", "orders", "create_orders"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, load("services/users"), []string{"new", "create_users"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"orders", "users"} {
		files, _ := os.ReadDir(filepath.Join(root, "services", dir, "migrations"))
		if len(files) != 1 {
			t.Errorf("%s has %d migration files", dir, len(files))
		}
	}

	// Status visits every target of every service; the fake has no database.
	out.Reset()
	Migrate(ctx, load(""), []string{"status"}, &out) //nolint:errcheck
	for _, h := range []string{"== users/prod ==", "== users/dev ==", "== orders/dev =="} {
		if !strings.Contains(out.String(), h) {
			t.Errorf("status misses %s:\n%s", h, out.String())
		}
	}
	out.Reset()
	Migrate(ctx, load(""), []string{"status", "-s", "orders"}, &out) //nolint:errcheck
	if strings.Contains(out.String(), "users/") {
		t.Errorf("-s orders shows users:\n%s", out.String())
	}
	out.Reset()
	Migrate(ctx, load("services/users"), []string{"status", "dev"}, &out) //nolint:errcheck
	if !strings.Contains(out.String(), "== users/dev ==") || strings.Contains(out.String(), "orders") {
		t.Errorf("status inside users:\n%s", out.String())
	}

	// Naming: unknown names and services fail, service/target works.
	for _, args := range [][]string{{"status", "nope"}, {"status", "-s", "nope"}, {"status", "-s", "orders", "prod"}, {"clear-dirty", "1", "dev"}} {
		if err := Migrate(ctx, load(""), args, &bytes.Buffer{}); err == nil || strings.Contains(err.Error(), "no database") {
			t.Errorf("%v: %v", args, err)
		}
	}
	out.Reset()
	Migrate(ctx, load(""), []string{"status", "orders/dev"}, &out) //nolint:errcheck
	if !strings.Contains(out.String(), "== orders/dev ==") || strings.Contains(out.String(), "users") {
		t.Errorf("status orders/dev:\n%s", out.String())
	}
}

func TestDetachInWorkspace(t *testing.T) {
	_, load := newWorkspace(t)
	bg := &fakeRunner{}
	var out bytes.Buffer
	if err := MigrateWith(context.Background(), load(""), bg, []string{"up", "-d", "-s", "users", "dev"}, &out); err != nil {
		t.Fatal(err)
	}
	want := domain.Job{Service: "users", Target: "dev", Op: domain.OpUp}
	if len(bg.started) != 1 || bg.started[0] != want {
		t.Errorf("started %+v, want %+v", bg.started, want)
	}
	if !strings.Contains(out.String(), "runs/users/dev.log") {
		t.Errorf("out = %s", out.String())
	}
}

func TestPlainProjectRejectsServiceFlag(t *testing.T) {
	svc, _ := newService(t)
	if err := Migrate(context.Background(), svc, []string{"status", "-s", "x"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "not a workspace") {
		t.Errorf("err = %v", err)
	}
}
