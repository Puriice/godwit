package plugin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/puriice/godwit/internal/domain"
)

// fakeHome points the user's home directory at a temporary one.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)        // Unix
	t.Setenv("USERPROFILE", home) // Windows
	return home
}

// putPlugin installs a working fake plugin (a copy of the test binary).
func putPlugin(t *testing.T, dir, name string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+filepath.Ext(self))
	if err := os.WriteFile(path, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveSearchOrder(t *testing.T) {
	home := fakeHome(t)
	root := t.TempDir()
	t.Setenv("PATH", t.TempDir()) // nothing useful on PATH

	if _, err := resolve(root, "godwit-driver-order"); err == nil {
		t.Fatal("found a plugin that does not exist")
	}

	// Found in the user's global plugins.
	global := putPlugin(t, pluginsDir(home), "godwit-driver-order")
	if got, err := resolve(root, "godwit-driver-order"); err != nil || got != global {
		t.Errorf("global: got %q, %v; want %q", got, err, global)
	}

	// A project's own plugins directory wins over the global one.
	local := putPlugin(t, pluginsDir(root), "godwit-driver-order")
	if got, err := resolve(root, "godwit-driver-order"); err != nil || got != local {
		t.Errorf("project: got %q, %v; want %q", got, err, local)
	}

	// PATH comes last.
	onPath := putPlugin(t, t.TempDir(), "godwit-driver-pathonly")
	t.Setenv("PATH", filepath.Dir(onPath))
	if got, err := resolve(root, "godwit-driver-pathonly"); err != nil || got != onPath {
		t.Errorf("PATH: got %q, %v; want %q", got, err, onPath)
	}
}

func TestGlobalPluginLoadsFromAnyProject(t *testing.T) {
	home := fakeHome(t)
	t.Setenv("PATH", "")
	t.Setenv(fakeEnv, "ok")
	putPlugin(t, pluginsDir(home), "godwit-driver-fake")

	// Two unrelated projects both find the one globally installed binary.
	for range 2 {
		f := New(t.TempDir(), []domain.PluginSpec{{Name: "fakedb", Command: "godwit-driver-fake"}})
		if w := f.Warnings(); len(w) != 0 {
			t.Fatalf("warnings: %v", w)
		}
		if got := f.Drivers(); len(got) != 1 || got[0] != "fakedb" {
			t.Errorf("drivers = %v", got)
		}
	}
}

func TestInstallGlobalGoesToHome(t *testing.T) {
	home := fakeHome(t)
	withFakeGo(t, "ok")
	project := t.TempDir()

	// A global install passes the home directory as the root.
	command, err := Install(t.Context(), home, "github.com/me/godwit-driver-fake", os.Stdout, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pluginsDir(project)); err == nil {
		t.Error("a global install touched the project")
	}

	t.Setenv("PATH", "")
	t.Setenv(fakeEnv, "ok")
	if _, err := Probe(project, domain.PluginSpec{Name: command, Command: command}); err != nil {
		t.Errorf("the globally installed plugin should be found from any project: %v", err)
	}
}
