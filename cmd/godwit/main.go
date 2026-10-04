// Command godwit is the composition root: it wires the adapters to the
// application service and hands it to the chosen front end.
package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/puriice/godwit/internal/adapters/cli"
	"github.com/puriice/godwit/internal/adapters/filestore"
	"github.com/puriice/godwit/internal/adapters/fsmigrations"
	"github.com/puriice/godwit/internal/adapters/sqldb"
	"github.com/puriice/godwit/internal/adapters/tui"
	"github.com/puriice/godwit/internal/app"
)

const usage = `usage: godwit [command]

Commands:
  (none)                                   open the migration TUI
  init                                     set the migrations directory and add targets
  auth add <name> <driver> <conn string>   add a target from a connection string
  auth list                                list targets (passwords redacted)
  auth remove <name>                       remove a target and its saved password
  auth disable <name>                      temporarily skip a target
  auth enable <name>                       use a disabled target again
`

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "", "init", "auth":
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
	dbs := sqldb.NewFactory()
	svc, err := app.New(filestore.New(root), fsmigrations.New(root, dbs.Drivers()), dbs)
	if err != nil {
		fatal(err)
	}

	switch cmd {
	case "init":
		err = tui.RunInit(svc)
	case "auth":
		err = cli.Auth(svc, args[1:], os.Stdout)
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
