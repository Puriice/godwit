package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

// Probe starts a plugin to check it and describes its driver.
type Probe func(domain.PluginSpec) (domain.DriverInfo, error)

// Install builds a plugin from a Go package path and returns the command that
// runs it. The go tool's output is written to stdout and stderr.
type Install func(ctx context.Context, pkg string, stdout, stderr io.Writer) (command string, err error)

// PluginOps are the things "godwit plugin" needs from the outside world.
type PluginOps struct {
	Probe   Probe
	Install Install
}

// PluginUsage documents the plugin subcommands.
const PluginUsage = `usage: godwit plugin list
       godwit plugin add <command> [name] [-- args...]
       godwit plugin install <url> [name] [-- args...]
       godwit plugin remove <name>

  add      register a driver plugin: an executable that speaks godwit's plugin
           protocol (docs/plugins.md). <command> is a path relative to the
           project, or a name found in .godwit/plugins or on PATH.
  install  build a plugin with "go install" into .godwit/plugins and register it.
           <url> is a Go package path such as github.com/you/godwit-driver-x
           (@version optional, default @latest). Needs the Go toolchain.
           Running it again updates the plugin.
  remove   unregister a plugin (targets using its driver are left alone)
  list     show registered plugins

  name     what the plugin is called in "plugin remove" and "plugin list".
           Defaults to the driver name the plugin reports about itself.
  args     everything after "--" is passed to the plugin when it starts.

The plugin is started once to check it before it is saved. Plugins run with your
privileges, and install builds and then runs code from the network: only use
plugins you trust.
`

// Plugin runs "godwit plugin ...". args excludes the leading "plugin".
func Plugin(ctx context.Context, svc *app.Service, ops PluginOps, args []string, out io.Writer) error {
	if len(args) > 0 {
		rest := args[1:]
		switch args[0] {
		case "list":
			if len(rest) == 0 {
				return pluginList(svc, out)
			}
		case "add":
			if pos, extra := splitArgs(rest); len(pos) == 1 || len(pos) == 2 {
				return pluginRegister(svc, ops, pos[0], optional(pos, 1), extra, false, out)
			}
		case "install":
			if pos, extra := splitArgs(rest); len(pos) == 1 || len(pos) == 2 {
				return pluginInstall(ctx, svc, ops, pos[0], optional(pos, 1), extra, out)
			}
		case "remove":
			if len(rest) == 1 {
				if err := svc.RemovePlugin(rest[0]); err != nil {
					return err
				}
				fmt.Fprintf(out, "Removed plugin %q.\n", rest[0])
				return nil
			}
		}
	}
	return errors.New("unknown plugin command\n\n" + PluginUsage)
}

// splitArgs separates positional arguments from the plugin's own, which follow "--".
func splitArgs(args []string) (pos, extra []string) {
	if i := slices.Index(args, "--"); i >= 0 {
		return args[:i], args[i+1:]
	}
	return args, nil
}

func optional(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}

func pluginList(svc *app.Service, out io.Writer) error {
	plugins := svc.Plugins()
	if len(plugins) == 0 {
		fmt.Fprintln(out, "No plugins. Add one with: godwit plugin add <command>  or  godwit plugin install <url>")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, p := range plugins {
		fmt.Fprintf(w, "%s\t%s\n", p.Name, strings.TrimSpace(p.Command+" "+strings.Join(p.Args, " ")))
	}
	return w.Flush()
}

func pluginInstall(ctx context.Context, svc *app.Service, ops PluginOps, url, name string, extra []string, out io.Writer) error {
	fmt.Fprintf(out, "Installing %s into .godwit/plugins (this builds and then runs its code)\n", url)
	command, err := ops.Install(ctx, url, out, out)
	if err != nil {
		return err
	}
	return pluginRegister(svc, ops, command, name, extra, true, out)
}

// pluginRegister probes a plugin and saves it. An empty name means the driver
// name the plugin reports. installed makes re-registering the same plugin an
// update rather than a no-op.
func pluginRegister(svc *app.Service, ops PluginOps, command, name string, args []string, installed bool, out io.Writer) error {
	spec := domain.PluginSpec{Name: name, Command: command, Args: args}
	if spec.Name == "" {
		spec.Name = command // only used in messages until the plugin names itself
	}
	info, err := ops.Probe(spec)
	if err != nil {
		return err
	}
	driver := strings.ToLower(strings.TrimSpace(info.Name))
	if _, builtin := domain.NewRegistry().Info(driver); builtin {
		return fmt.Errorf("plugin provides driver %q, which is built in and cannot be replaced", driver)
	}
	if name == "" {
		spec.Name = driver
	}

	for _, p := range svc.Plugins() {
		if strings.EqualFold(p.Name, spec.Name) && p.Command == spec.Command && slices.Equal(p.Args, spec.Args) {
			if installed {
				fmt.Fprintf(out, "Updated plugin %q (driver %s). Restart godwit to use it.\n", p.Name, driver)
			} else {
				fmt.Fprintf(out, "Plugin %q is already registered.\n", p.Name)
			}
			return nil
		}
	}
	if err := svc.AddPlugin(spec); err != nil {
		return err
	}
	fmt.Fprintf(out, "Added plugin %q (driver %s). Restart godwit to use it.\n", strings.ToLower(spec.Name), driver)
	return nil
}
