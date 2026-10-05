package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/puriice/godwit/internal/domain"
)

const processesHelp = "←/→ switch panel · ↑/↓ select · x cancel run · q quit"

const (
	barWidth      = 20
	maxProcessMig = 8 // migrations listed in the details before "… N more"
)

// hasProcesses reports whether the target has a run to show: one in progress,
// or the latest one, which stays until the next starts.
func (st *targetState) hasProcesses() bool { return st.job.Op != "" }

// processStatus renders how the target's latest run is doing.
func (st *targetState) processStatus() string {
	switch {
	case st.running && st.cancelling:
		return warnStyle.Render("cancelling…")
	case st.running:
		return warnStyle.Render("running…")
	case st.cancelled:
		return warnStyle.Render("cancelled")
	case st.runErr != nil:
		return errStyle.Render("failed")
	}
	return okStyle.Render("finished")
}

// processTargets are the targets that have a run, in target order.
func (m *Model) processTargets() []domain.Target {
	var out []domain.Target
	for _, t := range m.targets() {
		if m.state(t.Name).hasProcesses() {
			out = append(out, t)
		}
	}
	return out
}

// progress is "done/total", or empty when the run announced nothing (a redo,
// or a run that has not started its first migration yet).
func (st *targetState) progress() string {
	if st.total == 0 || st.redo {
		return ""
	}
	return fmt.Sprintf("%d/%d", st.done, st.total)
}

func (st *targetState) bar() string {
	if st.total == 0 || st.redo {
		return ""
	}
	filled := barWidth * st.done / st.total
	return okStyle.Render(strings.Repeat("█", filled)) + dimStyle.Render(strings.Repeat("░", barWidth-filled))
}

// where says which process runs the migrations.
func (m *Model) where(st *targetState) string {
	switch {
	case m.bg == nil:
		return "this window"
	case st.running && st.pid != 0:
		return fmt.Sprintf("pid %d (background)", st.pid)
	}
	return "background"
}

// runVersions are the migrations the run covers, in the order it takes them.
func (st *targetState) runVersions() []int64 {
	if st.redo {
		return []int64{st.job.Version}
	}
	return st.order
}

// migrationName looks a version up in the target's loaded migrations.
func (st *targetState) migrationName(v int64) string {
	for _, it := range st.items {
		if it.Version == v {
			return fmt.Sprintf("%d_%s", v, it.Name)
		}
	}
	return fmt.Sprint(v)
}

// migrationStatus is what the run has done, or is doing, with one migration.
func (st *targetState) migrationStatus(v int64, current bool) string {
	switch {
	case st.redo && !st.running:
		if st.runErr != nil {
			return errStyle.Render("failed")
		}
		return okStyle.Render("done")
	case st.finished[v]:
		return okStyle.Render("done")
	case st.running && current:
		return warnStyle.Render("running…")
	case st.running:
		return dimStyle.Render("queued")
	}
	return dimStyle.Render("not run")
}

func (m *Model) viewProcesses() string {
	var b strings.Builder
	b.WriteString(m.header(m.tabs()))
	ts := m.processTargets()
	if len(ts) == 0 {
		b.WriteString(dimStyle.Render("No background runs. Start one from a target (u apply all, or open it) or with: godwit migrate up --detach"))
		b.WriteRune('\n')
		return b.String() + m.footer(processesHelp)
	}
	m.prcur = min(m.prcur, len(ts)-1)
	t := ts[m.prcur]
	st := m.state(t.Name)

	// Like the debug panel: the list on the left, the selected run's details
	// in a second column, or below the list on a terminal too narrow for two.
	detailW, wide := m.processColumns()
	var list strings.Builder
	for i, t := range ts {
		list.WriteString(m.processRow(pointer(i == m.prcur), t.Name, m.state(t.Name)))
		list.WriteRune('\n')
	}
	listText := strings.TrimRight(list.String(), "\n")
	if wide {
		detail := m.processDetail(t.Name, st, detailW)
		cols := lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(processRowWidth).Render(listText), strings.Repeat(" ", sideGap), detail)
		b.WriteString(cols)
		b.WriteRune('\n')
	} else {
		b.WriteString(listText)
		b.WriteString("\n\n")
		b.WriteString(m.processDetail(t.Name, st, m.width))
		b.WriteRune('\n')
	}

	if m.bg != nil {
		fmt.Fprintf(&b, "\n%s %s\n", titleStyle.Render("Log"), dimStyle.Render(m.bg.LogPath(t.Name)))
	} else {
		fmt.Fprintf(&b, "\n%s\n", titleStyle.Render("Log"))
	}
	tail := max(min(m.height-22, 20), 4)
	log := st.log
	if len(log) > tail {
		log = log[len(log)-tail:]
	}
	for _, l := range log {
		fmt.Fprintf(&b, "  %s\n", dimStyle.Render(truncate(l, max(m.width-4, 20))))
	}
	return b.String() + m.footer(processesHelp)
}

// processRowWidth is the width of the list column: pointer, target name,
// operation, status and progress.
const (
	processRowWidth = 2 + processNameW + 1 + processOpW + 1 + processStatusW + 1 + processProgW
	processNameW    = 14
	processOpW      = 6
	processStatusW  = 11
	processProgW    = 5
)

// processColumns sizes the detail column of the processes tab. wide is false
// when the terminal cannot fit it beside the list.
func (m *Model) processColumns() (detail int, wide bool) {
	detail = min(max(sideDetailMin, m.width*2/5), sideDetailMax)
	return detail, m.width >= processRowWidth+sideGap+sideDetailMin
}

// padRight pads s with spaces to n columns, counting what is visible.
func padRight(s string, n int) string {
	return s + strings.Repeat(" ", max(n-lipgloss.Width(s), 0))
}

// processRow is one line of the list of runs.
func (m *Model) processRow(ptr, name string, st *targetState) string {
	return ptr + padRight(truncate(name, processNameW), processNameW) + " " +
		padRight(string(st.job.Op), processOpW) + " " +
		padRight(st.processStatus(), processStatusW) + " " +
		dimStyle.Render(st.progress())
}

// processDetail describes one run, for the column beside the list.
func (m *Model) processDetail(name string, st *targetState, width int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", titleStyle.Render(truncate(name, max(width, 1))))
	row := func(label, value string) { fmt.Fprintf(&b, "%s%s\n", dimStyle.Render(fmt.Sprintf("%-10s", label)), value) }
	row("operation", string(st.job.Op))
	row("status", st.processStatus())
	row("process", m.where(st))
	if bar := st.bar(); bar != "" {
		row("progress", bar+" "+st.progress())
	}
	if st.runErr != nil {
		row("error", errStyle.Render(truncate(firstLine(st.runErr.Error()), max(width-10, 10))))
	}

	if versions := st.runVersions(); len(versions) > 0 {
		current := int64(-1)
		for _, v := range versions {
			if !st.finished[v] {
				current = v
				break
			}
		}
		fmt.Fprintf(&b, "\n%s\n", titleStyle.Render("migrations"))
		for i, v := range versions {
			if i == maxProcessMig {
				fmt.Fprintf(&b, "%s\n", dimStyle.Render(fmt.Sprintf("… %d more", len(versions)-i)))
				break
			}
			status := st.migrationStatus(v, v == current)
			nameW := max(width-lipgloss.Width(status)-1, 8)
			fmt.Fprintf(&b, "%s %s\n", padRight(truncate(st.migrationName(v), nameW), nameW), status)
		}
	}
	return lipgloss.NewStyle().Width(max(width, 1)).Render(strings.TrimRight(b.String(), "\n"))
}

func (m *Model) updateProcesses(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	ts := m.processTargets()
	switch key.String() {
	case "q":
		return m, tea.Quit
	case "left", "h":
		m.switchPanel(-1)
	case "right", "l":
		m.switchPanel(1)
	case "up", "k":
		if m.prcur > 0 {
			m.prcur--
		}
	case "down", "j":
		if m.prcur < len(ts)-1 {
			m.prcur++
		}
	case "x":
		if m.prcur < len(ts) {
			return m, m.cancelRun(ts[m.prcur])
		}
	}
	return m, nil
}

// cancelRun asks for confirmation, then cancels the target's run. The
// migration being executed is stopped where it is: a transaction is rolled
// back, but finished passes of a repeat block stay and the migration is left
// dirty, to be redone or cleared.
func (m *Model) cancelRun(t domain.Target) tea.Cmd {
	st := m.state(t.Name)
	switch {
	case !st.running:
		m.notice = t.Name + " has no run in progress"
		return nil
	case st.cancelling:
		m.notice = "already cancelling the run on " + t.Name
		return nil
	}
	return m.openConfirm(fmt.Sprintf("Cancel the %s run on %q? The migration being executed is stopped where it is: a transaction is rolled back, but finished passes of a repeat block stay and the migration is left dirty. Migrations not started yet are skipped.", st.job.Op, t.Name), func(m *Model) tea.Cmd {
		m.stopRun(t.Name)
		return nil
	})
}

// stopRun cancels the target's run: by asking its background worker, or by
// cancelling the context of the run in this process. The run reports its end
// like any other, flagged as cancelled.
func (m *Model) stopRun(target string) {
	st := m.state(target)
	if !st.running || st.cancelling {
		return
	}
	switch {
	case m.bg != nil:
		if err := m.bg.Stop(target); err != nil {
			m.notice = "cancel: " + err.Error()
			return
		}
	case st.cancel != nil:
		st.cancel()
	default:
		return
	}
	st.cancelling = true
	st.addLog("■ cancel requested")
}
