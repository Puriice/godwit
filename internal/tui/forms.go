package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"github.com/puriice/godwit/internal/config"
	"github.com/puriice/godwit/internal/driver"
	"github.com/puriice/godwit/internal/migration"
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
func (m *Model) openCredentialForm(t config.Target) tea.Cmd {
	var (
		pw      string
		persist = true
	)
	f := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title(fmt.Sprintf("Password for %s", t.Name)).
			Description(fmt.Sprintf("%s://%s@%s/%s", t.Driver, t.User, t.Host, t.Database)).
			EchoMode(huh.EchoModePassword).
			Value(&pw),
		huh.NewConfirm().Title("Save to .godwit/.env?").Affirmative("Yes").Negative("This session only").Value(&persist),
	))
	return m.openForm("Credentials", f, func(m *Model) tea.Cmd {
		if err := m.proj.SetPassword(t, pw, persist); err != nil {
			m.notice = "saving password: " + err.Error()
		}
		if next := m.nextCredentialForm(); next != nil {
			return next
		}
		return m.refreshAll()
	})
}

// openTargetForm adds a target, or edits existing when non-nil.
func (m *Model) openTargetForm(existing *config.Target) tea.Cmd {
	in := newTargetInput(existing)
	title := "Add target"
	if existing != nil {
		title = "Edit target " + existing.Name
	}
	return m.openForm(title, huh.NewForm(huh.NewGroup(in.fields(m.proj, existing != nil)...)), func(m *Model) tea.Cmd {
		t := in.apply(m.proj, existing)
		if existing == nil {
			m.cursor = len(m.proj.Targets) - 1
		}
		if err := m.proj.Save(); err != nil {
			m.notice = "saving config: " + err.Error()
		}
		if in.pw != "" {
			if err := m.proj.SetPassword(t, in.pw, in.persist); err != nil {
				m.notice = "saving password: " + err.Error()
			}
		}
		m.state(t.Name) // ensure state exists
		return m.refresh(t)
	})
}

// targetInput is the editable state behind the add/edit target form. It is
// shared by the TUI and by `godwit init`.
type targetInput struct {
	t       config.Target
	port    string
	pw      string
	persist bool
}

func newTargetInput(existing *config.Target) *targetInput {
	in := &targetInput{
		t:       config.Target{Driver: driver.Names()[0], Host: "localhost"},
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
func (in *targetInput) fields(p *config.Project, editing bool) []huh.Field {
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
			if _, dup := p.Target(s); dup {
				return errors.New("a target with this name exists")
			}
			return nil
		}))
	}
	return append(fields,
		huh.NewSelect[string]().Title("Driver").Options(huh.NewOptions(driver.Names()...)...).Value(&in.t.Driver),
		huh.NewInput().Title("Host").Value(&in.t.Host).Validate(required),
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
		huh.NewInput().Title("User").Value(&in.t.User).Validate(required),
		huh.NewInput().Title(pwTitle).EchoMode(huh.EchoModePassword).Value(&in.pw),
		huh.NewConfirm().Title("Save password to .godwit/.env?").Affirmative("Yes").Negative("This session only").Value(&in.persist),
	)
}

// apply adds the target to p (or replaces existing) in memory only.
func (in *targetInput) apply(p *config.Project, existing *config.Target) config.Target {
	t := in.t
	t.Name = strings.TrimSpace(t.Name)
	t.Port, _ = strconv.Atoi(in.port)
	if existing == nil {
		p.Targets = append(p.Targets, t)
		return t
	}
	for i := range p.Targets {
		if p.Targets[i].Name == existing.Name {
			p.Targets[i] = t
		}
	}
	return t
}

func required(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("required")
	}
	return nil
}

// openPasswordForm re-prompts for one target's password (key p).
func (m *Model) openPasswordForm(t config.Target) tea.Cmd {
	var (
		pw      string
		persist = true
	)
	f := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Password for "+t.Name).EchoMode(huh.EchoModePassword).Value(&pw).Validate(required),
		huh.NewConfirm().Title("Save to .godwit/.env?").Affirmative("Yes").Negative("This session only").Value(&persist),
	))
	return m.openForm("Credentials", f, func(m *Model) tea.Cmd {
		if err := m.proj.SetPassword(t, pw, persist); err != nil {
			m.notice = "saving password: " + err.Error()
		}
		return m.refresh(t)
	})
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

// openNewMigrationForm scaffolds a migration file.
func (m *Model) openNewMigrationForm() tea.Cmd {
	var name string
	f := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Migration name").Description("letters, digits, _ and -").Value(&name).Validate(required),
	))
	return m.openForm("New migration", f, func(m *Model) tea.Cmd {
		path, err := migration.Create(m.proj.MigrationsPath(), strings.TrimSpace(name))
		if err != nil {
			m.notice = err.Error()
			return nil
		}
		m.notice = "created " + path
		m.reloadMigrations()
		return m.refreshAll()
	})
}
