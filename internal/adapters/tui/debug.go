package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/puriice/godwit/internal/domain"
)

// migrationTabs renders the two panels of the migrations screen, which left
// and right switch between, with the current one highlighted.
func (m *Model) migrationTabs() string {
	names := []string{"migrations", "debug"}
	cur := 0
	if m.mdebug {
		cur = 1
	}
	parts := make([]string, len(names))
	for i, n := range names {
		if i == cur {
			parts[i] = cursorStyle.Render("[" + n + "]")
		} else {
			parts[i] = n
		}
	}
	return strings.Join(parts, " ")
}

// recordedState is what the state table says about a migration: pending when
// it has no record, otherwise dirty or applied. Unlike Item.State it ignores
// whether the file changed or went missing, since those are not stored.
func recordedState(it domain.Item) domain.State {
	switch {
	case it.Record == nil:
		return domain.Pending
	case it.Record.Dirty:
		return domain.Dirty
	}
	return domain.Applied
}

func shortSum(s string) string {
	if s == "" {
		return "-"
	}
	return truncate(s, 9)
}

// debugDetail describes the selected migration for the debug panel: what is
// recorded for it on the target next to what the file says.
func (m *Model) debugDetail(st *targetState) string {
	if m.mcur < 0 || m.mcur >= len(st.items) {
		return ""
	}
	it := st.items[m.mcur]
	var b strings.Builder
	b.WriteRune('\n')
	fmt.Fprintf(&b, "%s\n", titleStyle.Render(fmt.Sprintf("%d_%s", it.Version, it.Name)))
	fmt.Fprintf(&b, "  state     %s\n", badgeItem(it))
	fmt.Fprintf(&b, "  recorded  %s\n", recordedState(it))
	file := "missing"
	if it.Migration != nil {
		file = shortSum(it.Migration.Checksum)
	}
	rec := "-"
	if it.Record != nil {
		rec = shortSum(it.Record.Checksum)
	}
	fmt.Fprintf(&b, "  checksum  file %s · recorded %s\n", file, rec)
	if r := it.Record; r != nil {
		fmt.Fprintf(&b, "  run       batch %d · applied %s · %d ms\n", r.Batch, r.AppliedAt.Format("2006-01-02 15:04:05"), r.DurationMS)
	}
	b.WriteString(warnStyle.Render("  Forcing changes only the godwit_migration record, never the schema."))
	b.WriteRune('\n')
	return b.String()
}

// updateDebug handles keys while the debug panel is showing.
func (m *Model) updateDebug(t domain.Target, st *targetState, key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc", "q":
		m.screen = scrTargets
	case "up", "k":
		if m.mcur > 0 {
			m.mcur--
		}
	case "down", "j":
		if m.mcur < len(st.items)-1 {
			m.mcur++
		}
	case "p":
		return m, m.confirmForce(t, st, domain.Pending)
	case "d":
		return m, m.confirmForce(t, st, domain.Dirty)
	case "a":
		return m, m.confirmForce(t, st, domain.Applied)
	case "r":
		if t.Disabled {
			m.notice = t.Name + " is disabled; press t on the migrations panel to enable it"
			break
		}
		m.reloadMigrations()
		return m, m.refresh(t)
	}
	return m, nil
}

// confirmForce asks before forcing the selected migration into state to.
func (m *Model) confirmForce(t domain.Target, st *targetState, to domain.State) tea.Cmd {
	switch {
	case t.Disabled:
		m.notice = t.Name + " is disabled; enable it before forcing a state"
		return nil
	case st.running:
		m.notice = "a run is in progress on " + t.Name
		return nil
	case m.mcur >= len(st.items):
		return nil
	}
	it := st.items[m.mcur]
	label := fmt.Sprintf("%d_%s", it.Version, it.Name)
	from := recordedState(it)
	if from == to {
		m.notice = fmt.Sprintf("%s is already recorded as %s", label, to)
		return nil
	}
	q := fmt.Sprintf("Force %s on %s from %s to %s? Only the godwit_migration record changes; the schema is not touched.", label, t.Name, from, to)
	return m.openConfirm(q, func(m *Model) tea.Cmd { return m.forceState(t, it.Version, label, to) })
}

func (m *Model) forceState(t domain.Target, version int64, label string, to domain.State) tea.Cmd {
	svc := m.svc
	return func() tea.Msg {
		err := svc.ForceState(context.Background(), t.Name, version, to)
		return forcedMsg{target: t.Name, label: label, to: to, err: err}
	}
}
