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
// existing project. Inside a service of a workspace it edits that service; at
// a workspace root, which has no migrations of its own, it refuses.
func RunInit(svc *app.Service) error {
	service := svc.CurrentService()
	if svc.IsWorkspace() && service == "" {
		return errors.New("this is a workspace root, which has no migrations of its own: run godwit init inside a service directory, add a service with godwit service add <dir>, or add global targets with godwit auth add")
	}
	dir := svc.MigrationsDir(service)
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
		if _, ok := svc.Target(domain.TargetRef{Service: service, Target: name}); ok && !svc.IsGlobal(domain.TargetRef{Service: service, Target: name}) {
			return true
		}
		for _, p := range pending {
			if p.t.Name == name {
				return true
			}
		}
		return false
	}

	existing := len(svc.ScopeTargets(service))
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

	if err := svc.SetMigrationsDir(service, strings.TrimSpace(dir)); err != nil {
		return err
	}
	for _, p := range pending {
		if err := svc.AddTarget(service, p.t, p.pw, p.persist); err != nil {
			return err
		}
	}
	if err := svc.EnsureMigrationsDir(service); err != nil {
		return err
	}
	what := "project"
	if service != "" {
		what = "service " + service
	}
	fmt.Printf("Initialized godwit %s\n  migrations: %s\n  targets:    %d\n", what, svc.MigrationsLocation(service), len(svc.ScopeTargets(service)))
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
