package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/puriice/godwit/internal/domain"
)

const filesHelp = "←/→ switch panel · ↑/↓ select · ↵ migrate all to selected · e enable/disable by default · n new migration · r refresh · q quit"

// fileRow is one line group of the migrations panel: a migration file of a
// service. In a workspace a service without files (or whose directory cannot
// be read) gets a row of its own with no file, so it still shows and can be
// selected.
type fileRow struct {
	service string
	file    *domain.MigrationFile // nil for an empty or unreadable service
	err     error                 // the service's files could not be listed
}

// fileRows lists the migration files of every service shown, service by
// service.
func (m *Model) fileRows() []fileRow {
	var rows []fileRow
	ws := m.svc.IsWorkspace()
	for _, service := range m.svc.DefaultServices() {
		files, err := m.svc.MigrationFiles(service)
		if err != nil || (ws && len(files) == 0) {
			rows = append(rows, fileRow{service: service, err: err})
			continue
		}
		for i := range files {
			rows = append(rows, fileRow{service: service, file: &files[i]})
		}
	}
	return rows
}

// heading reports whether row i starts a service's section (workspace only).
func (m *Model) heading(rows []fileRow, i int) bool {
	return m.svc.IsWorkspace() && (i == 0 || rows[i-1].service != rows[i].service)
}

// viewFiles lists the migration files on disk, independent of any target.
func (m *Model) viewFiles() string {
	var b strings.Builder
	b.WriteString(m.header(m.tabs()))
	rows := m.fileRows()
	ws := m.svc.IsWorkspace()
	if !ws {
		switch {
		case len(rows) == 1 && rows[0].err != nil:
			b.WriteString(errStyle.Render(firstLine(rows[0].err.Error())))
			b.WriteRune('\n')
			rows = nil
		case len(rows) == 0:
			b.WriteString(dimStyle.Render("No migration files. Press n to create one."))
			b.WriteRune('\n')
		}
	} else if len(rows) == 0 {
		b.WriteString(dimStyle.Render("This workspace has no services yet. Add one with: godwit service add <dir>"))
		b.WriteRune('\n')
	}
	m.fcur = min(m.fcur, max(len(rows)-1, 0))

	lines := func(i int) int {
		if i < 0 || i >= len(rows) {
			return 0
		}
		n := rowLines(rows[i])
		if m.heading(rows, i) {
			n++
		}
		return n
	}
	room := max(m.height-9-lipgloss.Height(m.footer(filesHelp)), 3)
	// A file with an error takes a line per error line, so budget by lines:
	// scroll just far enough that the cursor row is the last one that fits.
	start := m.fcur
	for used := lines(start); start > 0 && used+lines(start-1) <= room; {
		start--
		used += lines(start)
	}
	used, end := 0, start
	for i := start; i < len(rows); i++ {
		n := lines(i)
		if used > 0 && used+n > room {
			break
		}
		used += n
		end = i + 1
		r := rows[i]
		if m.heading(rows, i) || (ws && i == start) {
			fmt.Fprintf(&b, "%s\n", titleStyle.Render(r.service)+" "+dimStyle.Render(m.svc.MigrationsLocation(r.service)))
		}
		switch {
		case r.err != nil:
			fmt.Fprintf(&b, "%s%s\n", pointer(i == m.fcur), errStyle.Render(firstLine(r.err.Error())))
		case r.file == nil:
			fmt.Fprintf(&b, "%s%s\n", pointer(i == m.fcur), dimStyle.Render("no migration files; press n to create one"))
		case r.file.Err != nil:
			f := r.file
			fmt.Fprintf(&b, "%s%s\n", pointer(i == m.fcur), errStyle.Render(f.Name+"  ERROR"))
			for _, line := range strings.Split(f.Err.Error(), "\n") {
				b.WriteString(errStyle.Render("    " + line))
				b.WriteRune('\n')
			}
		default:
			mg := r.file.Migration
			fmt.Fprintf(&b, "%s%-15d %-24s %s\n", pointer(i == m.fcur), mg.Version, truncate(mg.Name, 24), dimStyle.Render(migSummary(mg)+m.disabledNote(r.service, mg.Version)))
		}
	}
	if len(rows) > end {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  … %d more", len(rows)-end)))
		b.WriteRune('\n')
	}
	files, broken := 0, 0
	for _, r := range rows {
		if r.file != nil {
			files++
			if r.file.Err != nil {
				broken++
			}
		}
	}
	count := fmt.Sprintf("%d migration file(s)", files)
	if broken > 0 {
		count += fmt.Sprintf(" (%d with errors)", broken)
	}
	if ws {
		count += fmt.Sprintf(" in %d service(s)", len(m.svc.DefaultServices()))
	} else {
		count += " in " + m.svc.MigrationsLocation("")
	}
	fmt.Fprintf(&b, "\n%s\n", dimStyle.Render(count))
	if m.fcur < len(rows) && rows[m.fcur].file != nil && rows[m.fcur].file.Migration != nil && rows[m.fcur].file.Migration.Source != "" {
		b.WriteString(dimStyle.Render(rows[m.fcur].file.Migration.Source))
		b.WriteRune('\n')
	}
	return b.String() + m.footer(filesHelp)
}

// rowLines is how many lines a row takes in the list, without its heading.
func rowLines(r fileRow) int {
	if r.file != nil && r.file.Err != nil {
		return 1 + strings.Count(r.file.Err.Error(), "\n") + 1
	}
	return 1
}

// disabledNote describes a migration's default and any per-target overrides,
// among the targets of its service.
func (m *Model) disabledNote(service string, version int64) string {
	def := m.svc.MigrationDisabledByDefault(service, version)
	differ := 0
	for _, t := range m.targets() {
		if t.ref.Service == service && m.svc.MigrationDisabled(t.ref, version) != def {
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
	rows := m.fileRows()
	var sel *fileRow
	if m.fcur < len(rows) {
		sel = &rows[m.fcur]
	}
	// selected returns the selected migration, or explains why there is none
	// to act on.
	selected := func() *domain.Migration {
		switch {
		case sel == nil:
			return nil
		case sel.err != nil:
			m.notice = firstLine(sel.err.Error())
		case sel.file == nil:
			m.notice = "no migration files in " + sel.service + "; press n to create one"
		case sel.file.Err != nil:
			m.notice = "fix " + sel.file.Name + " first"
		default:
			return sel.file.Migration
		}
		return nil
	}
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
		if m.fcur < len(rows)-1 {
			m.fcur++
		}
	case "enter":
		if mg := selected(); mg != nil {
			return m, m.goToAll(sel.service, mg)
		}
	case "e":
		mg := selected()
		if mg == nil {
			break
		}
		service := sel.service
		enable := m.svc.MigrationDisabledByDefault(service, mg.Version)
		if err := m.svc.SetMigrationEnabledDefault(service, mg.Version, enable); err != nil {
			m.notice = err.Error()
			break
		}
		// Targets with their own override keep it; refresh the rest.
		for _, t := range m.targets() {
			if t.ref.Service != service {
				continue
			}
			st := m.state(t.ref)
			for i := range st.items {
				if st.items[i].Version == mg.Version {
					st.items[i].Disabled, st.items[i].Reason = m.svc.MigrationSetting(t.ref, mg.Version)
				}
			}
		}
		if enable {
			m.notice = fmt.Sprintf("enabled %d_%s by default; per-target overrides are kept", mg.Version, mg.Name)
		} else {
			m.notice = fmt.Sprintf("disabled %d_%s by default; per-target overrides are kept", mg.Version, mg.Name)
		}
	case "r":
		m.reloadMigrations()
		return m, m.refreshAll()
	case "n":
		service := ""
		if sel != nil {
			service = sel.service
		} else if svcs := m.svc.DefaultServices(); len(svcs) > 0 {
			service = svcs[0]
		}
		return m, m.openNewMigrationForm(service)
	}
	return m, nil
}

// goToAll migrates every enabled target of service to the selected migration
// file: targets where it is pending apply up to and including it, targets
// where it is applied roll back everything newer. Targets that are already
// there, still loading, busy, or have the migration dirty or missing are
// skipped and named.
func (m *Model) goToAll(service string, mg *domain.Migration) tea.Cmd {
	label := fmt.Sprintf("%d_%s", mg.Version, mg.Name)
	var ups, downs, skipped []string
	var runs []func(*Model) tea.Cmd
	for _, t := range m.targets() {
		if t.Disabled || t.ref.Service != service {
			continue
		}
		st := m.state(t.ref)
		var it *domain.Item
		for i := range st.items {
			if st.items[i].Version == mg.Version {
				it = &st.items[i]
			}
		}
		if st.running || !st.loaded || it == nil || it.State == domain.Dirty || it.State == domain.Missing || (it.State == domain.Pending && it.Disabled) {
			skipped = append(skipped, t.label())
			continue
		}
		t := t
		if it.State == domain.Pending {
			ups = append(ups, t.label())
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
			downs = append(downs, t.label())
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
