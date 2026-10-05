package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/puriice/godwit/internal/domain"
)

const processesHelp = "←/→ switch panel · ↑/↓ select · x cancel run / remove queued job · X clear queue · q quit"

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

// processItem is one selectable line of the processes list: a target's run,
// or one of the jobs queued behind it.
type processItem struct {
	target domain.Target
	queued int // index into the target's queue; -1 for the run itself
}

// processItems lists the runs in target order, each followed by its queue.
func (m *Model) processItems() []processItem {
	var out []processItem
	for _, t := range m.targets() {
		st := m.state(t.Name)
		if !st.hasProcesses() {
			continue
		}
		out = append(out, processItem{t, -1})
		for i := range st.pending {
			out = append(out, processItem{t, i})
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
	items := m.processItems()
	if len(items) == 0 {
		b.WriteString(dimStyle.Render("No background runs. Start one from a target (u apply all, or open it) or with: godwit migrate up --detach"))
		b.WriteRune('\n')
		return b.String() + m.footer(processesHelp)
	}
	m.prcur = min(m.prcur, len(items)-1)
	sel := items[m.prcur]
	t := sel.target
	st := m.state(t.Name)

	// Like the debug panel: the list on the left, the selected run's details
	// in a second column, or below the list on a terminal too narrow for two.
	detailW, wide := m.processColumns()
	var list strings.Builder
	for i, it := range items {
		ptr := pointer(i == m.prcur)
		if it.queued < 0 {
			list.WriteString(m.processRow(ptr, it.target.Name, m.state(it.target.Name)))
		} else {
			list.WriteString(queuedRow(ptr, m.state(it.target.Name).pending[it.queued]))
		}
		list.WriteRune('\n')
	}
	listText := strings.TrimRight(list.String(), "\n")
	detailOf := func(width int) string {
		if sel.queued >= 0 {
			return m.queuedDetail(t.Name, st, sel.queued, width)
		}
		return m.processDetail(t.Name, st, width)
	}
	if wide {
		detail := detailOf(detailW)
		cols := lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(processRowWidth).Render(listText), strings.Repeat(" ", sideGap), detail)
		b.WriteString(cols)
		b.WriteRune('\n')
	} else {
		b.WriteString(listText)
		b.WriteString("\n\n")
		b.WriteString(detailOf(m.width))
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

// queuedRow is one job waiting behind a run, indented under it.
func queuedRow(ptr string, j domain.Job) string {
	const labelW = processNameW + processOpW - 3
	return ptr + dimStyle.Render("  ↳ ") + padRight(truncate(jobLabel(j), labelW), labelW) + " " +
		padRight(dimStyle.Render("queued"), processStatusW)
}

// queuedDetail describes the idx'th job waiting behind a target's run.
func (m *Model) queuedDetail(name string, st *targetState, idx, width int) string {
	j := st.pending[idx]
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", titleStyle.Render(truncate(name+" · queued", max(width, 1))))
	row := func(label, value string) { fmt.Fprintf(&b, "%s%s\n", dimStyle.Render(fmt.Sprintf("%-10s", label)), value) }
	row("operation", string(j.Op))
	if j.Version != 0 {
		row("version", fmt.Sprint(j.Version))
	}
	if j.N != 0 {
		row("count", fmt.Sprint(j.N))
	}
	row("status", dimStyle.Render("queued"))
	row("position", fmt.Sprintf("%d of %d", idx+1, len(st.pending)))
	fmt.Fprintf(&b, "\n%s", dimStyle.Render("Starts when the run before it ends well. It is dropped if that run fails or is cancelled. x removes it; X clears the whole queue."))
	return lipgloss.NewStyle().Width(max(width, 1)).Render(b.String())
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
	if len(st.pending) > 0 {
		labels := make([]string, len(st.pending))
		for i, j := range st.pending {
			labels[i] = jobLabel(j)
		}
		row("queued", truncate(strings.Join(labels, ", "), max(width-10, 10)))
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
	items := m.processItems()
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
		if m.prcur < len(items)-1 {
			m.prcur++
		}
	case "x":
		if m.prcur < len(items) {
			it := items[m.prcur]
			if it.queued >= 0 {
				return m, m.removeQueued(it.target, it.queued)
			}
			return m, m.cancelRun(it.target)
		}
	case "X":
		if m.prcur < len(items) {
			return m, m.clearQueue(items[m.prcur].target)
		}
	}
	return m, nil
}

// clearQueue asks for confirmation, then removes every job queued behind the
// target's run. The run in progress is not affected.
func (m *Model) clearQueue(t domain.Target) tea.Cmd {
	n := len(m.state(t.Name).pending)
	if n == 0 || m.bg == nil {
		m.notice = "nothing is queued on " + t.Name
		return nil
	}
	return m.openConfirm(fmt.Sprintf("Remove all %d queued job(s) from the queue of %q? The run in progress is not affected.", n, t.Name), func(m *Model) tea.Cmd {
		m.clearPending(t.Name)
		return nil
	})
}

// clearPending empties the target's queue and puts the cursor on its run,
// since the queued row it may have been on is gone.
func (m *Model) clearPending(target string) {
	st := m.state(target)
	n, err := m.bg.ClearQueue(target)
	if err != nil {
		m.notice = "clear queue: " + err.Error()
		return
	}
	st.pending = nil
	for i, it := range m.processItems() {
		if it.target.Name == target && it.queued < 0 {
			m.prcur = i
			break
		}
	}
	st.addLog(fmt.Sprintf("－ removed %d job(s) from the queue", n))
	m.notice = fmt.Sprintf("removed %d queued job(s) from %s", n, target)
}

// removeQueued asks for confirmation, then takes the idx'th queued job off the
// target's queue. The run in progress is not affected.
func (m *Model) removeQueued(t domain.Target, idx int) tea.Cmd {
	st := m.state(t.Name)
	if idx >= len(st.pending) || m.bg == nil {
		return nil
	}
	job := st.pending[idx]
	return m.openConfirm(fmt.Sprintf("Remove the queued %s from the queue of %q? The run in progress is not affected.", jobLabel(job), t.Name), func(m *Model) tea.Cmd {
		m.dequeue(t.Name, idx, job)
		return nil
	})
}

// dequeue takes the job at position idx off the target's queue. The position,
// not just the job, identifies it: the same job may be queued more than once.
func (m *Model) dequeue(target string, idx int, job domain.Job) {
	st := m.state(target)
	if err := m.bg.Dequeue(target, idx, job); err != nil {
		m.notice = "remove: " + err.Error()
		return
	}
	if idx < len(st.pending) && st.pending[idx] == job {
		st.pending = slices.Delete(st.pending, idx, idx+1)
	}
	st.addLog(fmt.Sprintf("－ removed %s from the queue", jobLabel(job)))
	m.notice = fmt.Sprintf("removed %s from the queue", jobLabel(job))
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
