package tui

import (
	"strings"
	"testing"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

type stubRunner struct{ stopped []string }

func (*stubRunner) Start(domain.Job) error                    { return nil }
func (*stubRunner) LogPath(t string) string                   { return "runs/" + t + ".log" }
func (*stubRunner) Poll(string, int64) (app.RunStatus, error) { return app.RunStatus{}, nil }
func (s *stubRunner) Stop(t string) error                     { s.stopped = append(s.stopped, t); return nil }

// processesModel is a model on the processes panel, with a background run on
// target "a" found at startup: migration 1 done, 2 executing, 3 queued.
func processesModel(t *testing.T) (*Model, *stubRunner) {
	t.Helper()
	bg := &stubRunner{}
	m := testModel(t).WithBackground(bg)
	m.Update(statusMsg{target: "a", items: []domain.Item{
		{Version: 1, Name: "x", State: domain.Applied},
		{Version: 2, Name: "y", State: domain.Dirty}, // being executed by the worker
		{Version: 3, Name: "z", State: domain.Pending},
	}})
	m.Update(bgAttachMsg{target: "a", st: app.RunStatus{
		Job:    domain.Job{Target: "a", Op: domain.OpUp},
		Pid:    4242,
		Active: true,
		Lines:  []string{"✓ up 1_x", "… up 2_y"},
		Queued: []int64{1, 2, 3},
		// 1 finished before the TUI was opened.
		Finished: []int64{1},
	}})
	return m, bg
}

func TestProcessesIsATabBetweenMigrationsAndPlugins(t *testing.T) {
	m := testModel(t)
	m.screen = scrFiles
	m.switchPanel(1)
	if m.screen != scrProcesses {
		t.Fatalf("screen after migrations = %v", m.screen)
	}
	m.switchPanel(1)
	if m.screen != scrPlugins {
		t.Fatalf("screen after processes = %v", m.screen)
	}
	if v := m.tabs(); !strings.Contains(v, "processes") {
		t.Errorf("tabs = %q", v)
	}
}

func TestProcessesPanelWithoutRuns(t *testing.T) {
	m := testModel(t)
	m.screen = scrProcesses
	if v := m.View(); !strings.Contains(v, "No background runs") {
		t.Errorf("empty panel:\n%s", v)
	}
	if v := m.viewTargets(); strings.Contains(v, "log · ") {
		t.Errorf("the index page should not list processes:\n%s", v)
	}
}

func TestProcessesPanelShowsDetailsOfARun(t *testing.T) {
	m, _ := processesModel(t)
	st := m.state("a")
	if !st.running || st.total != 3 || st.done != 1 {
		t.Fatalf("running=%v total=%d done=%d", st.running, st.total, st.done)
	}
	// The migration the worker is on reads as running, not dirty, and so does
	// the one still queued behind it.
	if st.items[1].State != domain.Running || st.items[2].State != domain.Running || st.items[0].State != domain.Applied {
		t.Errorf("states = %v %v %v", st.items[0].State, st.items[1].State, st.items[2].State)
	}

	m.screen = scrProcesses
	v := m.View()
	for _, want := range []string{"operation", "migrations", "running…", "pid 4242", "1/3", "runs/a.log", "1_x", "2_y", "3_z", "done", "queued", "… up 2_y"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}

	m.Update(bgPollMsg{target: "a", st: app.RunStatus{
		Job: domain.Job{Target: "a", Op: domain.OpUp}, Pid: 4242, Done: true, Count: 3,
		Lines: []string{"✓ up 2_y", "✓ up 3_z"}, Finished: []int64{2, 3},
	}})
	v = m.View()
	if st.running || !strings.Contains(v, "finished") || !strings.Contains(v, "3/3") {
		t.Errorf("after the run (running=%v):\n%s", st.running, v)
	}
}

// lineWith returns the first line of view that contains s.
func lineWith(view, s string) string {
	for l := range strings.SplitSeq(view, "\n") {
		if strings.Contains(l, s) {
			return l
		}
	}
	return ""
}

func TestProcessDetailSitsBesideTheList(t *testing.T) {
	m, _ := processesModel(t)
	// A second run, so the list has two rows.
	b := m.state("b")
	b.begin(domain.Job{Target: "b", Op: domain.OpDown}, 99)
	b.running = true
	m.screen = scrProcesses

	m.width = 100
	// The detail of the selected run starts on the list's second row.
	if l := lineWith(m.View(), "operation"); !strings.Contains(l, "down") || !strings.Contains(l, "up") {
		t.Errorf("wide: the detail is not beside the list row of the other run: %q", l)
	}

	m.width = 60
	if l := lineWith(m.View(), "operation"); strings.Contains(l, "down") {
		t.Errorf("narrow: the detail should go below the list: %q", l)
	}
}

func TestCancelAsksThenStopsTheWorker(t *testing.T) {
	m, bg := processesModel(t)
	m.screen = scrProcesses

	m.Update(key("x"))
	if m.screen != scrForm {
		t.Fatalf("no confirmation asked, screen = %v", m.screen)
	}
	if len(bg.stopped) != 0 {
		t.Fatal("stopped before the user confirmed")
	}
	// What confirming the form does.
	st := m.state("a")
	m.screen = scrProcesses
	m.stopRun("a")
	if len(bg.stopped) != 1 || bg.stopped[0] != "a" {
		t.Fatalf("stopped = %v", bg.stopped)
	}
	if !st.cancelling || !strings.Contains(m.viewProcesses(), "cancelling…") {
		t.Errorf("cancelling=%v:\n%s", st.cancelling, m.viewProcesses())
	}

	// The worker records the cancellation, which the poll reports.
	m.Update(bgPollMsg{target: "a", st: app.RunStatus{
		Job: domain.Job{Target: "a", Op: domain.OpUp}, Done: true, Cancelled: true, Count: 1,
		Err: "2_y: context canceled",
	}})
	if st.running || !st.cancelled || !strings.Contains(m.viewProcesses(), "cancelled") {
		t.Errorf("running=%v cancelled=%v:\n%s", st.running, st.cancelled, m.viewProcesses())
	}
}

func TestCancelNeedsARunningProcess(t *testing.T) {
	m, bg := processesModel(t)
	m.state("a").running = false
	m.screen = scrProcesses
	m.Update(key("x"))
	if m.screen == scrForm || len(bg.stopped) != 0 || !strings.Contains(m.notice, "no run in progress") {
		t.Errorf("screen=%v stopped=%v notice=%q", m.screen, bg.stopped, m.notice)
	}
}

func TestFailedRunIsNotShownAsCancelled(t *testing.T) {
	m, _ := processesModel(t)
	m.Update(bgPollMsg{target: "a", st: app.RunStatus{
		Job: domain.Job{Target: "a", Op: domain.OpUp}, Done: true, Err: "2_y: syntax error",
	}})
	st := m.state("a")
	if st.cancelled || st.runErr == nil {
		t.Errorf("cancelled=%v runErr=%v", st.cancelled, st.runErr)
	}
	m.screen = scrProcesses
	if v := m.View(); !strings.Contains(v, "failed") || !strings.Contains(v, "syntax error") {
		t.Errorf("view:\n%s", v)
	}
}
