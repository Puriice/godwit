package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// pluginsDir is where installed plugins live, relative to the project root.
// resolve looks here before PATH, so a bare command name finds them.
func pluginsDir(root string) string { return filepath.Join(root, ".godwit", "plugins") }

var majorSuffix = regexp.MustCompile(`^v[0-9]+$`)

// installTarget turns what the user typed into an argument for "go install":
// a package path, optionally with a version, defaulting to @latest.
func installTarget(pkg string) (string, error) {
	pkg = strings.TrimSpace(pkg)
	for _, scheme := range []string{"https://", "http://"} {
		pkg = strings.TrimPrefix(pkg, scheme)
	}
	pkg = strings.TrimSuffix(pkg, "/")
	switch {
	case pkg == "":
		return "", errors.New("a package path is required, like github.com/you/godwit-driver-x/cmd/godwit-driver-x")
	case strings.HasPrefix(pkg, "-") || strings.ContainsAny(pkg, " \t\r\n"):
		return "", fmt.Errorf("%q is not a package path", pkg)
	case strings.HasPrefix(pkg, ".") || strings.HasPrefix(pkg, "/") || strings.Contains(pkg, `\`) || filepath.IsAbs(pkg):
		return "", fmt.Errorf("%q is a local path; build it yourself and use: godwit plugin add <command>", pkg)
	}
	if !strings.Contains(pkg, "@") {
		pkg += "@latest"
	}
	return pkg, nil
}

// binaryName is the name "go install" gives the executable for a package: the
// last path element, or the one before it if that is a major version suffix.
func binaryName(target string) string {
	p, _, _ := strings.Cut(target, "@")
	elems := strings.Split(p, "/")
	name := elems[len(elems)-1]
	if majorSuffix.MatchString(name) && len(elems) > 1 {
		name = elems[len(elems)-2]
	}
	return path.Clean(name)
}

// Install builds a plugin with "go install" into <root>/.godwit/plugins and
// returns the command name to register for it. Pass the user's home directory
// as root to install globally into ~/.godwit/plugins. pkg is a Go package path, with an
// optional @version (default @latest). It needs the Go toolchain on PATH.
// Output of the go tool is copied to stdout and stderr.
//
// "go install" compiles code from the network, and godwit then runs it, so only
// install plugins you trust.
func Install(ctx context.Context, root, pkg string, stdout, stderr io.Writer) (command string, err error) {
	target, err := installTarget(pkg)
	if err != nil {
		return "", err
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		return "", errors.New("installing a plugin needs the Go toolchain (https://go.dev/dl) on PATH; " +
			"or build the plugin yourself and use: godwit plugin add <command>")
	}

	dir := pluginsDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// Plugin binaries are per-OS build output: keep them out of version control.
	ignore := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(ignore); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(ignore, []byte("*\n!.gitignore\n"), 0o644); err != nil {
			return "", err
		}
	}

	cmd := exec.CommandContext(ctx, goTool, "install", target)
	cmd.Env = append(os.Environ(), "GOBIN="+dir)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("go install %s: %w", target, err)
	}

	command = binaryName(target)
	// LookPath applies PATHEXT on Windows, so this finds command(.exe) too.
	if _, err := exec.LookPath(filepath.Join(dir, command)); err != nil {
		return "", fmt.Errorf("go install succeeded but %s is not in %s; is %s a main package?", command, dir, target)
	}
	return command, nil
}
