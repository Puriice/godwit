package tui

import (
	"fmt"
	"strings"

	"github.com/puriice/godwit/internal/domain"
)

// logTail is how many log lines the processes panel shows for the selected
// target.
const logTail = 6

// hasProcesses reports whether the target has a run to show: one in progress,
// or the latest one, which stays until the next starts.
func (st *targetState) hasProcesses() bool { return st.job.Op != "" }

// processStatus renders how the target's latest run is doing.
func (st *targetState) processStatus() string {
	switch {
	case st.running:
		return warnStyle.Render("running…")
	case st.runErr != nil:
		return errStyle.Render("failed")
	}
	return okStyle.Render("finished")
}

// processLine renders one row of the panel.
func (m *Model) processLine(t domain.Target, st *targetState, selected bool) string {
	progress := ""
	if st.total > 0 && !st.redo {
		progress = fmt.Sprintf(" %d/%d", st.done, st.total)
	}
	where := "in this window"
	if m.bg != nil {
		where = "in the background"
		if st.running && st.pid != 0 {
			where = fmt.Sprintf("pid %d", st.pid)
		}
	}
	return fmt.Sprintf("%s%-16s %-7s %s%s %s", pointer(selected), t.Name, st.job.Op, st.processStatus(), progress, dimStyle.Render(where))
}

// viewProcesses lists the targets that have a run, and the tail of the log of
// the selected target's run. It is empty when no target has run anything.
func (m *Model) viewProcesses() string {
	var rows []string
	selected := ""
	for i, t := range m.targets() {
		st := m.state(t.Name)
		if !st.hasProcesses() {
			continue
		}
		rows = append(rows, m.processLine(t, st, i == m.cursor))
		if i == m.cursor {
			selected = t.Name
		}
	}
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n" + titleStyle.Render("Processes") + "\n")
	for _, r := range rows {
		b.WriteString(r + "\n")
	}
	if selected != "" {
		log := m.state(selected).log
		if len(log) > logTail {
			log = log[len(log)-logTail:]
		}
		b.WriteString("\n" + dimStyle.Render("log · "+selected) + "\n")
		for _, l := range log {
			b.WriteString("  " + dimStyle.Render(truncate(l, max(m.width-4, 20))) + "\n")
		}
	}
	return b.String()
}
