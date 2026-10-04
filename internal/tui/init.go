package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/puriice/godwit/internal/config"
)

// RunInit is `godwit init`: it asks for the migrations directory and any
// number of targets, then writes .godwit/ and creates the directory. Nothing
// is written if the user cancels. Re-running it edits the existing project.
func RunInit(p *config.Project) error {
	dir := p.MigrationsDir
	if err := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title("Migrations directory").
			Description("relative to " + p.Root).
			Value(&dir).
			Validate(required),
	)).Run(); err != nil {
		return cancelled(err)
	}

	type pendingSecret struct {
		t       config.Target
		pw      string
		persist bool
	}
	var secrets []pendingSecret

	question := "Add a target database now?"
	if len(p.Targets) > 0 {
		question = fmt.Sprintf("Project has %d target(s). Add another?", len(p.Targets))
	}
	for {
		var add bool
		if err := huh.NewForm(huh.NewGroup(
			huh.NewConfirm().Title(question).Affirmative("Yes").Negative("No").Value(&add),
		)).Run(); err != nil {
			return cancelled(err)
		}
		if !add {
			break
		}
		in := newTargetInput(nil)
		if err := huh.NewForm(huh.NewGroup(in.fields(p, false)...)).Run(); err != nil {
			return cancelled(err)
		}
		t := in.apply(p, nil)
		if in.pw != "" {
			secrets = append(secrets, pendingSecret{t, in.pw, in.persist})
		}
		question = "Add another target?"
	}

	p.MigrationsDir = filepath.Clean(strings.TrimSpace(dir))
	if err := p.Save(); err != nil {
		return err
	}
	for _, s := range secrets {
		if err := p.SetPassword(s.t, s.pw, s.persist); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(p.MigrationsPath(), 0o755); err != nil {
		return err
	}
	fmt.Printf("Initialized %s\n  migrations: %s\n  targets:    %d\n", p.Dir(), p.MigrationsPath(), len(p.Targets))
	return nil
}

func cancelled(err error) error {
	if errors.Is(err, huh.ErrUserAborted) {
		return errors.New("init cancelled; nothing was written")
	}
	return err
}
