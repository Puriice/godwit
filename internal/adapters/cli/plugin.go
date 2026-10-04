package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/charmbracelet/lipgloss"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

// Probe starts a plugin to check it and describes its driver.
type Probe func(domain.PluginSpec) (domain.DriverInfo, error)

// Install builds a plugin from a Go package path and returns the command that
// runs it. A global install goes into the user's ~/.godwit/plugins, otherwise
// into the project's .godwit/plugins. The go tool's output is written to stdout
// and stderr.
type Install func(ctx context.Context, global bool, pkg string, stdout, stderr io.Writer) (command string, err error)

// PluginOps are the things "godwit plugin" needs from the outside world.
type PluginOps struct {
	Probe   Probe
	Install Install
}

// PluginUsage documents the plugin subcommands.
const PluginUsage = `usage: godwit plugin list
       godwit plugin add [-g | -G] <command> [name] [-- args...]
       godwit plugin install [-g | -G] <url> [name] [-- args...]
       godwit plugin remove [-g | -G] <name>

  add      register a driver plugin: an executable that speaks godwit's plugin
           protocol (docs/plugins.md). <command> is a path relative to the
           project, or a name found in .godwit/plugins, ~/.godwit/plugins or on PATH.
  install  build a plugin with "go install" into .godwit/plugins and register it.
           <url> is a Go package path such as github.com/you/godwit-driver-x
           (@version optional, default @latest). Needs the Go toolchain.
           Running it again updates the plugin.
  remove   unregister a plugin (targets using its driver are left alone)
  list     show registered plugins, project and global

  -g       use the global location, ~/.godwit, for every project of yours
           (plugins go in ~/.godwit/plugins and config in ~/.godwit/config.json)
  -G       both: register globally and in this project. A project entry replaces
           a global one with the same name.
  name     what the plugin is called in "plugin remove" and "plugin list".
           Defaults to the driver name the plugin reports about itself.
  args     everything after "--" is passed to the plugin when it starts.

Without -g or -G, a plugin belongs to this project only.

The plugin is started once to check it before it is saved. Plugins run with your
privileges, and install builds and then runs code from the network: only use
plugins you trust.
`

// scope says where a plugin is registered.
type scope int

const (
	scopeProject scope = iota // this project's .godwit (the default)
	scopeGlobal               // the user's ~/.godwit (-g)
	scopeBoth                 // both (-G)
)

func (s scope) project() bool { return s != scopeGlobal }
func (s scope) global() bool  { return s != scopeProject }

// where describes the scope for messages.
func (s scope) where() string {
	switch s {
	case scopeGlobal:
		return " globally (~/.godwit)"
	case scopeBoth:
		return " globally (~/.godwit) and in this project"
	}
	return ""
}

// Plugin runs "godwit plugin ...". args excludes the leading "plugin".
func Plugin(ctx context.Context, svc *app.Service, ops PluginOps, args []string, out io.Writer) error {
	if len(args) == 0 {
		return usageErr("")
	}
	pos, extra, sc, err := parsePluginArgs(args[1:])
	if err != nil {
		return err
	}
	if sc.global() && !svc.HasGlobal() {
		return errors.New("-g and -G need a home directory for ~/.godwit, and none was found")
	}
	one := len(pos) == 1 || len(pos) == 2
	switch args[0] {
	case "list":
		if len(pos) == 0 && len(extra) == 0 && sc == scopeProject {
			return pluginList(svc, out)
		}
	case "add":
		if one {
			return pluginRegister(svc, ops, pos[0], optional(pos, 1), extra, sc, false, out)
		}
	case "install":
		if one {
			return pluginInstall(ctx, svc, ops, pos[0], optional(pos, 1), extra, sc, out)
		}
	case "remove":
		if len(pos) == 1 && len(extra) == 0 {
			return pluginRemove(svc, pos[0], sc, out)
		}
	}
	return usageErr("")
}

func usageErr(prefix string) error {
	if prefix == "" {
		prefix = "unknown plugin command"
	}
	return errors.New(prefix + "\n\n" + PluginUsage)
}

// parsePluginArgs splits arguments into positionals, the plugin's own arguments
// (everything after "--") and the -g / -G scope flags.
func parsePluginArgs(args []string) (pos, extra []string, sc scope, err error) {
	if i := slices.Index(args, "--"); i >= 0 {
		args, extra = args[:i], args[i+1:]
	}
	var g, both bool
	for _, a := range args {
		switch {
		case a == "-g":
			g = true
		case a == "-G":
			both = true
		case strings.HasPrefix(a, "-") && a != "-":
			return nil, nil, 0, usageErr(fmt.Sprintf("unknown flag %q", a))
		default:
			pos = append(pos, a)
		}
	}
	switch {
	case g && both:
		return nil, nil, 0, usageErr("-g and -G cannot be used together")
	case g:
		sc = scopeGlobal
	case both:
		sc = scopeBoth
	}
	return pos, extra, sc, nil
}

func optional(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}

// overriddenMark flags a global plugin that a project plugin replaces. The
// meaning is also spelled out in words, so it survives output without color.
const (
	overriddenMark = "✗"
	overriddenNote = "overridden by project"
)

// pluginList prints the project's plugins, then the user's global ones. A global
// plugin that a project plugin of the same name replaces is marked as such.
func pluginList(svc *app.Service, out io.Writer) error {
	local, global := svc.Plugins(), svc.GlobalPlugins()
	if len(local)+len(global) == 0 {
		fmt.Fprintln(out, "No plugins. Add one with: godwit plugin add <command>  or  godwit plugin install <url>")
		return nil
	}

	fmt.Fprintln(out, "Project plugins")
	if err := printPluginRows(out, local, func(domain.PluginSpec) bool { return false }); err != nil {
		return err
	}
	if !svc.HasGlobal() {
		return nil
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Global plugins (~/.godwit)")
	return printPluginRows(out, global, func(p domain.PluginSpec) bool { return hasPlugin(local, p.Name) })
}

// printPluginRows writes one aligned row per plugin. Rows for which overridden
// reports true get the overridden marker and note, and on a terminal are also
// shown faint and struck through. Color is applied after the columns are laid
// out, so escape codes never disturb the alignment.
func printPluginRows(out io.Writer, plugins []domain.PluginSpec, overridden func(domain.PluginSpec) bool) error {
	if len(plugins) == 0 {
		_, err := fmt.Fprintln(out, "  (none)")
		return err
	}
	var buf strings.Builder
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	for _, p := range plugins {
		mark, note := " ", ""
		if overridden(p) {
			mark, note = overriddenMark, "\t"+overriddenNote
		}
		cmd := strings.TrimSpace(p.Command + " " + strings.Join(p.Args, " "))
		fmt.Fprintf(w, "  %s %s\t%s%s\n", mark, p.Name, cmd, note)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	// Strike out the plugin itself, but not the padding between columns or the
	// note, which explains why it is struck out.
	faded := lipgloss.NewRenderer(out).NewStyle().Faint(true).Strikethrough(true).StrikethroughSpaces(false)
	for i, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if before, _, found := strings.Cut(line, overriddenNote); found && overridden(plugins[i]) {
			line = faded.Render(before) + overriddenNote
		}
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	return nil
}

func hasPlugin(list []domain.PluginSpec, name string) bool {
	return slices.ContainsFunc(list, func(p domain.PluginSpec) bool { return strings.EqualFold(p.Name, name) })
}

func pluginRemove(svc *app.Service, name string, sc scope, out io.Writer) error {
	inProject, inGlobal := hasPlugin(svc.Plugins(), name), hasPlugin(svc.GlobalPlugins(), name)
	if sc == scopeBoth && !inProject && !inGlobal {
		return fmt.Errorf("no plugin named %q, in this project or globally", name)
	}
	if sc.project() && (inProject || sc == scopeProject) {
		if err := svc.RemovePlugin(name); err != nil {
			return err
		}
	}
	if sc.global() && (inGlobal || sc == scopeGlobal) {
		if err := svc.RemoveGlobalPlugin(name); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "Removed plugin %q%s.\n", name, sc.where())
	return nil
}

func pluginInstall(ctx context.Context, svc *app.Service, ops PluginOps, url, name string, extra []string, sc scope, out io.Writer) error {
	into := ".godwit/plugins"
	if sc.global() {
		into = "~/.godwit/plugins"
	}
	fmt.Fprintf(out, "Installing %s into %s (this builds and then runs its code)\n", url, into)
	command, err := ops.Install(ctx, sc.global(), url, out, out)
	if err != nil {
		return err
	}
	return pluginRegister(svc, ops, command, name, extra, sc, true, out)
}

// pluginRegister probes a plugin and saves it in the requested places. An empty
// name means the driver name the plugin reports. installed makes registering the
// same plugin again an update rather than a no-op.
func pluginRegister(svc *app.Service, ops PluginOps, command, name string, args []string, sc scope, installed bool, out io.Writer) error {
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

	type dest struct {
		spec     domain.PluginSpec
		existing []domain.PluginSpec
		add      func(domain.PluginSpec) error
		label    string
	}
	var dests []dest
	if sc.global() {
		g := spec
		// A relative path would mean something different in every project.
		if g.Command, err = globalCommand(spec.Command); err != nil {
			return err
		}
		dests = append(dests, dest{g, svc.GlobalPlugins(), svc.AddGlobalPlugin, "globally"})
	}
	if sc.project() {
		dests = append(dests, dest{spec, svc.Plugins(), svc.AddPlugin, "in this project"})
	}

	// Check every destination before changing any, so -G is all or nothing.
	same := func(a, b domain.PluginSpec) bool {
		return strings.EqualFold(a.Name, b.Name) && a.Command == b.Command && slices.Equal(a.Args, b.Args)
	}
	for _, d := range dests {
		for _, p := range d.existing {
			if strings.EqualFold(p.Name, d.spec.Name) && !same(p, d.spec) {
				return fmt.Errorf("a plugin named %q already exists %s with a different command (%s); remove it first",
					p.Name, d.label, p.Command)
			}
		}
	}
	added := false
	for _, d := range dests {
		if slices.ContainsFunc(d.existing, func(p domain.PluginSpec) bool { return same(p, d.spec) }) {
			continue
		}
		if err := d.add(d.spec); err != nil {
			return err
		}
		added = true
	}

	switch {
	case added:
		fmt.Fprintf(out, "Added plugin %q (driver %s)%s. Restart godwit to use it.\n", strings.ToLower(spec.Name), driver, sc.where())
	case installed:
		fmt.Fprintf(out, "Updated plugin %q (driver %s)%s. Restart godwit to use it.\n", strings.ToLower(spec.Name), driver, sc.where())
	default:
		fmt.Fprintf(out, "Plugin %q is already registered%s.\n", strings.ToLower(spec.Name), sc.where())
	}
	return nil
}

// globalCommand makes a command with a path absolute, so it means the same
// thing from every project. Bare names are looked up, so they stay as they are.
func globalCommand(cmd string) (string, error) {
	if !strings.ContainsAny(cmd, `/\`) || filepath.IsAbs(cmd) {
		return cmd, nil
	}
	return filepath.Abs(filepath.FromSlash(cmd))
}
