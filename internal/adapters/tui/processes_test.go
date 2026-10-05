package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

type stubRunner struct {
	stopped    []string
	cleared    []string
	dequeued   []domain.Job
	dequeuedAt []int
	dequeueOK  bool // Dequeue finds the job; otherwise it fails
	started    []domain.Job
	queued     bool // Start reports the job as queued behind a run
}

func (s *stubRunner) ClearQueue(target string) (int, error) {
	if !s.dequeueOK {
		return 0, errors.New("the queue has changed")
	}
	s.cleared = append(s.cleared, target)
	return 2, nil
}

func (s *stubRunner) Dequeue(_ string, index int, j domain.Job) error {
	if !s.dequeueOK {
		return errors.New("the queue has changed")
	}
	s.dequeued = append(s.dequeued, j)
	s.dequeuedAt = append(s.dequeuedAt, index)
	return nil
}

func (s *stubRunner) Start(j domain.Job) (bool, error) {
	s.started = append(s.started, j)
	return s.queued, nil
}
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

// queuedModel is processesModel with two jobs queued behind the run on "a".
func queuedModel(t *testing.T) (*Model, *stubRunner) {
	t.Helper()
	m, bg := processesModel(t)
	m.state("a").pending = []domain.Job{
		{Target: "a", Op: domain.OpRedo, Version: 7},
		{Target: "a", Op: domain.OpDown, N: 1},
	}
	m.screen = scrProcesses
	return m, bg
}

func TestQueuedJobsAreSelectableSubItems(t *testing.T) {
	m, _ := queuedModel(t)
	if v := m.viewProcesses(); !strings.Contains(v, "↳ redo 7") || !strings.Contains(v, "↳ down -n 1") {
		t.Fatalf("the queue is not listed under the run:\n%s", v)
	}
	if got := len(m.processItems()); got != 3 {
		t.Fatalf("items = %d, want the run and two queued jobs", got)
	}

	m.Update(key("down"))
	v := m.viewProcesses()
	if !strings.Contains(v, "a · queued") || !strings.Contains(v, "1 of 2") {
		t.Errorf("details of the first queued job:\n%s", v)
	}
	m.Update(key("down"))
	if v := m.viewProcesses(); !strings.Contains(v, "2 of 2") {
		t.Errorf("details of the second queued job:\n%s", v)
	}
	m.Update(key("down"))
	if m.prcur != 2 {
		t.Errorf("moved past the last item: %d", m.prcur)
	}
}

func TestXOnAQueuedJobRemovesItNotTheRun(t *testing.T) {
	m, bg := queuedModel(t)
	bg.dequeueOK = true
	m.Update(key("down"))
	m.Update(key("x"))
	if m.screen != scrForm {
		t.Fatalf("no confirmation asked, screen = %v", m.screen)
	}
	if len(bg.dequeued) != 0 || len(bg.stopped) != 0 {
		t.Fatal("acted before the user confirmed")
	}
	// What confirming the form does.
	m.screen = scrProcesses
	m.dequeue("a", 0, m.state("a").pending[0])
	st := m.state("a")
	if len(bg.dequeued) != 1 || bg.dequeued[0].Op != domain.OpRedo || len(bg.stopped) != 0 {
		t.Fatalf("dequeued=%v stopped=%v", bg.dequeued, bg.stopped)
	}
	if len(st.pending) != 1 || st.pending[0].Op != domain.OpDown || !st.running {
		t.Errorf("pending=%v running=%v", st.pending, st.running)
	}
}

func TestXOnADuplicateRemovesTheSelectedPosition(t *testing.T) {
	m, bg := queuedModel(t)
	bg.dequeueOK = true
	a := domain.Job{Target: "a", Op: domain.OpRedo, Version: 7}
	b := domain.Job{Target: "a", Op: domain.OpDown, N: 1}
	st := m.state("a")
	st.pending = []domain.Job{a, b, a}

	// Select the last A: the run, then A, B, A.
	m.Update(key("down"))
	m.Update(key("down"))
	m.Update(key("down"))
	m.Update(key("x"))
	if m.screen != scrForm {
		t.Fatalf("no confirmation asked, screen = %v", m.screen)
	}
	// What confirming the form does, for the selected row.
	m.screen = scrProcesses
	m.dequeue("a", 2, st.pending[2])
	if len(bg.dequeuedAt) != 1 || bg.dequeuedAt[0] != 2 {
		t.Fatalf("asked the runner to remove position %v, want 2", bg.dequeuedAt)
	}
	if len(st.pending) != 2 || st.pending[0] != a || st.pending[1] != b {
		t.Errorf("pending = %v, want the first A and B to stay in order", st.pending)
	}
}

func TestCapitalXAsksThenClearsTheWholeQueue(t *testing.T) {
	m, bg := queuedModel(t)
	bg.dequeueOK = true
	st := m.state("a")

	// From a queued row, not only from the run.
	m.Update(key("down"))
	m.Update(key("down"))
	m.Update(key("X"))
	if m.screen != scrForm {
		t.Fatalf("no confirmation asked, screen = %v", m.screen)
	}
	if len(bg.cleared) != 0 || len(st.pending) != 2 {
		t.Fatal("cleared before the user confirmed")
	}
	// What confirming the form does.
	m.screen = scrProcesses
	m.clearPending("a")
	if len(bg.cleared) != 1 || bg.cleared[0] != "a" || len(bg.stopped) != 0 {
		t.Fatalf("cleared=%v stopped=%v", bg.cleared, bg.stopped)
	}
	if len(st.pending) != 0 || !st.running || st.cancelling {
		t.Errorf("pending=%v running=%v cancelling=%v", st.pending, st.running, st.cancelling)
	}
	if m.prcur != 0 {
		t.Errorf("the cursor stayed on a row that is gone: %d", m.prcur)
	}
	if got := len(m.processItems()); got != 1 {
		t.Errorf("items = %d, want just the run", got)
	}
}

func TestCapitalXWithNothingQueuedSaysSo(t *testing.T) {
	m, bg := processesModel(t)
	m.screen = scrProcesses
	m.Update(key("X"))
	if m.screen == scrForm || len(bg.cleared) != 0 || !strings.Contains(m.notice, "nothing is queued") {
		t.Errorf("screen=%v cleared=%v notice=%q", m.screen, bg.cleared, m.notice)
	}
}

func TestClearQueueFailureKeepsTheQueue(t *testing.T) {
	m, bg := queuedModel(t)
	bg.dequeueOK = false
	m.clearPending("a")
	if len(m.state("a").pending) != 2 || !strings.Contains(m.notice, "clear queue") {
		t.Errorf("pending=%v notice=%q", m.state("a").pending, m.notice)
	}
}

func TestRemovingAJobThatAlreadyStartedIsReported(t *testing.T) {
	m, bg := queuedModel(t)
	bg.dequeueOK = false
	m.dequeue("a", 0, m.state("a").pending[0])
	if len(m.state("a").pending) != 2 || !strings.Contains(m.notice, "has changed") {
		t.Errorf("pending=%v notice=%q", m.state("a").pending, m.notice)
	}
}

func TestStartingAJobWhileRunningQueuesIt(t *testing.T) {
	m, bg := processesModel(t)
	bg.queued = true
	a, _ := m.svc.Target("a")
	m.redo(a, 7)
	st := m.state("a")
	if len(bg.started) != 1 || bg.started[0].Op != domain.OpRedo {
		t.Fatalf("started = %v", bg.started)
	}
	if !st.running || st.job.Op != domain.OpUp {
		t.Errorf("the running job was disturbed: running=%v job=%v", st.running, st.job)
	}
	if len(st.pending) != 1 || !strings.Contains(m.notice, "queued redo 7") {
		t.Errorf("pending=%v notice=%q", st.pending, m.notice)
	}
	m.screen = scrProcesses
	if v := m.viewProcesses(); !strings.Contains(v, "redo 7") {
		t.Errorf("the queue is not shown:\n%s", v)
	}
}

func TestRunHandsOverToTheQueuedJob(t *testing.T) {
	m, _ := processesModel(t)
	st := m.state("a")

	// The run ends well and the next queued job takes over in a new worker.
	m.Update(bgPollMsg{target: "a", st: app.RunStatus{
		Job: domain.Job{Target: "a", Op: domain.OpUp}, Pid: 4242, Done: true, More: true, Count: 3,
	}})
	if !st.running || st.waitPid != 4242 {
		t.Fatalf("running=%v waitPid=%d", st.running, st.waitPid)
	}

	// Until another worker appears, the old one's log is not mistaken for it.
	m.Update(bgPollMsg{target: "a", st: app.RunStatus{
		Job: domain.Job{Target: "a", Op: domain.OpUp}, Pid: 4242, Done: true, More: true, Count: 3,
	}})
	m.Update(bgPollMsg{target: "a"})
	if !st.running || st.waitPid != 4242 || st.runErr != nil {
		t.Fatalf("running=%v waitPid=%d err=%v", st.running, st.waitPid, st.runErr)
	}

	m.Update(bgPollMsg{target: "a", st: app.RunStatus{
		Job: domain.Job{Target: "a", Op: domain.OpRedo, Version: 2}, Pid: 5151, Active: true,
		Lines: []string{"… down 2_y"},
	}})
	if !st.running || st.waitPid != 0 || st.pid != 5151 || st.job.Op != domain.OpRedo {
		t.Errorf("running=%v waitPid=%d pid=%d job=%v", st.running, st.waitPid, st.pid, st.job)
	}

	// ... and it ends like any other run.
	m.Update(bgPollMsg{target: "a", st: app.RunStatus{
		Job: domain.Job{Target: "a", Op: domain.OpRedo, Version: 2}, Pid: 5151, Done: true, Count: 1,
	}})
	if st.running {
		t.Error("still running after the last job")
	}
}

func TestHandoverThatNeverStartsIsReported(t *testing.T) {
	m, _ := processesModel(t)
	st := m.state("a")
	m.Update(bgPollMsg{target: "a", st: app.RunStatus{
		Job: domain.Job{Target: "a", Op: domain.OpUp}, Pid: 4242, Done: true, More: true,
	}})
	for range maxHandoverPolls + 1 {
		m.Update(bgPollMsg{target: "a"})
	}
	if st.running || st.runErr == nil || !strings.Contains(st.runErr.Error(), "did not start") {
		t.Errorf("running=%v err=%v", st.running, st.runErr)
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
