package filestore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/puriice/godwit/internal/domain"
)

// workspaceDir lays out a workspace in a temp dir: the root lists entries as
// its services, and the given service dirs get a config naming them.
func workspaceDir(t *testing.T, entries []string, named map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if err := New(root).Save(domain.Project{Services: entries}); err != nil {
		t.Fatal(err)
	}
	for dir, name := range named {
		if err := NewService(filepath.Join(root, filepath.FromSlash(dir)), name).Save(domain.Project{Service: name}); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestDiscoverWorkspace(t *testing.T) {
	root := workspaceDir(t,
		[]string{"services/users", `services\orders`, "./billing/", "/abs", "../outside"},
		map[string]string{"services/users": "accounts"})
	for _, d := range []string{"services/orders", "billing", "services/users/internal"} {
		os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755)
	}

	l, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if l.Root != root || l.Current != "" || !l.IsWorkspace() {
		t.Fatalf("root layout = %+v", l)
	}
	var got []string
	for _, s := range l.Services {
		got = append(got, s.Dir+"="+s.Name)
	}
	if strings.Join(got, " ") != "services/users=accounts services/orders=orders billing=billing" {
		t.Errorf("services = %v", got)
	}
	if !l.Services[0].Configured || l.Services[1].Configured {
		t.Errorf("configured flags = %+v", l.Services)
	}
	if len(l.Warnings) != 2 {
		t.Errorf("warnings = %v", l.Warnings)
	}

	// Inside a service: the root is found above, and the service is current.
	l, err = Discover(filepath.Join(root, "services", "users"))
	if err != nil || l.Root != root || l.Current != "accounts" {
		t.Fatalf("service layout = %+v, %v", l, err)
	}
	l, err = Discover(filepath.Join(root, "billing"))
	if err != nil || l.Root != root || l.Current != "billing" {
		t.Fatalf("unconfigured service layout = %+v, %v", l, err)
	}

	// A directory inside a service, or anywhere else, is a plain project.
	for _, d := range []string{"services/users/internal", "services"} {
		cwd := filepath.Join(root, filepath.FromSlash(d))
		l, err = Discover(cwd)
		if err != nil || l.Root != cwd || l.IsWorkspace() {
			t.Errorf("%s: layout = %+v, %v", d, l, err)
		}
	}
}

func TestDiscoverPlainProject(t *testing.T) {
	cwd := t.TempDir()
	l, err := Discover(cwd)
	if err != nil || l.Root != cwd || l.IsWorkspace() || l.Current != "" {
		t.Fatalf("no .godwit: %+v, %v", l, err)
	}
	if err := New(cwd).Save(domain.Project{MigrationsDir: "m"}); err != nil {
		t.Fatal(err)
	}
	if l, err = Discover(cwd); err != nil || l.Root != cwd || l.IsWorkspace() {
		t.Fatalf("plain .godwit: %+v, %v", l, err)
	}
}

func TestServiceConfigRoundTrip(t *testing.T) {
	root := t.TempDir()
	if err := New(root).Save(domain.Project{Services: []string{"a", "b/c"}}); err != nil {
		t.Fatal(err)
	}
	p, err := New(root).Load()
	if err != nil || len(p.Services) != 2 || p.Services[1] != "b/c" || !p.IsWorkspace() {
		t.Fatalf("root = %+v, %v", p, err)
	}

	dir := t.TempDir()
	if err := NewService(dir, "users").Save(domain.Project{Service: "users", MigrationsDir: "migrations"}); err != nil {
		t.Fatal(err)
	}
	if p, err := New(dir).Load(); err != nil || p.Service != "users" || p.IsWorkspace() {
		t.Fatalf("service = %+v, %v", p, err)
	}
}

// A plain project's config must not change shape: neither new key appears.
func TestPlainConfigHasNoServiceKeys(t *testing.T) {
	root := t.TempDir()
	if err := New(root).Save(domain.Project{MigrationsDir: "migrations"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, DirName, configFile))
	if strings.Contains(string(raw), "service") {
		t.Errorf("config.json = %s", raw)
	}
}

func TestServicePasswordKeys(t *testing.T) {
	if got := NewService(t.TempDir(), "users").PasswordKey("dev-db"); got != "GODWIT_USERS_DEV_DB_PASSWORD" {
		t.Errorf("service key = %q", got)
	}
	if got := New(t.TempDir()).PasswordKey("dev"); got != "GODWIT_DEV_PASSWORD" {
		t.Errorf("plain key = %q", got)
	}

	dir := t.TempDir()
	s := NewService(dir, "users")
	if err := s.SetPassword("dev", "pw", true); err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(filepath.Join(dir, DirName, envFile))
	if !strings.Contains(string(env), "GODWIT_USERS_DEV_PASSWORD") {
		t.Errorf(".env = %s", env)
	}
	// The process environment overrides .env under the service key.
	t.Setenv("GODWIT_USERS_DEV_PASSWORD", "from-env")
	if pw, ok := NewService(dir, "users").Password("dev"); !ok || pw != "from-env" {
		t.Errorf("password = %q, %v", pw, ok)
	}
}
