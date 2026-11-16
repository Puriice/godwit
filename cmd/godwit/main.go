// Command godwit is the composition root: it wires the adapters to the
// application service and hands it to the chosen front end.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/puriice/godwit/internal/adapters/cli"
	"github.com/puriice/godwit/internal/adapters/detach"
	"github.com/puriice/godwit/internal/adapters/filestore"
	"github.com/puriice/godwit/internal/adapters/fsmigrations"
	"github.com/puriice/godwit/internal/adapters/plugin"
	"github.com/puriice/godwit/internal/adapters/sqldb"
	"github.com/puriice/godwit/internal/adapters/tui"
	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

const usage = `usage: godwit [command]

Interface:
  (none)                                   open the migration TUI
  init                                     set the migrations directory and add targets

Targets:
  auth add <name> <driver> <conn string>   add a target from a connection string (not recommended, use TUI instead)
  auth list                                list targets (passwords redacted)
  auth remove <name>                       remove a target and its saved password
  auth disable <name>                      temporarily skip a target
  auth enable <name>                       use a disabled target again

Migrations:
  migrate status [target...]               show migration states
  migrate up [-d | --detach] [-n N | --to V] [target...]   apply pending migrations
  migrate down [-d | --detach] [-n N | --to V | --batch] [target...] roll back migrations
  migrate redo [-d | --detach] <version> [target...]       roll back and re-apply one migration
                                           -d, --detach = run in the background and return
  migrate clear-dirty <version> <target>   clear a dirty flag after a manual repair
  migrate new <name>                       create a migration file

Background runs:
  process list [target...]                 show background runs and their queues
  process show [-n LINES] <target>         details, queue and log tail of a run
  process cancel <target>                  cancel the run in progress
  process dequeue <target> <position>      remove one queued job
  process clear-queue <target>             remove every queued job of a target

Services (monorepos):
  service list                             list the services of this workspace
  service add <dir> [name]                 make <dir> a service and add it to the workspace
  service remove <name>                    take a service out of the workspace (files are kept)
  -s, --service NAME                       (auth, migrate, process, translate) limit a command to
                                           one service; repeatable. Default: every service at the
                                           workspace root, the current one inside a service directory.
                                           A target can also be named service/target, as in users/dev

Plugins:
  plugin install [-g|-G] <url> [name]      build a driver plugin from a Go package and add it
  plugin add [-g|-G] <command> [name]      add a driver plugin you already have (see docs/plugins.md)
  plugin list [-g|-G]                      list plugins (this project; -g = global, -G = both)
  plugin remove [-g|-G] <name>             remove a plugin
                                           -g = global (~/.godwit), -G = global and this project

Translate:
  translate goose [--dry-run] [--table NAME] [target...]
                                           move a project from goose: add godwit directives next to
                                           the goose ones and import the versions goose has applied

Help:
  help, -h, --help                         show this message
`

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "", "init", "auth", "migrate", "process", "plugin", "translate", "service", detach.WorkerCommand, detach.LaunchCommand:
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "godwit: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}

	cwd, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	// The project is the working directory, unless it is a service of a
	// workspace listed further up: then the workspace root holds the global
	// targets, the background runs and the plugin binaries.
	layout, err := filestore.Discover(cwd)
	if err != nil {
		fatal(err)
	}
	root := layout.Root
	// Work from the workspace root, as detached workers do, so that relative
	// paths in any configuration (a SQLite file, a plugin command) mean the
	// same wherever godwit is started.
	if root != cwd {
		if err := os.Chdir(root); err != nil {
			fatal(err)
		}
	}
	for _, w := range layout.Warnings {
		fmt.Fprintln(os.Stderr, "godwit: warning:", w)
	}
	store := filestore.New(root)
	project, err := store.Load()
	if err != nil {
		fatal(err)
	}
	type loaded struct {
		info  filestore.ServiceInfo
		store *filestore.Store
	}
	var services []loaded
	var servicePlugins []domain.ServicePlugins
	for _, si := range layout.Services {
		st := filestore.NewService(layout.Dir(si), si.Name)
		p, err := st.Load()
		if err != nil {
			fatal(fmt.Errorf("service %s: %w", si.Dir, err))
		}
		if !si.Configured {
			fmt.Fprintf(os.Stderr, "godwit: warning: service %s (%s) has no .godwit/config.json yet; run: godwit service add %s\n", si.Name, si.Dir, si.Dir)
		}
		services = append(services, loaded{si, st})
		servicePlugins = append(servicePlugins, domain.ServicePlugins{Service: si.Name, Plugins: p.Plugins})
	}
	// Global plugins live in ~/.godwit; a project's own entries replace them,
	// and in a workspace so do the services' entries.
	var global *filestore.Store
	specs := project.Plugins
	if home, err := os.UserHomeDir(); err == nil {
		global = filestore.New(home)
		g, err := global.Load()
		if err != nil {
			fatal(err)
		}
		specs = domain.MergePlugins(g.Plugins, project.Plugins)
	}
	specs, pluginWarnings := domain.MergeWorkspacePlugins(specs, servicePlugins)
	plugins := plugin.New(root, specs)
	for _, w := range append(pluginWarnings, plugins.Warnings()...) {
		fmt.Fprintln(os.Stderr, "godwit: warning:", w)
	}
	dbs := app.Combine(sqldb.NewFactory(), plugins)
	reg := domain.NewRegistry()
	for _, i := range plugins.DriverInfos() {
		reg.Add(i)
	}
	svc, err := app.New(store, fsmigrations.New(root, dbs.Drivers()).WithNormalizer(reg.Normalize), dbs)
	if err != nil {
		fatal(err)
	}
	if global != nil {
		if err := svc.SetGlobalStore(global); err != nil {
			fatal(err)
		}
	}
	for _, l := range services {
		if err := svc.LoadService(l.info.Name, l.info.Dir, l.store); err != nil {
			fatal(err)
		}
	}
	if err := svc.SetCurrentService(layout.Current); err != nil {
		fatal(err)
	}
	newServiceStore := func(dir, name string) app.ProjectStore {
		return filestore.NewService(filepath.Join(root, filepath.FromSlash(dir)), name)
	}
	// serviceDir turns a directory relative to base into a service directory
	// relative to the workspace root.
	serviceDir := func(base string) func(string) (string, bool) {
		return func(dir string) (string, bool) {
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(base, dir)
			}
			rel, err := filepath.Rel(root, dir)
			if err != nil {
				return "", false
			}
			return filestore.CleanServiceDir(rel)
		}
	}

	switch cmd {
	case "init":
		err = tui.RunInit(svc)
	case "auth":
		err = cli.Auth(svc, args[1:], os.Stdout)
	case "plugin":
		err = cli.Plugin(context.Background(), svc, cli.PluginOps{
			Probe: func(spec domain.PluginSpec) (domain.DriverInfo, error) { return plugin.Probe(root, spec) },
			Install: func(ctx context.Context, global bool, pkg string, stdout, stderr io.Writer) (string, error) {
				dir := root
				if global {
					home, err := os.UserHomeDir()
					if err != nil {
						return "", err
					}
					dir = home
				}
				return plugin.Install(ctx, dir, pkg, stdout, stderr)
			},
		}, args[1:], os.Stdout)
	case "translate":
		err = cli.Translate(context.Background(), svc, args[1:], os.Stdout)
	case "service":
		// On the command line a directory is relative to where godwit was
		// started, as a shell user expects.
		err = cli.Service(svc, cli.ServiceOps{NewStore: newServiceStore, CleanDir: serviceDir(cwd)}, args[1:], os.Stdout)
	case "migrate":
		var bg app.BackgroundRunner // stays nil if the executable can't be found; --detach then errors
		if runner, rerr := detach.New(root); rerr == nil {
			bg = runner
		}
		err = cli.MigrateWith(context.Background(), svc, bg, args[1:], os.Stdout)
	case "process":
		var bg app.BackgroundRunner
		if runner, rerr := detach.New(root); rerr == nil {
			bg = runner
		}
		err = cli.Process(svc, bg, args[1:], os.Stdout)
	case detach.LaunchCommand:
		var runner *detach.Runner
		if runner, err = detach.New(root); err == nil {
			err = runner.Launch(args[1:])
		}
	case detach.WorkerCommand:
		// Started by the TUI as a detached process; see internal/adapters/detach.
		var runner *detach.Runner
		if runner, err = detach.New(root); err == nil {
			err = runner.RunWorker(context.Background(), args[1:], svc.RunJob)
		}
	default:
		model := tui.New(svc).WithPluginOps(tui.PluginOps{
			Probe: func(spec domain.PluginSpec) (domain.DriverInfo, error) { return plugin.Probe(root, spec) },
			Install: func(ctx context.Context, global bool, pkg string, out io.Writer) (string, error) {
				dir := root
				if global {
					home, err := os.UserHomeDir()
					if err != nil {
						return "", err
					}
					dir = home
				}
				return plugin.Install(ctx, dir, pkg, out, out)
			},
		})
		// The TUI asks for a directory relative to the workspace root.
		model.WithServiceOps(tui.ServiceOps{NewStore: newServiceStore, CleanDir: serviceDir(root)})
		// Runs go to detached workers so they survive quitting the TUI.
		if runner, rerr := detach.New(root); rerr == nil {
			model.WithBackground(runner)
		}
		_, err = tea.NewProgram(model, tea.WithAltScreen()).Run()
	}
	if err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "godwit:", err)
	os.Exit(1)
}
