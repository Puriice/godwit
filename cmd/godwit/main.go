package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/puriice/godwit/internal/config"
	"github.com/puriice/godwit/internal/tui"
)

func main() {
	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	proj, err := config.Load(root)
	if err != nil {
		fatal(err)
	}
	if _, err := tea.NewProgram(tui.New(proj), tea.WithAltScreen()).Run(); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "godwit:", err)
	os.Exit(1)
}
