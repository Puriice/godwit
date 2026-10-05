package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/puriice/godwit/internal/domain"
)

const filesHelp = "←/→ switch panel · ↑/↓ select · ↵ migrate all to selected · e enable/disable by default · n new migration · r refresh · q quit"

// viewFiles lists the migration files on disk, independent of any target.
func (m *Model) viewFiles() string {
	var b strings.Builder
	b.WriteString(m.header(m.tabs()))
	migs, err := m.svc.Migrations()
	switch {
	case err != nil:
		b.WriteString(errStyle.Render(firstLine(err.Error())))
		b.WriteRune('\n')
	case len(migs) == 0:
		b.WriteString(dimStyle.Render("No migration files. Press n to create one."))
		b.WriteRune('\n')
	}

	room := max(m.height-9-lipgloss.Height(m.footer(filesHelp)), 3)
	start := 0
	if m.fcur >= room {
		start = m.fcur - room + 1
	}
	end := min(start+room, len(migs))
	for i := start; i < end; i++ {
		mg := migs[i]
		fmt.Fprintf(&b, "%s%-15d %-24s %s\n", pointer(i == m.fcur), mg.Version, truncate(mg.Name, 24), dimStyle.Render(migSummary(mg)+m.disabledNote(mg.Version)))
	}
	if len(migs) > end {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  … %d more", len(migs)-end)))
		b.WriteRune('\n')
	}
	fmt.Fprintf(&b, "\n%s\n", dimStyle.Render(fmt.Sprintf("%d migration file(s) in %s", len(migs), m.svc.MigrationsLocation())))
	if m.fcur < len(migs) && migs[m.fcur].Source != "" {
		b.WriteString(dimStyle.Render(migs[m.fcur].Source))
		b.WriteRune('\n')
	}
	return b.String() + m.footer(filesHelp)
}

// disabledNote describes a migration's default and any per-target overrides.
func (m *Model) disabledNote(version int64) string {
	def := m.svc.MigrationDisabledByDefault(version)
	differ := 0
	for _, t := range m.targets() {
		if m.svc.MigrationDisabled(t, version) != def {
			differ++
		}
	}
	var s string
	if def {
		s = " · disabled by default"
	}
	switch {
	case differ == 0:
	case def:
		s += fmt.Sprintf(" (enabled on %d target(s))", differ)
	default:
		s += fmt.Sprintf(" · disabled on %d target(s)", differ)
	}
	return s
}

func migSummary(mg *domain.Migration) string {
	s := fmt.Sprintf("%d up stmts · %d down stmts", len(mg.Up), len(mg.Down))
	if mg.NoTransaction {
		s += " · no transaction"
	}
	return s
}

func (m *Model) updateFiles(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	migs, _ := m.svc.Migrations()
	switch key.String() {
	case "q":
		return m, tea.Quit
	case "left", "h":
		m.switchPanel(-1)
	case "right", "l":
		m.switchPanel(1)
	case "up", "k":
		if m.fcur > 0 {
			m.fcur--
		}
	case "down", "j":
		if m.fcur < len(migs)-1 {
			m.fcur++
		}
	case "enter":
		if m.fcur < len(migs) {
			return m, m.goToAll(migs[m.fcur])
		}
	case "e":
		if m.fcur < len(migs) {
			mg := migs[m.fcur]
			enable := m.svc.MigrationDisabledByDefault(mg.Version)
			if err := m.svc.SetMigrationEnabledDefault(mg.Version, enable); err != nil {
				m.notice = err.Error()
				break
			}
			// Targets with their own override keep it; refresh the rest.
			for _, t := range m.targets() {
				st := m.state(t.Name)
				for i := range st.items {
					if st.items[i].Version == mg.Version {
						st.items[i].Disabled, st.items[i].Reason = m.svc.MigrationSetting(t, mg.Version)
					}
				}
			}
			if enable {
				m.notice = fmt.Sprintf("enabled %d_%s by default; per-target overrides are kept", mg.Version, mg.Name)
			} else {
				m.notice = fmt.Sprintf("disabled %d_%s by default; per-target overrides are kept", mg.Version, mg.Name)
			}
		}
	case "r":
		m.reloadMigrations()
		return m, m.refreshAll()
	case "n":
		return m, m.openNewMigrationForm()
	}
	return m, nil
}

// goToAll migrates every enabled target to the selected migration file: targets
// where it is pending apply up to and including it, targets where it is applied
// roll back everything newer. Targets that are already there, still loading,
// busy, or have the migration dirty or missing are skipped and named.
func (m *Model) goToAll(mg *domain.Migration) tea.Cmd {
	label := fmt.Sprintf("%d_%s", mg.Version, mg.Name)
	var ups, downs, skipped []string
	var runs []func(*Model) tea.Cmd
	for _, t := range m.targets() {
		if t.Disabled {
			continue
		}
		st := m.state(t.Name)
		var it *domain.Item
		for i := range st.items {
			if st.items[i].Version == mg.Version {
				it = &st.items[i]
			}
		}
		if st.running || !st.loaded || it == nil || it.State == domain.Dirty || it.State == domain.Missing || (it.State == domain.Pending && it.Disabled) {
			skipped = append(skipped, t.Name)
			continue
		}
		t := t
		if it.State == domain.Pending {
			ups = append(ups, t.Name)
			runs = append(runs, func(m *Model) tea.Cmd { return m.upTo(t, mg.Version) })
			continue
		}
		newer := 0
		for _, x := range st.items {
			if x.Version > mg.Version && x.Record != nil {
				newer++
			}
		}
		if newer > 0 {
			downs = append(downs, t.Name)
			runs = append(runs, func(m *Model) tea.Cmd { return m.downTo(t, mg.Version) })
		}
	}
	if len(runs) == 0 {
		m.notice = "every enabled target is already at " + label
		if len(skipped) > 0 {
			m.notice += " or cannot be moved (" + strings.Join(skipped, ", ") + ")"
		}
		return nil
	}
	q := "Migrate enabled targets to " + label + "?"
	if len(ups) > 0 {
		q += " Apply pending migrations up to it on: " + strings.Join(ups, ", ") + "."
	}
	if len(downs) > 0 {
		q += " Roll back newer migrations on: " + strings.Join(downs, ", ") + " (runs their Down sections, may destroy data)."
	}
	if len(skipped) > 0 {
		q += " Skipped (not loaded, busy, dirty, missing or disabled): " + strings.Join(skipped, ", ") + "."
	}
	return m.openConfirm(q, func(m *Model) tea.Cmd {
		var cmds []tea.Cmd
		for _, run := range runs {
			cmds = append(cmds, run(m))
		}
		return tea.Batch(cmds...)
	})
}
