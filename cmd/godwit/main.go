// Command godwit is the composition root: it wires the adapters to the
// application service and hands it to the chosen front end.
package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/puriice/godwit/internal/adapters/filestore"
	"github.com/puriice/godwit/internal/adapters/fsmigrations"
	"github.com/puriice/godwit/internal/adapters/sqldb"
	"github.com/puriice/godwit/internal/adapters/tui"
	"github.com/puriice/godwit/internal/app"
)

const usage = `usage: godwit [command]

Commands:
  (none)  open the migration TUI
  init    set the migrations directory and add targets
`

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "help", "-h", "--help":
			fmt.Print(usage)
			return
		case "init":
		default:
			fmt.Fprintf(os.Stderr, "godwit: unknown command %q\n\n%s", os.Args[1], usage)
			os.Exit(2)
		}
	}

	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	svc, err := app.New(filestore.New(root), fsmigrations.New(root), sqldb.NewFactory())
	if err != nil {
		fatal(err)
	}

	if len(os.Args) > 1 { // "init"
		if err := tui.RunInit(svc); err != nil {
			fatal(err)
		}
		return
	}
	if _, err := tea.NewProgram(tui.New(svc), tea.WithAltScreen()).Run(); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "godwit:", err)
	os.Exit(1)
}
