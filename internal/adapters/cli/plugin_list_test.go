package cli

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/puriice/godwit/internal/domain"
)

func TestPluginListSections(t *testing.T) {
	svc, _ := newService(t)
	withGlobal(t, svc)
	for _, p := range []domain.PluginSpec{{Name: "jsonfile", Command: "./tool", Args: []string{"-v"}}, {Name: "ch", Command: "ch"}} {
		if err := svc.AddPlugin(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []domain.PluginSpec{{Name: "duck", Command: "duck"}, {Name: "JSONFile", Command: "/home/me/jsonfile"}} {
		if err := svc.AddGlobalPlugin(p); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	var installed []string
	if err := Plugin(context.Background(), svc, ops(&installed), []string{"list", "-G"}, &out); err != nil {
		t.Fatal(err)
	}

	// Two sections, project first. Without a terminal there is no color, so the
	// text alone must say which global plugin is overridden.
	want := `Project plugins
    jsonfile  ./tool -v
    ch        ch

Global plugins (~/.godwit)
    duck      duck
  ✗ jsonfile  /home/me/jsonfile  overridden by project
`
	if out.String() != want {
		t.Errorf("list output:\n%s\nwant:\n%s", out.String(), want)
	}
	if strings.Contains(out.String(), "\x1b[") {
		t.Error("escape codes written to a non-terminal")
	}
}

func TestPluginListEmptySections(t *testing.T) {
	svc, _ := newService(t)
	withGlobal(t, svc)
	var out bytes.Buffer
	var installed []string
	run := func() string {
		out.Reset()
		if err := Plugin(context.Background(), svc, ops(&installed), []string{"list", "-G"}, &out); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}

	if got := run(); !strings.HasPrefix(got, "No plugins.") {
		t.Errorf("nothing registered: %q", got)
	}

	// Only global plugins: the project section says so instead of vanishing.
	if err := svc.AddGlobalPlugin(domain.PluginSpec{Name: "duck", Command: "duck"}); err != nil {
		t.Fatal(err)
	}
	want := "Project plugins\n  (none)\n\nGlobal plugins (~/.godwit)\n    duck  duck\n"
	if got := run(); got != want {
		t.Errorf("only global:\n%q\nwant:\n%q", got, want)
	}

	// Only project plugins.
	if err := svc.RemoveGlobalPlugin("duck"); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddPlugin(domain.PluginSpec{Name: "ch", Command: "ch"}); err != nil {
		t.Fatal(err)
	}
	want = "Project plugins\n    ch  ch\n\nGlobal plugins (~/.godwit)\n  (none)\n"
	if got := run(); got != want {
		t.Errorf("only project:\n%q\nwant:\n%q", got, want)
	}
}

func TestPluginListWithoutHomeHasNoGlobalSection(t *testing.T) {
	svc, _ := newService(t) // no home directory
	if err := svc.AddPlugin(domain.PluginSpec{Name: "ch", Command: "ch"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	var installed []string
	if err := Plugin(context.Background(), svc, ops(&installed), []string{"list"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Global") || !strings.Contains(out.String(), "Project plugins") {
		t.Errorf("output = %q", out.String())
	}
}

// With color forced on, only the overridden row is styled, so the strikethrough
// highlights exactly the plugin that has no effect.
func TestPluginListStylesOverriddenRows(t *testing.T) {
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("NO_COLOR", "")
	svc, _ := newService(t)
	withGlobal(t, svc)
	if err := svc.AddPlugin(domain.PluginSpec{Name: "jsonfile", Command: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddGlobalPlugin(domain.PluginSpec{Name: "jsonfile", Command: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddGlobalPlugin(domain.PluginSpec{Name: "duck", Command: "duck"}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	var installed []string
	if err := Plugin(context.Background(), svc, ops(&installed), []string{"list", "-G"}, &out); err != nil {
		t.Fatal(err)
	}
	ansi := regexp.MustCompile("\x1b\\[[0-9;]*m")
	rows := 0
	for _, raw := range strings.Split(out.String(), "\n") {
		plain := ansi.ReplaceAllString(raw, "")
		if !strings.Contains(plain, "jsonfile") && !strings.Contains(plain, "duck") {
			continue
		}
		rows++
		isOverridden := strings.Contains(plain, overriddenNote)
		if styled := strings.Contains(raw, "\x1b["); styled != isOverridden {
			t.Errorf("styled=%v but overridden=%v: %q", styled, isOverridden, raw)
		}
		if isOverridden {
			// The note stays readable: it is not inside any escape sequence.
			if !strings.HasSuffix(raw, overriddenNote) {
				t.Errorf("note is styled: %q", raw)
			}
			// Spaces are not struck through, only the plugin's own text.
			if strings.Contains(raw, "\x1b[9m \x1b[0m") {
				t.Errorf("padding is struck through: %q", raw)
			}
			if !strings.Contains(plain, overriddenMark+" jsonfile") {
				t.Errorf("marker missing: %q", plain)
			}
		}
	}
	if rows != 3 {
		t.Errorf("expected 3 rows, got %d:\n%q", rows, out.String())
	}
}

func TestPluginListScopeFlags(t *testing.T) {
	svc, _ := newService(t)
	withGlobal(t, svc)
	if err := svc.AddPlugin(domain.PluginSpec{Name: "local", Command: "./tool"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddGlobalPlugin(domain.PluginSpec{Name: "duck", Command: "duck"}); err != nil {
		t.Fatal(err)
	}
	list := func(flags ...string) string {
		var out bytes.Buffer
		var installed []string
		if err := Plugin(context.Background(), svc, ops(&installed), append([]string{"list"}, flags...), &out); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if got := list(); got != "Project plugins\n    local  ./tool\n" {
		t.Errorf("no flag:\n%s", got)
	}
	want := "Global plugins (~/.godwit)\n    duck  duck\n"
	if got := list("-g"); got != want {
		t.Errorf("-g:\n%s", got)
	}
	if got := list("-G"); !strings.Contains(got, "Project plugins") || !strings.Contains(got, "Global plugins") {
		t.Errorf("-G:\n%s", got)
	}
}
