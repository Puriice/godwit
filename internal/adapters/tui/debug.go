package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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

const (
	sideMinWidth  = 70 // narrower terminals stack the detail below the list
	sideDetailMin = 34
	sideDetailMax = 44
	sideGap       = 3  // spaces between the list and the detail column
	versionCol    = 15 // width of the version column, with its trailing space
	stateCol      = 9  // room for the one-word state in a row
	maxName       = 24
)

// columns splits the screen into the list column and the detail column of the
// debug panel. wide is false when the terminal is too narrow for two columns,
// and the detail then goes below the list. It depends only on the terminal, not
// on the tab, so both tabs lay their rows out the same way.
//
// The list column is exactly as wide as its rows and the detail column stops
// growing at sideDetailMax, so on a very wide terminal the two stay together
// at the left instead of drifting apart.
func (m *Model) columns() (list, detail int, wide bool) {
	if m.width < sideMinWidth {
		return 0, 0, false
	}
	detail = min(max(sideDetailMin, m.width*2/5), sideDetailMax)
	list = rowWidth(m.nameWidth())
	return list, detail, true
}

// rowWidth is the width of a list row with a name column of nameW.
func rowWidth(nameW int) int { return 2 + (versionCol - 1) + 1 + nameW + 1 + stateCol }

// nameWidth is the width of the name column, shared by both tabs so a row's
// version, name and state sit at the same screen columns in each. It takes
// what the detail column and the gap leave, up to maxName.
func (m *Model) nameWidth() int {
	if m.width < sideMinWidth {
		return maxName
	}
	detail := min(max(sideDetailMin, m.width*2/5), sideDetailMax)
	return min(max(m.width-detail-sideGap-rowWidth(0), 6), maxName)
}

// stateWord is a short, coloured state for the compact list.
func stateWord(it domain.Item) string {
	switch {
	case it.Disabled:
		return dimStyle.Render("off")
	case it.State == domain.Applied:
		return okStyle.Render("applied")
	case it.State == domain.Pending:
		return warnStyle.Render("pending")
	case it.State == domain.Modified:
		return warnStyle.Render("modified")
	case it.State == domain.Dirty:
		return errStyle.Render("DIRTY")
	case it.State == domain.Missing:
		return errStyle.Render("missing")
	}
	return string(it.State)
}

// listRow is one line of the migration list, the same in both tabs. short
// swaps the full state text (which may carry a reason, such as "applied (file
// modified)") for a one-word state, so the row fits beside the detail column.
func (m *Model) listRow(selected bool, it domain.Item, short bool) string {
	state := badgeItem(it)
	if short {
		state = stateWord(it)
	}
	nameW := m.nameWidth()
	return fmt.Sprintf("%s%-*d %-*s %s", pointer(selected), versionCol-1, it.Version, nameW, truncate(it.Name, nameW), state)
}

// debugDetail describes the selected migration for the debug panel: what is
// recorded for it on the target next to what the file says. Lines wrap to
// width.
func (m *Model) debugDetail(st *targetState, width int) string {
	if m.mcur < 0 || m.mcur >= len(st.items) {
		return ""
	}
	it := st.items[m.mcur]
	var b strings.Builder
	line := func(k, v string) { fmt.Fprintf(&b, "%-10s%s\n", k, v) }
	fmt.Fprintf(&b, "%s\n", titleStyle.Render(truncate(fmt.Sprintf("%d_%s", it.Version, it.Name), max(width, 1))))
	line("state", badgeItem(it))
	line("recorded", string(recordedState(it)))
	file := "missing"
	if it.Migration != nil {
		file = shortSum(it.Migration.Checksum)
	}
	line("file", file)
	if r := it.Record; r != nil {
		line("record", shortSum(r.Checksum))
		line("batch", fmt.Sprint(r.Batch))
		applied := "-"
		if !r.AppliedAt.IsZero() {
			applied = r.AppliedAt.Format("2006-01-02 15:04:05")
		}
		line("applied", applied)
		line("duration", fmt.Sprintf("%d ms", r.DurationMS))
	}
	b.WriteRune('\n')
	b.WriteString(warnStyle.Render("Record only; schema untouched."))
	return lipgloss.NewStyle().Width(max(width, 1)).Render(b.String())
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
