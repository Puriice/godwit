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
	m.form = f.WithWidth(formWidth(m.width))
	m.onFormDone = onDone
	return m.form.Init()
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
	t := config.Target{Driver: driver.Names()[0], Host: "localhost"}
	port := ""
	title := "Add target"
	if existing != nil {
		t = *existing
		title = "Edit target " + t.Name
	}
	if t.Port != 0 {
		port = strconv.Itoa(t.Port)
	}
	var (
		pw      string
		persist = true
	)

	nameField := huh.NewInput().Title("Name").Value(&t.Name).Validate(func(s string) error {
		s = strings.TrimSpace(s)
		if s == "" {
			return errors.New("required")
		}
		if _, dup := m.proj.Target(s); dup {
			return errors.New("a target with this name exists")
		}
		return nil
	})
	fields := []huh.Field{
		huh.NewSelect[string]().Title("Driver").Options(huh.NewOptions(driver.Names()...)...).Value(&t.Driver),
		huh.NewInput().Title("Host").Value(&t.Host).Validate(required),
		huh.NewInput().Title("Port").Description("blank = driver default").Value(&port).Validate(func(s string) error {
			if s == "" {
				return nil
			}
			if n, err := strconv.Atoi(s); err != nil || n < 1 || n > 65535 {
				return errors.New("must be 1-65535")
			}
			return nil
		}),
		huh.NewInput().Title("Database").Value(&t.Database).Validate(required),
		huh.NewInput().Title("User").Value(&t.User).Validate(required),
	}
	pwTitle := "Password"
	if existing != nil {
		pwTitle = "Password (blank = keep current)"
	} else {
		fields = append([]huh.Field{nameField}, fields...)
	}
	fields = append(fields,
		huh.NewInput().Title(pwTitle).EchoMode(huh.EchoModePassword).Value(&pw),
		huh.NewConfirm().Title("Save password to .godwit/.env?").Affirmative("Yes").Negative("This session only").Value(&persist),
	)

	return m.openForm(title, huh.NewForm(huh.NewGroup(fields...)), func(m *Model) tea.Cmd {
		t.Name = strings.TrimSpace(t.Name)
		t.Port, _ = strconv.Atoi(port)
		if existing != nil {
			for i := range m.proj.Targets {
				if m.proj.Targets[i].Name == existing.Name {
					m.proj.Targets[i] = t
				}
			}
		} else {
			m.proj.Targets = append(m.proj.Targets, t)
			m.cursor = len(m.proj.Targets) - 1
		}
		if err := m.proj.Save(); err != nil {
			m.notice = "saving config: " + err.Error()
		}
		if pw != "" {
			if err := m.proj.SetPassword(t, pw, persist); err != nil {
				m.notice = "saving password: " + err.Error()
			}
		}
		m.state(t.Name) // ensure state exists
		return m.refresh(t)
	})
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
