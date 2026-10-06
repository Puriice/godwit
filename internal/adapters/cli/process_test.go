package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

// procRunner is a fakeRunner with a canned run status.
type procRunner struct {
	fakeRunner
	status    app.RunStatus
	stopped   []string
	dequeued  []int
	cleared   int
	clearedOf string
}

func (p *procRunner) Poll(string, int64) (app.RunStatus, error) { return p.status, nil }
func (p *procRunner) Stop(t string) error                       { p.stopped = append(p.stopped, t); return nil }
func (p *procRunner) Dequeue(_ string, i int, _ domain.Job) error {
	p.dequeued = append(p.dequeued, i)
	return nil
}
func (p *procRunner) ClearQueue(t string) (int, error) {
	p.clearedOf = t
	return len(p.status.Pending), nil
}

func runningStatus() app.RunStatus {
	return app.RunStatus{
		Job:      domain.Job{Target: "prod", Op: domain.OpUp},
		Pid:      42,
		Active:   true,
		Queued:   []int64{1, 2, 3},
		Finished: []int64{1},
		Lines:    []string{"a", "b", "c"},
		Pending:  []domain.Job{{Target: "prod", Op: domain.OpDown, N: 2}, {Target: "prod", Op: domain.OpRedo, Version: 7}},
	}
}

func process(t *testing.T, bg app.BackgroundRunner, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := Process(newSvc(t), bg, args, &out)
	return out.String(), err
}

func TestProcessList(t *testing.T) {
	out, err := process(t, &procRunner{status: runningStatus()}, "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"prod", "running", "1/3", "down -n 2", "redo 7", "queued"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	out, _ = process(t, &procRunner{}, "list")
	if !strings.Contains(out, "No background runs") {
		t.Errorf("out = %q", out)
	}
}

func TestProcessShowTailsTheLog(t *testing.T) {
	out, err := process(t, &procRunner{status: runningStatus()}, "show", "-n", "2", "prod")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "  a\n") || !strings.Contains(out, "  c\n") || !strings.Contains(out, "pid 42") {
		t.Errorf("out:\n%s", out)
	}
}

func TestProcessCancel(t *testing.T) {
	bg := &procRunner{status: runningStatus()}
	if _, err := process(t, bg, "cancel", "prod"); err != nil {
		t.Fatal(err)
	}
	if len(bg.stopped) != 1 || bg.stopped[0] != "prod" {
		t.Errorf("stopped = %v", bg.stopped)
	}
	if _, err := process(t, &procRunner{}, "cancel", "prod"); err == nil {
		t.Error("want an error when nothing is running")
	}
}

func TestProcessDequeueAndClear(t *testing.T) {
	bg := &procRunner{status: runningStatus()}
	if _, err := process(t, bg, "dequeue", "prod", "2"); err != nil {
		t.Fatal(err)
	}
	if len(bg.dequeued) != 1 || bg.dequeued[0] != 1 {
		t.Errorf("dequeued = %v, want position 2 as index 1", bg.dequeued)
	}
	for _, pos := range []string{"0", "3", "x"} {
		if _, err := process(t, bg, "dequeue", "prod", pos); err == nil {
			t.Errorf("position %s: want an error", pos)
		}
	}
	out, err := process(t, bg, "clear-queue", "prod")
	if err != nil || bg.clearedOf != "prod" || !strings.Contains(out, "Removed 2") {
		t.Errorf("clear: %q %v %q", out, err, bg.clearedOf)
	}
}

func TestProcessErrors(t *testing.T) {
	bg := &procRunner{}
	for _, args := range [][]string{{}, {"bogus"}, {"show", "nope"}, {"cancel"}} {
		if _, err := process(t, bg, args...); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
	if _, err := process(t, nil, "list"); err == nil {
		t.Error("want an error without a runner")
	}
}
