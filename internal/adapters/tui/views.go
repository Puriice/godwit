package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/puriice/godwit/internal/domain"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	helpStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	cursorStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
)

func pointer(selected bool) string {
	if selected {
		return cursorStyle.Render("› ")
	}
	return "  "
}

func (m *Model) header(title string) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("godwit"))
	b.WriteString(dimStyle.Render(" · " + title))
	b.WriteRune('\n')
	if m.migErr != nil {
		b.WriteString(errStyle.Render("migrations: " + m.migErr.Error()))
		b.WriteRune('\n')
	}
	b.WriteRune('\n')
	return b.String()
}

const (
	helpSep        = " · "
	targetsHelp    = "←/→ switch panel · ↑/↓ select · ↵ open · a add · e edit · x delete · t enable/disable · p password · u apply all targets · r refresh · q quit"
	migrationsHelp = "↑/↓ select · u apply all · s apply next · d roll back last · ↵ migrate to selected · R redo selected · c clear dirty · t enable/disable · r refresh · n new · esc back"
)

// wrapHelp lays out a " · "-separated shortcut list in lines no wider than
// width, breaking only between shortcuts so none is split in half. A single
// shortcut wider than width gets a line of its own.
func wrapHelp(help string, width int) []string {
	var lines []string
	line := ""
	for item := range strings.SplitSeq(help, helpSep) {
		switch {
		case line == "":
			line = item
		case lipgloss.Width(line)+lipgloss.Width(helpSep)+lipgloss.Width(item) <= width:
			line += helpSep + item
		default:
			lines = append(lines, line)
			line = item
		}
	}
	return append(lines, line)
}

func (m *Model) footer(help string) string {
	var b strings.Builder
	if m.notice != "" {
		b.WriteRune('\n')
		b.WriteString(warnStyle.Width(max(m.width, 1)).Render(m.notice))
		b.WriteRune('\n')
	}
	b.WriteRune('\n')
	b.WriteString(helpStyle.Render(strings.Join(wrapHelp(help, m.width), "\n")))
	return b.String()
}

func summarize(items []domain.Item) string {
	counts := map[domain.State]int{}
	for _, it := range items {
		counts[it.State]++
	}
	parts := []string{fmt.Sprintf("%d applied", counts[domain.Applied]+counts[domain.Modified])}
	if n := counts[domain.Pending]; n > 0 {
		parts = append(parts, warnStyle.Render(fmt.Sprintf("%d pending", n)))
	} else {
		parts = append(parts, "up to date")
	}
	if n := counts[domain.Dirty]; n > 0 {
		parts = append(parts, errStyle.Render(fmt.Sprintf("%d dirty", n)))
	}
	if n := counts[domain.Modified]; n > 0 {
		parts = append(parts, warnStyle.Render(fmt.Sprintf("%d modified", n)))
	}
	if n := counts[domain.Missing]; n > 0 {
		parts = append(parts, warnStyle.Render(fmt.Sprintf("%d missing", n)))
	}
	return strings.Join(parts, " · ")
}

func targetLine(t domain.Target) string {
	port := ""
	if t.Port != 0 {
		port = fmt.Sprintf(":%d", t.Port)
	}
	return fmt.Sprintf("%s@%s%s/%s", t.User, t.Host, port, t.Database)
}

func (m *Model) viewTargets() string {
	var b strings.Builder
	b.WriteString(m.header(m.tabs()))
	ts := m.targets()
	if len(ts) == 0 {
		b.WriteString(dimStyle.Render("No targets yet. Press a to add one."))
		b.WriteRune('\n')
	}
	for i, t := range ts {
		st := m.state(t.Name)
		var status string
		switch {
		case t.Disabled:
			status = dimStyle.Render("disabled · press t to enable")
		case st.running:
			status = warnStyle.Render("running…")
		case st.loading:
			status = dimStyle.Render("loading…")
		case st.runErr != nil:
			status = errStyle.Render("last run failed: " + firstLine(st.runErr.Error()))
		case st.err != nil:
			status = errStyle.Render(firstLine(st.err.Error()))
		case st.loaded:
			status = summarize(st.items)
		}
		label := fmt.Sprintf("%-16s %-9s", t.Name, t.Driver)
		if t.Disabled {
			label = dimStyle.Render(label)
		}
		fmt.Fprintf(&b, "%s%s %s\n", pointer(i == m.cursor), label, dimStyle.Render(targetLine(t)))
		fmt.Fprintf(&b, "    %s\n", status)
	}
	fmt.Fprintf(&b, "\n%s\n", dimStyle.Render(fmt.Sprintf("%d migration file(s) in %s", m.migCount, m.svc.MigrationsLocation())))
	return b.String() + m.footer(targetsHelp)
}

func (m *Model) updateTargets(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.targets())
	switch key.String() {
	case "q":
		return m, tea.Quit
	case "left", "h":
		m.switchPanel(-1)
	case "right", "l":
		m.switchPanel(1)
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < n-1 {
			m.cursor++
		}
	case "enter":
		if _, ok := m.currentTarget(); ok {
			m.screen, m.mcur = scrMigrations, 0
		}
	case "a":
		return m, m.openTargetForm(nil)
	case "e":
		if t, ok := m.currentTarget(); ok {
			return m, m.openTargetForm(&t)
		}
	case "t":
		if t, ok := m.currentTarget(); ok {
			return m, m.toggleTarget(t)
		}
	case "p":
		if t, ok := m.currentTarget(); ok {
			return m, m.openPasswordForm(t)
		}
	case "x":
		if t, ok := m.currentTarget(); ok {
			return m, m.openConfirm(fmt.Sprintf("Remove target %q and its saved password? (the database is untouched)", t.Name), func(m *Model) tea.Cmd {
				if err := m.svc.RemoveTarget(t.Name); err != nil {
					m.notice = err.Error()
				}
				delete(m.states, t.Name)
				if m.cursor >= len(m.targets()) && m.cursor > 0 {
					m.cursor--
				}
				return nil
			})
		}
	case "r":
		m.reloadMigrations()
		return m, m.refreshAll()
	case "u":
		enabled := 0
		for _, t := range m.targets() {
			if !t.Disabled {
				enabled++
			}
		}
		if enabled == 0 {
			m.notice = "no enabled targets"
			break
		}
		return m, m.openConfirm(fmt.Sprintf("Apply all pending migrations to %d enabled target(s)? (disabled targets are skipped)", enabled), func(m *Model) tea.Cmd {
			var cmds []tea.Cmd
			for _, t := range m.targets() {
				if !t.Disabled {
					cmds = append(cmds, m.run(t, domain.Up, 0))
				}
			}
			return tea.Batch(cmds...)
		})
	}
	return m, nil
}

func (m *Model) viewMigrations() string {
	t, ok := m.currentTarget()
	if !ok {
		return m.viewTargets()
	}
	st := m.state(t.Name)
	var b strings.Builder
	b.WriteString(m.header(fmt.Sprintf("%s (%s %s)", t.Name, t.Driver, targetLine(t))))

	switch {
	case t.Disabled:
		b.WriteString(dimStyle.Render("This target is disabled. Press t to enable it."))
		b.WriteRune('\n')
	case st.err != nil && !st.loaded:
		b.WriteString(errStyle.Render(st.err.Error()))
		b.WriteRune('\n')
	case st.loading && !st.loaded:
		b.WriteString(dimStyle.Render("loading…"))
		b.WriteRune('\n')
	case len(st.items) == 0:
		b.WriteString(dimStyle.Render("No migrations. Press n to create one."))
		b.WriteRune('\n')
	}

	// Window the list so the log and the (possibly wrapped) footer still fit.
	foot := m.footer(migrationsHelp)
	logLines := min(len(st.log), 6)
	room := max(m.height-10-logLines-lipgloss.Height(foot), 3)
	start := 0
	if m.mcur >= room {
		start = m.mcur - room + 1
	}
	end := min(start+room, len(st.items))
	for i := start; i < end; i++ {
		it := st.items[i]
		fmt.Fprintf(&b, "%s%-15d %-24s %s\n", pointer(i == m.mcur), it.Version, truncate(it.Name, 24), badge(it.State))
	}
	if len(st.items) > end {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  … %d more", len(st.items)-end)))
		b.WriteRune('\n')
	}

	if st.runErr != nil {
		b.WriteRune('\n')
		b.WriteString(errStyle.Render(st.runErr.Error()))
		b.WriteRune('\n')
	}
	if st.err != nil && st.loaded {
		b.WriteRune('\n')
		b.WriteString(errStyle.Render(st.err.Error()))
		b.WriteRune('\n')
	}
	if st.running {
		b.WriteRune('\n')
		b.WriteString(warnStyle.Render("running…"))
		b.WriteRune('\n')
	}
	if logLines > 0 {
		b.WriteRune('\n')
		b.WriteString(dimStyle.Render(strings.Join(st.log[len(st.log)-logLines:], "\n")))
		b.WriteRune('\n')
	}
	return b.String() + foot
}

func (m *Model) updateMigrations(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	t, ok := m.currentTarget()
	if !ok {
		m.screen = scrTargets
		return m, nil
	}
	st := m.state(t.Name)
	if t.Disabled {
		switch key.String() {
		case "u", "s", "d", "enter", "R", "c", "r":
			m.notice = t.Name + " is disabled; press t to enable it"
			return m, nil
		}
	}
	switch key.String() {
	case "esc", "b", "q":
		m.screen = scrTargets
	case "t":
		return m, m.toggleTarget(t)
	case "up", "k":
		if m.mcur > 0 {
			m.mcur--
		}
	case "down", "j":
		if m.mcur < len(st.items)-1 {
			m.mcur++
		}
	case "u":
		return m, m.run(t, domain.Up, 0)
	case "s":
		return m, m.run(t, domain.Up, 1)
	case "d":
		return m, m.openConfirm(fmt.Sprintf("Roll back the last applied migration on %s?", t.Name), func(m *Model) tea.Cmd {
			return m.run(t, domain.Down, 1)
		})
	case "enter":
		return m, m.goTo(t, st)
	case "R":
		return m, m.confirmRedo(t, st)
	case "c":
		if m.mcur < len(st.items) && st.items[m.mcur].State == domain.Dirty {
			it := st.items[m.mcur]
			return m, m.openConfirm(fmt.Sprintf("Clear dirty flag on %d_%s? Only do this after repairing the target by hand.", it.Version, it.Name), func(m *Model) tea.Cmd {
				return m.clearDirty(t, it.Version)
			})
		}
		m.notice = "selected migration is not dirty"
	case "r":
		m.reloadMigrations()
		return m, m.refresh(t)
	case "n":
		return m, m.openNewMigrationForm()
	}
	return m, nil
}

// confirmRedo checks the selected migration can be redone and asks first: it
// runs the migration's Down and then its Up, which can destroy data.
func (m *Model) confirmRedo(t domain.Target, st *targetState) tea.Cmd {
	if m.mcur >= len(st.items) {
		return nil
	}
	it := st.items[m.mcur]
	label := fmt.Sprintf("%d_%s", it.Version, it.Name)
	if it.State == domain.Pending {
		m.notice = label + " is not applied yet; use u or s to apply it"
		return nil
	}
	if reason := blocked(it, "redone"); reason != "" {
		m.notice = reason
		return nil
	}
	later := 0
	for _, x := range st.items[m.mcur+1:] {
		if x.Record != nil {
			later++
		}
	}
	q := fmt.Sprintf("Redo %s on %s? This runs its Down, then its Up, and may destroy data.", label, t.Name)
	if later > 0 {
		q += fmt.Sprintf(" %d later applied migration(s) are left as they are and may depend on it.", later)
	}
	return m.openConfirm(q, func(m *Model) tea.Cmd { return m.redo(t, it.Version) })
}

// blocked explains why a migration in a dirty or missing state cannot be used
// for action ("redone", "a target"), or returns "" if it can.
func blocked(it domain.Item, action string) string {
	label := fmt.Sprintf("%d_%s", it.Version, it.Name)
	switch it.State {
	case domain.Dirty:
		return label + " is dirty; repair the target and clear the flag (c) first"
	case domain.Missing:
		return fmt.Sprintf("%s has no migration file, so it cannot be %s", label, action)
	}
	return ""
}

// goTo migrates the target to the selected migration. A pending selection
// applies every pending migration up to and including it. An applied one
// rolls back everything newer, so it becomes the latest applied migration.
// Both ask for confirmation, since Enter is easy to press by accident and a
// rollback can destroy data.
func (m *Model) goTo(t domain.Target, st *targetState) tea.Cmd {
	if m.mcur >= len(st.items) {
		return nil
	}
	it := st.items[m.mcur]
	label := fmt.Sprintf("%d_%s", it.Version, it.Name)
	if reason := blocked(it, "used as a target"); reason != "" {
		m.notice = reason
		return nil
	}

	if it.State == domain.Pending {
		pending := 0
		for _, x := range st.items[:m.mcur+1] {
			if x.State == domain.Pending {
				pending++
			}
		}
		q := fmt.Sprintf("Apply %d pending migration(s) on %s up to and including %s?", pending, t.Name, label)
		return m.openConfirm(q, func(m *Model) tea.Cmd { return m.upTo(t, it.Version) })
	}

	newer := 0
	for _, x := range st.items[m.mcur+1:] {
		if x.Record != nil {
			newer++
		}
	}
	if newer == 0 {
		m.notice = label + " is already the latest applied migration"
		return nil
	}
	q := fmt.Sprintf("Roll back %d migration(s) on %s so %s is the latest applied? This runs their Down sections and may destroy data.", newer, t.Name, label)
	return m.openConfirm(q, func(m *Model) tea.Cmd { return m.downTo(t, it.Version) })
}

func (m *Model) clearDirty(t domain.Target, version int64) tea.Cmd {
	svc := m.svc
	return func() tea.Msg {
		err := svc.ClearDirty(context.Background(), t.Name, version)
		return clearedMsg{target: t.Name, err: err}
	}
}

func badge(s domain.State) string {
	switch s {
	case domain.Applied:
		return okStyle.Render("applied")
	case domain.Pending:
		return warnStyle.Render("pending")
	case domain.Modified:
		return warnStyle.Render("applied (file modified)")
	case domain.Dirty:
		return errStyle.Render("DIRTY")
	case domain.Missing:
		return errStyle.Render("applied (file missing)")
	}
	return string(s)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
