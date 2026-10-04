// Command godwit is the composition root: it wires the adapters to the
// application service and hands it to the chosen front end.
package main

import (
	"context"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/puriice/godwit/internal/adapters/cli"
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
  plugin add <name> <command> [args...]    add a driver for another database (see docs/plugins.md)
  plugin list                              list plugins
  plugin remove <name>                     remove a plugin

Migrations:
  migrate status [target...]               show migration states
  migrate up [-n N | --to V] [target...]   apply pending migrations
  migrate down [-n N | --to V] [target...] roll back migrations
  migrate redo <version> [target...]       roll back and re-apply one migration
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
	case "", "init", "auth", "migrate", "plugin":
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
	plugins := plugin.New(root, project.Plugins)
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

	switch cmd {
	case "init":
		err = tui.RunInit(svc)
	case "auth":
		err = cli.Auth(svc, args[1:], os.Stdout)
	case "plugin":
		err = cli.Plugin(svc, func(spec domain.PluginSpec) (domain.DriverInfo, error) {
			return plugin.Probe(root, spec)
		}, args[1:], os.Stdout)
	case "migrate":
		err = cli.Migrate(context.Background(), svc, args[1:], os.Stdout)
	default:
		_, err = tea.NewProgram(tui.New(svc), tea.WithAltScreen()).Run()
	}
	if err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "godwit:", err)
	os.Exit(1)
}
