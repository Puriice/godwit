package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/puriice/godwit/internal/config"
	"github.com/puriice/godwit/internal/tui"
)

const usage = `usage: godwit [command]

Commands:
  (none)  open the migration TUI
  init    set the migrations directory and add targets
`

func main() {
	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	proj, err := config.Load(root)
	if err != nil {
		fatal(err)
	}

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init":
			if err := tui.RunInit(proj); err != nil {
				fatal(err)
			}
			return
		case "help", "-h", "--help":
			fmt.Print(usage)
			return
		default:
			fmt.Fprintf(os.Stderr, "godwit: unknown command %q\n\n%s", os.Args[1], usage)
			os.Exit(2)
		}
	}

	if _, err := tea.NewProgram(tui.New(proj), tea.WithAltScreen()).Run(); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "godwit:", err)
	os.Exit(1)
}
