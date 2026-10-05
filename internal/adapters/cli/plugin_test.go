package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/puriice/godwit/internal/domain"
)

// ops returns fake plugin operations. Commands named after a driver ("duckdb")
// report that driver; "broken" fails to start; "pg" claims a built-in driver.
func ops(installed *[]string) PluginOps {
	return PluginOps{
		Probe: func(spec domain.PluginSpec) (domain.DriverInfo, error) {
			switch spec.Command {
			case "broken":
				return domain.DriverInfo{}, errors.New("cannot find broken")
			case "pg":
				return domain.DriverInfo{Name: "postgres"}, nil
			}
			return domain.DriverInfo{Name: strings.ToUpper(spec.Command[:1]) + spec.Command[1:]}, nil
		},
		Install: func(_ context.Context, global bool, pkg string, stdout, _ io.Writer) (string, error) {
			if global {
				*installed = append(*installed, "global "+pkg)
			} else {
				*installed = append(*installed, pkg)
			}
			if strings.Contains(pkg, "nogo") {
				return "", errors.New("needs the Go toolchain")
			}
			stdout.Write([]byte("go: downloading\n"))
			parts := strings.Split(strings.SplitN(pkg, "@", 2)[0], "/")
			return parts[len(parts)-1], nil
		},
	}
}

func TestPluginAdd(t *testing.T) {
	svc, _ := newService(t)
	var installed []string
	o := ops(&installed)
	var out bytes.Buffer
	do := func(args ...string) error {
		out.Reset()
		return Plugin(context.Background(), svc, o, args, &out)
	}

	if err := do("list"); err != nil || !strings.Contains(out.String(), "No plugins") {
		t.Fatalf("empty list: %q %v", out.String(), err)
	}

	// The name defaults to the driver the plugin reports (lower-cased).
	if err := do("add", "duckdb"); err != nil {
		t.Fatal(err)
	}
	if got := svc.Plugins(); len(got) != 1 || got[0].Name != "duckdb" || got[0].Command != "duckdb" || len(got[0].Args) != 0 {
		t.Fatalf("plugins = %+v", got)
	}
	if !strings.Contains(out.String(), `Added plugin "duckdb" (driver duckdb)`) {
		t.Errorf("output = %q", out.String())
	}

	// An explicit name is a label, and arguments follow "--".
	if err := do("add", "./bin/duck", "mine", "--", "--flag", "x"); err != nil {
		t.Fatal(err)
	}
	got := svc.Plugins()
	if len(got) != 2 || got[1].Name != "mine" || got[1].Command != "./bin/duck" || !slices.Equal(got[1].Args, []string{"--flag", "x"}) {
		t.Fatalf("plugins = %+v", got)
	}

	// Adding the identical plugin again is harmless; a clashing name is not.
	if err := do("add", "duckdb"); err != nil || !strings.Contains(out.String(), "already registered") || len(svc.Plugins()) != 2 {
		t.Errorf("re-add: %q %v", out.String(), err)
	}
	if err := do("add", "other", "duckdb"); err == nil {
		t.Error("a second plugin with an existing name was accepted")
	}

	if err := do("list"); err != nil || !strings.Contains(out.String(), "mine") || !strings.Contains(out.String(), "--flag x") {
		t.Errorf("list = %q, %v", out.String(), err)
	}
	if err := do("remove", "duckdb"); err != nil || len(svc.Plugins()) != 1 {
		t.Errorf("remove: %v", err)
	}
}

func TestPluginAddRejectsBadPlugins(t *testing.T) {
	svc, _ := newService(t)
	var installed []string
	var out bytes.Buffer
	for _, args := range [][]string{{"add", "broken"}, {"add", "pg"}, {"add", "pg", "mine"}} {
		if err := Plugin(context.Background(), svc, ops(&installed), args, &out); err == nil {
			t.Errorf("%v should fail", args)
		}
	}
	if len(svc.Plugins()) != 0 {
		t.Fatalf("a rejected plugin was saved: %+v", svc.Plugins())
	}
}

func TestPluginInstall(t *testing.T) {
	svc, _ := newService(t)
	var installed []string
	o := ops(&installed)
	var out bytes.Buffer
	do := func(args ...string) error {
		out.Reset()
		return Plugin(context.Background(), svc, o, args, &out)
	}

	// Name omitted: it comes from the plugin's handshake, not from the URL.
	if err := do("install", "github.com/me/godwit-driver-x@v1.2.0"); err != nil {
		t.Fatal(err)
	}
	// The fake plugin reports its command with a capital; the CLI lower-cases it.
	got := svc.Plugins()
	if len(got) != 1 || got[0].Command != "godwit-driver-x" || got[0].Name != "godwit-driver-x" {
		t.Fatalf("plugins = %+v", got)
	}
	if installed[0] != "github.com/me/godwit-driver-x@v1.2.0" || !strings.Contains(out.String(), "go: downloading") ||
		!strings.Contains(out.String(), "Added plugin") {
		t.Errorf("installed=%v out=%q", installed, out.String())
	}

	// Running install again updates rather than failing on the duplicate.
	if err := do("install", "github.com/me/godwit-driver-x"); err != nil || !strings.Contains(out.String(), "Updated plugin") || len(svc.Plugins()) != 1 {
		t.Errorf("reinstall: %q %v", out.String(), err)
	}

	// Explicit name and plugin arguments.
	if err := do("install", "github.com/me/other", "mine", "--", "-v"); err != nil {
		t.Fatal(err)
	}
	got = svc.Plugins()
	if len(got) != 2 || got[1].Name != "mine" || got[1].Command != "other" || !slices.Equal(got[1].Args, []string{"-v"}) {
		t.Fatalf("plugins = %+v", got)
	}

	// A failed install saves nothing.
	if err := do("install", "github.com/me/nogo"); err == nil || !strings.Contains(err.Error(), "Go toolchain") {
		t.Errorf("failed install: %v", err)
	}
	if len(svc.Plugins()) != 2 {
		t.Errorf("plugins after failed install = %+v", svc.Plugins())
	}
}

func TestPluginUsageErrors(t *testing.T) {
	svc, _ := newService(t)
	var installed []string
	var out bytes.Buffer
	for _, args := range [][]string{
		nil, {"bogus"}, {"add"}, {"add", "a", "b", "c"}, {"install"}, {"install", "a", "b", "c"},
		{"add", "--", "x"}, {"remove"}, {"list", "extra"},
	} {
		err := Plugin(context.Background(), svc, ops(&installed), args, &out)
		if err == nil || !strings.Contains(err.Error(), "usage: godwit plugin") {
			t.Errorf("%v: err = %v", args, err)
		}
	}
	if len(installed) != 0 {
		t.Errorf("usage errors must not install anything: %v", installed)
	}
}
