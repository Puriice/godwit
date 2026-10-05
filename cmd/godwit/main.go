// Command godwit is the composition root: it wires the adapters to the
// application service and hands it to the chosen front end.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

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

Plugins:
  plugin install [-g|-G] <url> [name]      build a driver plugin from a Go package and add it
  plugin add [-g|-G] <command> [name]      add a driver plugin you already have (see docs/plugins.md)
  plugin list                              list plugins (project and global)
  plugin remove [-g|-G] <name>             remove a plugin
                                           -g = global (~/.godwit), -G = global and this project

Migrations:
  migrate status [target...]               show migration states
  migrate up [--detach] [-n N | --to V] [target...]   apply pending migrations
  migrate down [--detach] [-n N | --to V | --batch] [target...] roll back migrations
  migrate redo [--detach] <version> [target...]       roll back and re-apply one migration
                                           --detach = run in the background and return
  migrate clear-dirty <version> <target>   clear a dirty flag after a manual repair
  migrate new <name>                       create a migration file

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
	case "", "init", "auth", "migrate", "plugin", detach.WorkerCommand, detach.LaunchCommand:
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "godwit: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}

	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	store := filestore.New(root)
	project, err := store.Load()
	if err != nil {
		fatal(err)
	}
	// Global plugins live in ~/.godwit; a project's own entries replace them.
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
	plugins := plugin.New(root, specs)
	for _, w := range plugins.Warnings() {
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
	case "migrate":
		var bg app.BackgroundRunner // stays nil if the executable can't be found; --detach then errors
		if runner, rerr := detach.New(root); rerr == nil {
			bg = runner
		}
		err = cli.MigrateWith(context.Background(), svc, bg, args[1:], os.Stdout)
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
