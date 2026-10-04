package plugin

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/puriice/godwit/internal/domain"
)

const fakeGoMode = "GODWIT_FAKE_GO_MODE"

// The test binary also impersonates the go tool: a copy of it named "go" runs
// runFakeGo. That keeps these tests offline and identical on every OS.
func isFakeGo() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	base := strings.TrimSuffix(filepath.Base(exe), filepath.Ext(exe))
	return strings.EqualFold(base, "go")
}

// runFakeGo handles "go install <pkg>@<version>": it "builds" the package by
// copying itself, which is a working fake plugin, to $GOBIN/<binary name>.
func runFakeGo() {
	if len(os.Args) != 3 || os.Args[1] != "install" {
		fmt.Fprintln(os.Stderr, "fake go: unexpected arguments", os.Args[1:])
		os.Exit(2)
	}
	target := os.Args[2]
	switch os.Getenv(fakeGoMode) {
	case "fail":
		fmt.Fprintln(os.Stderr, "go: module not found")
		os.Exit(1)
	case "nobinary":
		return
	}
	if err := os.WriteFile(os.Getenv("GOBIN")+".target", []byte(target), 0o644); err != nil {
		os.Exit(3)
	}
	self, err := os.Executable()
	if err != nil {
		os.Exit(3)
	}
	raw, err := os.ReadFile(self)
	if err != nil {
		os.Exit(3)
	}
	out := filepath.Join(os.Getenv("GOBIN"), binaryName(target)+filepath.Ext(self))
	if err := os.WriteFile(out, raw, 0o755); err != nil {
		os.Exit(3)
	}
}

// withFakeGo puts the fake go tool alone on PATH.
func withFakeGo(t *testing.T, mode string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "go"+filepath.Ext(self)), raw, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv(fakeGoMode, mode)
}

func TestInstallTarget(t *testing.T) {
	for in, want := range map[string]string{
		"github.com/me/godwit-driver-x":                  "github.com/me/godwit-driver-x@latest",
		"  github.com/me/x/cmd/x@v1.2.3 ":                "github.com/me/x/cmd/x@v1.2.3",
		"https://github.com/me/godwit-driver-x/":         "github.com/me/godwit-driver-x@latest",
		"github.com/me/x@main":                           "github.com/me/x@main",
		"example.com/mod/v2/cmd/tool@v2.0.0":             "example.com/mod/v2/cmd/tool@v2.0.0",
		"http://example.com/godwit-driver-y@v0.0.0-2025": "example.com/godwit-driver-y@v0.0.0-2025",
	} {
		if got, err := installTarget(in); err != nil || got != want {
			t.Errorf("installTarget(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "  ", "-toolexec=evil", "--help", "a b", "./local/dir", "../x", "/abs/path", "C:\\x"} {
		if got, err := installTarget(bad); err == nil {
			t.Errorf("installTarget(%q) = %q; want an error", bad, got)
		}
	}
}

func TestBinaryName(t *testing.T) {
	for in, want := range map[string]string{
		"github.com/me/godwit-driver-x@latest":      "godwit-driver-x",
		"github.com/me/x/cmd/tool@v1":               "tool",
		"github.com/me/driver/v2@v2.1.0":            "driver", // major version suffix is not the binary name
		"github.com/me/driver/v2/cmd/plugin@latest": "plugin",
		"github.com/me/v@latest":                    "v",
	} {
		if got := binaryName(in); got != want {
			t.Errorf("binaryName(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestInstallEndToEnd(t *testing.T) {
	withFakeGo(t, "ok")
	goPath := os.Getenv("PATH")
	root := t.TempDir()
	var out, errOut bytes.Buffer

	command, err := Install(context.Background(), root, "github.com/me/godwit-driver-fake@v1.0.0", &out, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if command != "godwit-driver-fake" {
		t.Errorf("command = %q", command)
	}
	if raw, _ := os.ReadFile(filepath.Join(root, ".godwit", "plugins") + ".target"); string(raw) != "github.com/me/godwit-driver-fake@v1.0.0" {
		t.Errorf("go install got target %q", raw)
	}
	// Binaries stay out of version control.
	if gi, _ := os.ReadFile(filepath.Join(root, ".godwit", "plugins", ".gitignore")); !strings.Contains(string(gi), "*") {
		t.Errorf(".gitignore = %q", gi)
	}

	// The installed command resolves from .godwit/plugins by its bare name and
	// is a working plugin: the fake go "built" a copy of the fake plugin.
	t.Setenv("PATH", "")
	t.Setenv(fakeEnv, "ok")
	info, err := Probe(root, domain.PluginSpec{Name: command, Command: command})
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "fakedb" {
		t.Errorf("handshake driver = %q", info.Name)
	}

	// Installing again replaces the binary without complaint (an update).
	t.Setenv("PATH", goPath)
	if _, err := Install(context.Background(), root, "github.com/me/godwit-driver-fake", io.Discard, io.Discard); err != nil {
		t.Errorf("reinstall: %v", err)
	}
}

func TestInstallErrors(t *testing.T) {
	root := t.TempDir()

	t.Run("no go toolchain", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		_, err := Install(context.Background(), root, "github.com/me/x", io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "Go toolchain") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("go install fails", func(t *testing.T) {
		withFakeGo(t, "fail")
		var errOut bytes.Buffer
		_, err := Install(context.Background(), root, "github.com/me/x", io.Discard, &errOut)
		if err == nil || !strings.Contains(err.Error(), "go install github.com/me/x@latest") || !strings.Contains(errOut.String(), "module not found") {
			t.Errorf("err = %v, stderr = %q", err, errOut.String())
		}
	})
	t.Run("not a main package", func(t *testing.T) {
		withFakeGo(t, "nobinary")
		_, err := Install(context.Background(), root, "github.com/me/lib", io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "is github.com/me/lib@latest a main package") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("rejects bad package before running anything", func(t *testing.T) {
		withFakeGo(t, "fail")
		_, err := Install(context.Background(), root, "-toolexec=evil", io.Discard, io.Discard)
		if err == nil || strings.Contains(err.Error(), "go install") {
			t.Errorf("err = %v", err)
		}
	})
}
