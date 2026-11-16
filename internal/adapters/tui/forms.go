package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"github.com/puriice/godwit/internal/domain"
)

func formWidth(w int) int {
	if w > 80 {
		return 80
	}
	if w < 30 {
		return 30
	}
	return w - 4
}

// openForm shows a form. onDone runs when it completes; Esc or Ctrl+C cancels.
func (m *Model) openForm(title string, f *huh.Form, onDone func(m *Model) tea.Cmd) tea.Cmd {
	m.prev = m.screen
	if m.prev == scrForm {
		m.prev = scrTargets
	}
	m.screen = scrForm
	m.formTitle = title
	m.form = f
	m.onFormDone = onDone
	m.fitForm()
	return m.form.Init()
}

// formChrome is the number of lines viewForm adds around the form itself:
// title, blank line, blank line, help.
const formChrome = 4

// fitForm sizes the form to the terminal. If the form is taller than the
// space left, huh scrolls inside it and keeps the focused field visible,
// instead of the bottom of an oversized view being what the terminal shows.
func (m *Model) fitForm() {
	if m.form == nil {
		return
	}
	m.form = m.form.WithWidth(formWidth(m.width))
	m.form.Update(tea.WindowSizeMsg{Width: m.width, Height: max(m.height-formChrome, 3)})
}

func (m *Model) closeForm() {
	m.screen = m.prev
	m.form, m.onFormDone = nil, nil
}

func (m *Model) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && (key.String() == "esc" || key.String() == "ctrl+c") {
		m.closeForm()
		// Cancelling one credential prompt moves on to the next, or loads.
		if len(m.credQueue) > 0 {
			return m, m.nextCredentialForm()
		}
		return m, m.refreshAll()
	}
	model, cmd := m.form.Update(msg)
	if f, ok := model.(*huh.Form); ok {
		m.form = f
	}
	switch m.form.State {
	case huh.StateCompleted:
		done := m.onFormDone
		m.closeForm()
		var extra tea.Cmd
		if done != nil {
			extra = done(m)
		}
		return m, tea.Batch(cmd, extra)
	case huh.StateAborted:
		m.closeForm()
	}
	return m, cmd
}

func (m *Model) viewForm() string {
	return titleStyle.Render(m.formTitle) + "\n\n" + m.form.View() + "\n" + helpStyle.Render("esc cancel")
}

// openCredentialForm asks for a target's password.
func (m *Model) openCredentialForm(t row) tea.Cmd {
	var (
		pw      string
		persist = true
	)
	f := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title(fmt.Sprintf("Password for %s", t.label())).
			Description(fmt.Sprintf("%s://%s@%s/%s · or set %s", t.Driver, t.User, t.Host, t.Database, m.svc.PasswordKey(t.ref))).
			EchoMode(huh.EchoModePassword).
			Value(&pw),
		huh.NewConfirm().Title("Save to .godwit/.env?").Affirmative("Yes").Negative("This session only").Value(&persist),
	))
	return m.openForm("Credentials", f, func(m *Model) tea.Cmd {
		if err := m.svc.SetPassword(t.ref, pw, persist); err != nil {
			m.notice = "saving password: " + err.Error()
		}
		if next := m.nextCredentialForm(); next != nil {
			return next
		}
		return m.refreshAll()
	})
}

// globalScope is the scope option for a workspace's global targets.
const globalScope = ""

// openTargetForm adds a target, or edits existing when non-nil. In a
// workspace, a new target is added to a service or as a global target, as the
// form's Scope field says.
func (m *Model) openTargetForm(existing *row) tea.Cmd {
	var base *domain.Target
	if existing != nil {
		base = &existing.Target
	}
	in := newTargetInput(m.svc.Drivers(), base)
	in.noHost = m.svc.DriverNoHost
	title := "Add target"
	scope := m.svc.CurrentService()
	if cur, ok := m.currentTarget(); ok {
		scope = cur.ref.Service
	}
	if existing != nil {
		title = "Edit target " + existing.label()
		if m.svc.IsGlobal(existing.ref) {
			title += " (global: changes apply to every service)"
		}
	}
	// A name is taken when the scope itself defines it; a service may shadow
	// a global target.
	nameTaken := func(name string) bool {
		r := domain.TargetRef{Service: scope, Target: name}
		_, ok := m.svc.Target(r)
		return ok && (scope == globalScope || !m.svc.IsGlobal(r))
	}
	fields := in.fields(m.svc.Drivers(), nameTaken, existing != nil)
	if existing == nil && m.svc.IsWorkspace() {
		opts := []huh.Option[string]{huh.NewOption("global (every service)", globalScope)}
		for _, s := range m.svc.DefaultServices() {
			opts = append(opts, huh.NewOption("service "+s, s))
		}
		fields = append([]huh.Field{huh.NewSelect[string]().Title("Scope").Options(opts...).Value(&scope)}, fields...)
	}
	return m.openForm(title, huh.NewForm(huh.NewGroup(fields...)), func(m *Model) tea.Cmd {
		t := in.target()
		var err error
		var ref domain.TargetRef
		if existing == nil {
			ref = domain.TargetRef{Service: scope, Target: t.Name}
			err = m.svc.AddTarget(scope, t, in.pw, in.persist)
		} else {
			ref = existing.ref
			err = m.svc.UpdateTarget(ref, t, in.pw, in.persist)
		}
		if err != nil {
			m.notice = "saving target: " + err.Error()
			return nil
		}
		return m.refreshDefinition(ref, existing == nil)
	})
}

// refreshDefinition reloads every row that shows the target ref defines: one
// row, or for a global target one per service. With moveCursor the cursor
// goes to the first of them.
func (m *Model) refreshDefinition(ref domain.TargetRef, moveCursor bool) tea.Cmd {
	global := m.svc.IsWorkspace() && (ref.Service == globalScope || m.svc.IsGlobal(ref))
	var cmds []tea.Cmd
	first := true
	for i, r := range m.targets() {
		same := r.ref == ref || (global && r.ref.Target == ref.Target && m.svc.IsGlobal(r.ref))
		if !same {
			continue
		}
		if moveCursor && first {
			m.cursor = i
		}
		first = false
		m.state(r.ref) // ensure state exists
		cmds = append(cmds, m.refresh(r))
	}
	return tea.Batch(cmds...)
}

// targetInput is the editable state behind the add/edit target form. It is
// shared by the TUI and by `godwit init`.
type targetInput struct {
	t       domain.Target
	port    string
	pw      string
	persist bool
	// noHost reports whether a driver has no network endpoint (file based
	// databases), so Host and User are optional. Nil means every driver has one.
	noHost func(driver string) bool
}

func (in *targetInput) hostOptional() bool { return in.noHost != nil && in.noHost(in.t.Driver) }

// requiredUnlessNoHost is required, except for drivers without a host.
func (in *targetInput) requiredUnlessNoHost(s string) error {
	if in.hostOptional() {
		return nil
	}
	return required(s)
}

func newTargetInput(drivers []string, existing *domain.Target) *targetInput {
	in := &targetInput{
		t:       domain.Target{Driver: drivers[0], Host: "localhost"},
		persist: true,
	}
	if existing != nil {
		in.t = *existing
	}
	if in.t.Port != 0 {
		in.port = strconv.Itoa(in.t.Port)
	}
	return in
}

// fields builds the form fields. When editing, the name is fixed (the
// password key derives from it) and an empty password means "keep current".
func (in *targetInput) fields(drivers []string, nameTaken func(string) bool, editing bool) []huh.Field {
	var fields []huh.Field
	pwTitle := "Password"
	if editing {
		pwTitle = "Password (blank = keep current)"
	} else {
		fields = append(fields, huh.NewInput().Title("Name").Value(&in.t.Name).Validate(func(s string) error {
			s = strings.TrimSpace(s)
			if s == "" {
				return errors.New("required")
			}
			if nameTaken(s) {
				return errors.New("a target with this name exists")
			}
			return nil
		}))
	}
	return append(fields,
		huh.NewSelect[string]().Title("Driver").Options(huh.NewOptions(drivers...)...).Value(&in.t.Driver),
		huh.NewInput().Title("Host").Value(&in.t.Host).Validate(in.requiredUnlessNoHost),
		huh.NewInput().Title("Port").Description("blank = driver default").Value(&in.port).Validate(func(s string) error {
			if s == "" {
				return nil
			}
			if n, err := strconv.Atoi(s); err != nil || n < 1 || n > 65535 {
				return errors.New("must be 1-65535")
			}
			return nil
		}),
		huh.NewInput().Title("Database").Value(&in.t.Database).Validate(required),
		huh.NewInput().Title("User").Value(&in.t.User).Validate(in.requiredUnlessNoHost),
		huh.NewInput().Title(pwTitle).EchoMode(huh.EchoModePassword).Value(&in.pw),
		huh.NewConfirm().Title("Save password to .godwit/.env?").Affirmative("Yes").Negative("This session only").Value(&in.persist),
	)
}

// target returns the domain target described by the form values.
func (in *targetInput) target() domain.Target {
	t := in.t
	t.Name = strings.TrimSpace(t.Name)
	t.Port, _ = strconv.Atoi(in.port)
	return t
}

func required(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("required")
	}
	return nil
}

// openConfirm asks a yes/no question and runs onYes when confirmed.
func (m *Model) openConfirm(title string, onYes func(m *Model) tea.Cmd) tea.Cmd {
	var ok bool
	f := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title(title).Affirmative("Yes").Negative("No").Value(&ok),
	))
	return m.openForm("Confirm", f, func(m *Model) tea.Cmd {
		if !ok {
			return nil
		}
		return onYes(m)
	})
}

// openNewMigrationForm scaffolds a migration file, in service by default. In a
// workspace with several services the form asks which one.
func (m *Model) openNewMigrationForm(service string) tea.Cmd {
	var name string
	fields := []huh.Field{
		huh.NewInput().Title("Migration name").Description("letters, digits, _ and -").Value(&name).Validate(required),
	}
	if svcs := m.svc.DefaultServices(); len(svcs) > 1 {
		var opts []huh.Option[string]
		for _, s := range svcs {
			opts = append(opts, huh.NewOption(s, s))
		}
		fields = append([]huh.Field{huh.NewSelect[string]().Title("Service").Options(opts...).Value(&service)}, fields...)
	}
	f := huh.NewForm(huh.NewGroup(fields...))
	return m.openForm("New migration", f, func(m *Model) tea.Cmd {
		path, err := m.svc.CreateMigration(service, name)
		if err != nil {
			m.notice = err.Error()
			return nil
		}
		m.notice = "created " + path
		m.reloadMigrations()
		return m.refreshAll()
	})
}
