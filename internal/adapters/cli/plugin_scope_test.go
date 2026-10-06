package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/puriice/godwit/internal/adapters/filestore"
	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

// withGlobal gives the service a global store in a temporary "home" directory.
func withGlobal(t *testing.T, svc *app.Service) string {
	t.Helper()
	home := t.TempDir()
	if err := svc.SetGlobalStore(filestore.New(home)); err != nil {
		t.Fatal(err)
	}
	return home
}

func names(list []domain.PluginSpec) []string {
	var out []string
	for _, p := range list {
		out = append(out, p.Name)
	}
	return out
}

func TestPluginGlobalScopes(t *testing.T) {
	svc, _ := newService(t)
	home := withGlobal(t, svc)
	var installed []string
	o := ops(&installed)
	var out bytes.Buffer
	do := func(args ...string) error {
		out.Reset()
		return Plugin(context.Background(), svc, o, args, &out)
	}

	// -g: the user's config only, in the home directory's .godwit.
	if err := do("add", "-g", "duckdb"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "globally (~/.godwit)") {
		t.Errorf("output = %q", out.String())
	}
	if !slices.Equal(names(svc.GlobalPlugins()), []string{"duckdb"}) || len(svc.Plugins()) != 0 {
		t.Fatalf("global=%v project=%v", svc.GlobalPlugins(), svc.Plugins())
	}
	saved, err := filestore.New(home).Load()
	if err != nil || len(saved.Plugins) != 1 {
		t.Fatalf("~/.godwit/config.json = %+v, %v", saved, err)
	}

	// -G: both places; flags may sit anywhere before "--".
	if err := do("add", "duck", "-G", "--", "-x"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names(svc.GlobalPlugins()), []string{"duckdb", "duck"}) || !slices.Equal(names(svc.Plugins()), []string{"duck"}) {
		t.Fatalf("global=%v project=%v", names(svc.GlobalPlugins()), names(svc.Plugins()))
	}
	if !strings.Contains(out.String(), "globally (~/.godwit) and in this project") {
		t.Errorf("output = %q", out.String())
	}

	// Registering it again in the same places changes nothing.
	if err := do("add", "-G", "duck", "--", "-x"); err != nil || !strings.Contains(out.String(), "already registered") ||
		len(svc.GlobalPlugins()) != 2 || len(svc.Plugins()) != 1 {
		t.Errorf("re-add: %q %v", out.String(), err)
	}
	// ...and adding to the other scope only fills in what is missing.
	if err := do("add", "-G", "duckdb"); err != nil || !strings.Contains(out.String(), "Added") ||
		len(svc.GlobalPlugins()) != 2 || !slices.Equal(names(svc.Plugins()), []string{"duck", "duckdb"}) {
		t.Errorf("fill in: %q %v global=%v project=%v", out.String(), err, svc.GlobalPlugins(), svc.Plugins())
	}

	// list shows both, marking global entries a project entry replaces.
	if err := do("list", "-G"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Project plugins", "Global plugins (~/.godwit)", "duck", "overridden by project"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("list missing %q:\n%s", want, out.String())
		}
	}

	// remove: project by default, -g global, -G wherever it is.
	if err := do("remove", "duckdb"); err != nil || !slices.Equal(names(svc.GlobalPlugins()), []string{"duckdb", "duck"}) {
		t.Errorf("remove local: %v", err)
	}
	if err := do("remove", "-g", "duckdb"); err != nil || !slices.Equal(names(svc.GlobalPlugins()), []string{"duck"}) {
		t.Errorf("remove global: %v", err)
	}
	if err := do("remove", "-G", "duck"); err != nil || len(svc.GlobalPlugins())+len(svc.Plugins()) != 0 {
		t.Errorf("remove both: %v", err)
	}
	if err := do("remove", "-G", "duck"); err == nil {
		t.Error("removing a plugin that exists nowhere should fail")
	}
	if err := do("remove", "-g", "duck"); err == nil {
		t.Error("removing a missing global plugin should fail")
	}
}

func TestPluginGlobalInstall(t *testing.T) {
	svc, _ := newService(t)
	withGlobal(t, svc)
	var installed []string
	o := ops(&installed)
	var out bytes.Buffer

	if err := Plugin(context.Background(), svc, o, []string{"install", "-g", "github.com/me/godwit-driver-x"}, &out); err != nil {
		t.Fatal(err)
	}
	if installed[0] != "global github.com/me/godwit-driver-x" || !strings.Contains(out.String(), "~/.godwit/plugins") {
		t.Errorf("installed=%v out=%q", installed, out.String())
	}
	if len(svc.Plugins()) != 0 || len(svc.GlobalPlugins()) != 1 || svc.GlobalPlugins()[0].Command != "godwit-driver-x" {
		t.Errorf("global=%v project=%v", svc.GlobalPlugins(), svc.Plugins())
	}

	// -G installs once, globally, and records it in both.
	installed = nil
	if err := Plugin(context.Background(), svc, o, []string{"install", "-G", "github.com/me/other", "mine"}, &out); err != nil {
		t.Fatal(err)
	}
	if len(installed) != 1 || installed[0] != "global github.com/me/other" {
		t.Errorf("installed = %v", installed)
	}
	if !slices.Equal(names(svc.Plugins()), []string{"mine"}) || svc.Plugins()[0].Command != "other" ||
		!slices.Equal(names(svc.GlobalPlugins()), []string{"godwit-driver-x", "mine"}) {
		t.Errorf("global=%v project=%v", svc.GlobalPlugins(), svc.Plugins())
	}

	// Installing again updates.
	if err := Plugin(context.Background(), svc, o, []string{"install", "-G", "github.com/me/other", "mine"}, &out); err != nil ||
		!strings.Contains(out.String(), "Updated plugin") {
		t.Errorf("reinstall: %q %v", out.String(), err)
	}
}

func TestPluginGlobalPathsAreAbsolute(t *testing.T) {
	svc, _ := newService(t)
	withGlobal(t, svc)
	var installed []string
	var out bytes.Buffer
	if err := Plugin(context.Background(), svc, ops(&installed), []string{"add", "-G", "./bin/duck"}, &out); err != nil {
		t.Fatal(err)
	}
	// The global entry cannot depend on the project it was added from.
	if g := svc.GlobalPlugins()[0].Command; !filepath.IsAbs(g) || !strings.HasSuffix(filepath.ToSlash(g), "/bin/duck") {
		t.Errorf("global command = %q", g)
	}
	// The project's own entry stays as typed, so the repository remains portable.
	if p := svc.Plugins()[0].Command; p != "./bin/duck" {
		t.Errorf("project command = %q", p)
	}
}

func TestPluginScopeConflictChangesNothing(t *testing.T) {
	svc, _ := newService(t)
	withGlobal(t, svc)
	var installed []string
	o := ops(&installed)
	var out bytes.Buffer
	do := func(args ...string) error { return Plugin(context.Background(), svc, o, args, &out) }

	// "duckdb" already exists in the project with another command.
	if err := do("add", "./other/duckdb", "duckdb"); err != nil {
		t.Fatal(err)
	}
	// -G would add globally, then clash locally: nothing may be half done.
	err := do("add", "-G", "duckdb")
	if err == nil || !strings.Contains(err.Error(), "already exists in this project") {
		t.Fatalf("err = %v", err)
	}
	if len(svc.GlobalPlugins()) != 0 {
		t.Errorf("-G left a global entry behind: %v", svc.GlobalPlugins())
	}
}

func TestPluginGlobalUnavailableAndBadFlags(t *testing.T) {
	svc, _ := newService(t) // no global store: no home directory
	var installed []string
	o := ops(&installed)
	var out bytes.Buffer
	for _, args := range [][]string{{"add", "-g", "duckdb"}, {"install", "-G", "github.com/me/x"}, {"remove", "-g", "x"}, {"list", "-g"}} {
		if err := Plugin(context.Background(), svc, o, args, &out); err == nil || !strings.Contains(err.Error(), "home directory") {
			t.Errorf("%v: err = %v", args, err)
		}
	}
	withGlobal(t, svc)
	for _, args := range [][]string{{"add", "-g", "-G", "duckdb"}, {"add", "--force", "duckdb"}, {"add", "-x", "duckdb"}, {"list", "-g", "-G"}} {
		if err := Plugin(context.Background(), svc, o, args, &out); err == nil || !strings.Contains(err.Error(), "usage: godwit plugin") {
			t.Errorf("%v: err = %v", args, err)
		}
	}
	if len(installed) != 0 || len(svc.GlobalPlugins())+len(svc.Plugins()) != 0 {
		t.Errorf("rejected commands must change nothing: %v %v %v", installed, svc.GlobalPlugins(), svc.Plugins())
	}
}
