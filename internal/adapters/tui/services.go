package tui

import (
	"fmt"
	"path"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

// ServiceOps are what the services panel needs from the outside world to add
// a service. Without them the panel lists services but cannot add any.
type ServiceOps struct {
	// NewStore returns the configuration store of the service in dir, which is
	// relative to the workspace root with "/" separators.
	NewStore func(dir, name string) app.ProjectStore
	// CleanDir turns a directory typed by the user (relative to the workspace
	// root) into a clean, "/" separated one; ok is false for a directory
	// outside the workspace root.
	CleanDir func(dir string) (string, bool)
}

// WithServiceOps enables adding services in the services panel.
func (m *Model) WithServiceOps(ops ServiceOps) *Model {
	m.serviceOps = ops
	return m
}

const servicesHelp = "←/→ switch panel · ↑/↓ select · a add service · x remove service · r refresh · q quit"

func (m *Model) viewServices() string {
	var b strings.Builder
	b.WriteString(m.header(m.tabs()))
	names := m.svc.Services()
	switch {
	case !m.svc.IsWorkspace():
		b.WriteString(dimStyle.Render("This is a plain project. Press a to add a service: the project becomes a workspace, and its targets become global targets every service can use."))
		b.WriteRune('\n')
	case len(names) == 0:
		b.WriteString(dimStyle.Render("This workspace has no services. Press a to add one."))
		b.WriteRune('\n')
	}
	m.scur = min(m.scur, max(len(names)-1, 0))
	// Two lines per service.
	room := max((m.height-lipgloss.Height(b.String())-4-lipgloss.Height(m.footer(servicesHelp)))/2, 1)
	start, end := window(m.scur, len(names), room)
	for i := start; i < end; i++ {
		n := names[i]
		dir, _ := m.svc.ServiceDir(n)
		own, global := 0, 0
		for _, t := range m.svc.ScopeTargets(n) {
			if m.svc.IsGlobal(domain.TargetRef{Service: n, Target: t.Name}) {
				global++
			} else {
				own++
			}
		}
		label := fmt.Sprintf("%-16s %s", n, dir)
		if n == m.svc.CurrentService() {
			label += " (current)"
		}
		files := "migration files: ?"
		if fs, err := m.svc.MigrationFiles(n); err == nil {
			files = fmt.Sprintf("%d migration file(s)", len(fs))
		}
		fmt.Fprintf(&b, "%s%s\n", pointer(i == m.scur), label)
		fmt.Fprintf(&b, "    %s\n", dimStyle.Render(fmt.Sprintf("%d target(s) + %d global · %s · state table %s", own, global, files, domain.StateTable(n))))
	}
	if len(names) > end {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  … %d more", len(names)-end)))
		b.WriteRune('\n')
	}
	if m.svc.IsWorkspace() {
		fmt.Fprintf(&b, "\n%s\n", dimStyle.Render(fmt.Sprintf("%d global target(s) at the workspace root", len(m.svc.ScopeTargets("")))))
	}
	return b.String() + m.footer(servicesHelp)
}

func (m *Model) updateServices(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	names := m.svc.Services()
	switch key.String() {
	case "q":
		return m, tea.Quit
	case "left", "h":
		m.switchPanel(-1)
	case "right", "l":
		m.switchPanel(1)
	case "up", "k":
		if m.scur > 0 {
			m.scur--
		}
	case "down", "j":
		if m.scur < len(names)-1 {
			m.scur++
		}
	case "a":
		return m, m.openServiceForm()
	case "x":
		if m.scur < len(names) {
			return m, m.confirmRemoveService(names[m.scur])
		}
	case "r":
		m.reloadMigrations()
		return m, m.refreshAll()
	}
	return m, nil
}

// openServiceForm asks for a directory and a name, then makes the directory
// a service of the workspace.
func (m *Model) openServiceForm() tea.Cmd {
	switch {
	case m.serviceOps.NewStore == nil || m.serviceOps.CleanDir == nil:
		m.notice = "adding services is unavailable here; use: godwit service add <dir>"
		return nil
	case m.svc.CurrentService() != "":
		m.notice = "add services at the workspace root: run godwit there, not inside a service directory"
		return nil
	}
	var dir, name string
	validDir := func(s string) error {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("required")
		}
		if _, ok := m.serviceOps.CleanDir(strings.TrimSpace(s)); !ok {
			return fmt.Errorf("must be a directory inside the workspace root")
		}
		return nil
	}
	validName := func(s string) error {
		if s = strings.TrimSpace(s); s == "" {
			return nil // defaults to the directory's last part
		}
		return domain.ValidateServiceName(s)
	}
	f := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Directory").Description("relative to the workspace root, e.g. services/users").Value(&dir).Validate(validDir),
		huh.NewInput().Title("Name").Description("lower case letters, digits and _; empty = the directory's last part").Value(&name).Validate(validName),
	))
	return m.openForm("Add service", f, func(m *Model) tea.Cmd { return m.addService(dir, name) })
}

// addService makes dir a service named name (the directory's last part when
// empty) and reloads what that changes.
func (m *Model) addService(dir, name string) tea.Cmd {
	clean, ok := m.serviceOps.CleanDir(strings.TrimSpace(dir))
	if !ok {
		m.notice = fmt.Sprintf("%q must be a directory inside the workspace root", dir)
		return nil
	}
	n := strings.TrimSpace(name)
	if n == "" {
		n = path.Base(clean)
	}
	wasWorkspace := m.svc.IsWorkspace()
	if err := m.svc.AddService(n, clean, m.serviceOps.NewStore(clean, n)); err != nil {
		m.notice = "adding service: " + err.Error()
		return nil
	}
	if err := m.svc.EnsureMigrationsDir(n); err != nil {
		m.notice = "creating the migrations directory: " + err.Error()
	} else {
		m.notice = fmt.Sprintf("added service %s in %s (state table %s)", n, clean, domain.StateTable(n))
		if !wasWorkspace {
			m.notice += "; this project is now a workspace and its targets are global"
		}
	}
	for i, s := range m.svc.Services() {
		if s == n {
			m.scur = i
		}
	}
	return m.reloadAll()
}

// confirmRemoveService takes a service out of the workspace after a
// confirmation. Its directory, configuration and databases are left alone.
func (m *Model) confirmRemoveService(name string) tea.Cmd {
	for _, t := range m.targets() {
		if t.ref.Service == name && m.state(t.ref).running {
			m.notice = "a run is in progress on " + t.label() + "; wait for it or cancel it first"
			return nil
		}
	}
	dir, _ := m.svc.ServiceDir(name)
	q := fmt.Sprintf("Remove service %s from the workspace? %s, its configuration and its databases are left alone; add it again to use it.", name, dir)
	return m.openConfirm(q, func(m *Model) tea.Cmd { return m.removeService(name) })
}

// removeService takes a service out of the workspace and forgets its rows.
func (m *Model) removeService(name string) tea.Cmd {
	if err := m.svc.RemoveService(name); err != nil {
		m.notice = err.Error()
		return nil
	}
	for key := range m.states {
		if strings.HasPrefix(key, name+"/") {
			delete(m.states, key)
		}
	}
	m.notice = "removed service " + name + "; its files were not touched"
	return m.reloadAll()
}

// reloadAll re-reads everything the services decide: the migration files,
// the target rows and their statuses.
func (m *Model) reloadAll() tea.Cmd {
	m.reloadMigrations()
	m.cursor = min(m.cursor, max(len(m.targets())-1, 0))
	m.fcur = 0
	return m.refreshAll()
}
