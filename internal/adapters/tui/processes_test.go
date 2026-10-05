package tui

import (
	"strings"
	"testing"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

type stubRunner struct{}

func (stubRunner) Start(domain.Job) error                    { return nil }
func (stubRunner) LogPath(string) string                     { return "" }
func (stubRunner) Poll(string, int64) (app.RunStatus, error) { return app.RunStatus{}, nil }

func TestNoProcessesPanelWithoutRuns(t *testing.T) {
	m := testModel(t)
	if strings.Contains(m.View(), "log · ") { // not "Processes": the temp dir path has the test name
		t.Errorf("panel shown with nothing to show:\n%s", m.View())
	}
}

func TestProcessesPanelFollowsABackgroundRun(t *testing.T) {
	m := testModel(t).WithBackground(stubRunner{})
	m.Update(statusMsg{target: "a", items: []domain.Item{
		{Version: 1, Name: "x", State: domain.Applied},
		{Version: 2, Name: "y", State: domain.Dirty}, // being executed by the worker
		{Version: 3, Name: "z", State: domain.Pending},
	}})

	// A run left over from an earlier session is picked up on startup.
	m.Update(bgAttachMsg{target: "a", st: app.RunStatus{
		Job:    domain.Job{Target: "a", Op: domain.OpUp},
		Pid:    4242,
		Active: true,
		Lines:  []string{"✓ up 1_x", "… up 2_y"},
		Queued: []int64{2, 3},
	}})

	st := m.state("a")
	if !st.running || st.total != 2 || st.done != 0 {
		t.Fatalf("running=%v total=%d done=%d", st.running, st.total, st.done)
	}
	// The migration the worker is on reads as running, not dirty, and so does
	// the one still queued behind it.
	if st.items[1].State != domain.Running || st.items[2].State != domain.Running || st.items[0].State != domain.Applied {
		t.Errorf("states = %v %v %v", st.items[0].State, st.items[1].State, st.items[2].State)
	}
	v := m.View()
	for _, want := range []string{"Processes", "pid 4242", "up", "0/2", "… up 2_y"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}

	m.Update(bgPollMsg{target: "a", st: app.RunStatus{
		Job: domain.Job{Target: "a", Op: domain.OpUp}, Pid: 4242, Active: true,
		Lines: []string{"✓ up 2_y"}, Finished: []int64{2},
	}})
	if st.done != 1 || st.queue[2] || !st.queue[3] {
		t.Errorf("done=%d queue=%v", st.done, st.queue)
	}

	m.Update(bgPollMsg{target: "a", st: app.RunStatus{
		Job: domain.Job{Target: "a", Op: domain.OpUp}, Pid: 4242, Done: true, Count: 2,
		Lines: []string{"✓ up 3_z"}, Finished: []int64{3},
	}})
	v = m.View()
	if st.running || !strings.Contains(v, "finished") || !strings.Contains(v, "2/2") {
		t.Errorf("after the run (running=%v):\n%s", st.running, v)
	}
}
