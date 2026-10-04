package tui

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

// RunInit is `godwit init`: it asks for the migrations directory and any
// number of targets, then saves them through the service and creates the
// directory. Nothing is written if the user cancels. Re-running it edits the
// existing project.
func RunInit(svc *app.Service) error {
	dir := svc.MigrationsDir()
	if err := runForm(huh.NewGroup(
		huh.NewInput().
			Title("Migrations directory").
			Description("relative to the project root").
			Value(&dir).
			Validate(required),
	)); err != nil {
		return cancelled(err)
	}

	type pendingTarget struct {
		t       domain.Target
		pw      string
		persist bool
	}
	var pending []pendingTarget
	nameTaken := func(name string) bool {
		if _, ok := svc.Target(name); ok {
			return true
		}
		for _, p := range pending {
			if p.t.Name == name {
				return true
			}
		}
		return false
	}

	existing := len(svc.Targets())
	question := "Add a target database now?"
	if existing > 0 {
		question = fmt.Sprintf("Project has %d target(s). Add another?", existing)
	}
	for {
		var add bool
		if err := runForm(huh.NewGroup(
			huh.NewConfirm().Title(question).Affirmative("Yes").Negative("No").Value(&add),
		)); err != nil {
			return cancelled(err)
		}
		if !add {
			break
		}
		in := newTargetInput(svc.Drivers(), nil)
		in.noHost = svc.DriverNoHost
		if err := runForm(huh.NewGroup(in.fields(svc.Drivers(), nameTaken, false)...)); err != nil {
			return cancelled(err)
		}
		pending = append(pending, pendingTarget{in.target(), in.pw, in.persist})
		question = "Add another target?"
	}

	if err := svc.SetMigrationsDir(strings.TrimSpace(dir)); err != nil {
		return err
	}
	for _, p := range pending {
		if err := svc.AddTarget(p.t, p.pw, p.persist); err != nil {
			return err
		}
	}
	if err := svc.EnsureMigrationsDir(); err != nil {
		return err
	}
	fmt.Printf("Initialized godwit project\n  migrations: %s\n  targets:    %d\n", svc.MigrationsLocation(), len(svc.Targets()))
	return nil
}

// runForm shows one form on the alternate screen. Inline forms draw at the
// cursor row, which sits at the bottom of the terminal after a shell prompt;
// the alternate screen starts at the top.
func runForm(g *huh.Group) error {
	return huh.NewForm(g).WithProgramOptions(tea.WithAltScreen()).Run()
}

func cancelled(err error) error {
	if errors.Is(err, huh.ErrUserAborted) {
		return errors.New("init cancelled; nothing was written")
	}
	return err
}
