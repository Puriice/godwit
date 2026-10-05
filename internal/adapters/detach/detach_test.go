package detach

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

// seed records a run the way Start does, minus spawning the process.
func seed(t *testing.T, r *Runner, j domain.Job, pid int) {
	t.Helper()
	pidFile, logFile := r.paths(j.Target)
	if err := os.MkdirAll(filepath.Dir(pidFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	meta := strconv.Itoa(pid) + "\n" + j.Args()[0] + " " + j.Args()[1] + " " + j.Args()[2] + " " + j.Args()[3] + "\n"
	if err := os.WriteFile(pidFile, []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerLogIsPolled(t *testing.T) {
	r := &Runner{root: t.TempDir()}
	job := domain.Job{Target: "prod", Op: domain.OpUp, N: 2}
	seed(t, r, job, os.Getpid())

	st, err := r.Poll("prod", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Active || st.Done || st.Job != job {
		t.Fatalf("before the worker: %+v", st)
	}

	err = r.RunWorker(context.Background(), job.Args(), func(_ context.Context, j domain.Job, prog app.Progress) (int, error) {
		prog(domain.Event{Direction: domain.Up, Version: 1, Name: "a", Phase: domain.Started})
		prog(domain.Event{Direction: domain.Up, Version: 1, Name: "a", Phase: domain.Done})
		return 1, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err = r.Poll("prod", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Done || st.Active || st.Err != "" || st.Count != 1 || len(st.Lines) != 2 {
		t.Fatalf("after the worker: %+v", st)
	}
	// Following from the returned offset yields nothing new.
	if again, _ := r.Poll("prod", st.Next); len(again.Lines) != 0 {
		t.Fatalf("re-read lines: %v", again.Lines)
	}
}

func TestWorkerRecordsFailure(t *testing.T) {
	r := &Runner{root: t.TempDir()}
	job := domain.Job{Target: "prod", Op: domain.OpRedo, Version: 7}
	seed(t, r, job, os.Getpid())

	boom := errors.New("7_x: syntax\nerror")
	err := r.RunWorker(context.Background(), job.Args(), func(context.Context, domain.Job, app.Progress) (int, error) {
		return 0, boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	st, _ := r.Poll("prod", 0)
	if !st.Done || st.Err != "7_x: syntax error" {
		t.Fatalf("status: %+v", st)
	}
}

func TestNoRunReportsNothing(t *testing.T) {
	r := &Runner{root: t.TempDir()}
	st, err := r.Poll("prod", 0)
	if err != nil || st.Active || st.Done || st.Job.Op != "" {
		t.Fatalf("got %+v, %v", st, err)
	}
}

func TestDeadWorkerIsNotActive(t *testing.T) {
	r := &Runner{root: t.TempDir()}
	seed(t, r, domain.Job{Target: "prod", Op: domain.OpUp}, 1<<30)
	if st, _ := r.Poll("prod", 0); st.Active || st.Done {
		t.Fatalf("status: %+v", st)
	}
}

func TestStopCancelsTheRunningWorker(t *testing.T) {
	r := &Runner{root: t.TempDir()}
	job := domain.Job{Target: "prod", Op: domain.OpUp}
	seed(t, r, job, os.Getpid())

	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- r.RunWorker(context.Background(), job.Args(), func(ctx context.Context, _ domain.Job, _ app.Progress) (int, error) {
			close(started)
			<-ctx.Done() // a statement in flight, aborted by the cancellation
			return 1, ctx.Err()
		})
	}()
	<-started

	if err := r.Stop("prod"); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.Poll("prod", 0); !st.Cancelling {
		t.Errorf("not reported as cancelling: %+v", st)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("worker error = %v", err)
	}
	st, _ := r.Poll("prod", 0)
	if !st.Done || !st.Cancelled || st.Count != 1 || st.Err == "" || st.Cancelling {
		t.Errorf("after cancel: %+v", st)
	}
}

func TestStopWithoutARunFails(t *testing.T) {
	r := &Runner{root: t.TempDir()}
	if err := r.Stop("prod"); err == nil {
		t.Error("want an error when nothing is running")
	}
}

func TestStartQueuesBehindARunningWorker(t *testing.T) {
	r := &Runner{root: t.TempDir()}
	seed(t, r, domain.Job{Target: "prod", Op: domain.OpUp}, os.Getpid())

	redo := domain.Job{Target: "prod", Op: domain.OpRedo, Version: 7}
	queued, err := r.Start(redo)
	if err != nil || !queued {
		t.Fatalf("Start = %v, %v; want queued", queued, err)
	}
	down := domain.Job{Target: "prod", Op: domain.OpDown, N: 1}
	if queued, err := r.Start(down); err != nil || !queued {
		t.Fatalf("second Start = %v, %v; want queued", queued, err)
	}
	st, _ := r.Poll("prod", 0)
	if len(st.Pending) != 2 || st.Pending[0] != redo || st.Pending[1] != down {
		t.Fatalf("pending = %+v", st.Pending)
	}
	if !st.Active || st.Job.Op != domain.OpUp {
		t.Errorf("the running job was disturbed: %+v", st)
	}
}

func TestDequeueRemovesOnlyThatJob(t *testing.T) {
	r := &Runner{root: t.TempDir()}
	seed(t, r, domain.Job{Target: "prod", Op: domain.OpUp}, os.Getpid())
	redo := domain.Job{Target: "prod", Op: domain.OpRedo, Version: 7}
	down := domain.Job{Target: "prod", Op: domain.OpDown, N: 1}
	for _, j := range []domain.Job{redo, down} {
		if _, err := r.Start(j); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Dequeue("prod", 0, redo); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.Poll("prod", 0); len(st.Pending) != 1 || st.Pending[0] != down {
		t.Fatalf("pending = %+v", st.Pending)
	}
	if err := r.Dequeue("prod", 0, redo); err == nil {
		t.Error("want an error for a job that is not at that position")
	}
	if err := r.Dequeue("prod", 5, down); err == nil {
		t.Error("want an error for a position past the end")
	}
	if err := r.Dequeue("prod", 0, down); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.Poll("prod", 0); len(st.Pending) != 0 {
		t.Errorf("pending = %+v", st.Pending)
	}
}

// With A, B, A queued, removing the last A must leave A, B in that order.
func TestDequeueRemovesTheSelectedDuplicate(t *testing.T) {
	r := &Runner{root: t.TempDir()}
	seed(t, r, domain.Job{Target: "prod", Op: domain.OpUp}, os.Getpid())
	a := domain.Job{Target: "prod", Op: domain.OpRedo, Version: 7}
	b := domain.Job{Target: "prod", Op: domain.OpDown, N: 1}
	for _, j := range []domain.Job{a, b, a} {
		if _, err := r.Start(j); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Dequeue("prod", 2, a); err != nil {
		t.Fatal(err)
	}
	st, _ := r.Poll("prod", 0)
	if len(st.Pending) != 2 || st.Pending[0] != a || st.Pending[1] != b {
		t.Fatalf("pending = %+v, want A then B", st.Pending)
	}

	// A position that moved on (the worker took the first job meanwhile)
	// must not remove a different job.
	if err := r.Dequeue("prod", 1, a); err == nil {
		t.Error("removed something at a position that no longer holds that job")
	}
	if st, _ := r.Poll("prod", 0); len(st.Pending) != 2 {
		t.Errorf("pending = %+v", st.Pending)
	}
}

func TestWorkerHandsOverToTheNextQueuedJob(t *testing.T) {
	chainGrace = 0
	r := &Runner{root: t.TempDir(), exe: filepath.Join(t.TempDir(), "missing")}
	job := domain.Job{Target: "prod", Op: domain.OpUp}
	seed(t, r, job, os.Getpid())
	if _, err := r.Start(domain.Job{Target: "prod", Op: domain.OpRedo, Version: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start(domain.Job{Target: "prod", Op: domain.OpDown}); err != nil {
		t.Fatal(err)
	}

	// The next worker cannot start here (no executable), which is reported.
	err := r.RunWorker(context.Background(), job.Args(), func(context.Context, domain.Job, app.Progress) (int, error) {
		return 2, nil
	})
	if err == nil {
		t.Fatal("want the failure to start the queued run")
	}
	st, _ := r.Poll("prod", 0)
	// The next run starts a fresh log, so the end of this one is gone.
	if len(st.Lines) != 1 || !strings.Contains(st.Lines[0], "could not start the queued run") {
		t.Errorf("the failure is not logged: %v", st.Lines)
	}
	if len(st.Pending) != 1 || st.Pending[0].Op != domain.OpDown {
		t.Errorf("the redo was not taken off the queue: %+v", st.Pending)
	}
}

func TestFailureDropsTheQueue(t *testing.T) {
	r := &Runner{root: t.TempDir()}
	job := domain.Job{Target: "prod", Op: domain.OpUp}
	seed(t, r, job, os.Getpid())
	if _, err := r.Start(domain.Job{Target: "prod", Op: domain.OpDown}); err != nil {
		t.Fatal(err)
	}
	_ = r.RunWorker(context.Background(), job.Args(), func(context.Context, domain.Job, app.Progress) (int, error) {
		return 0, errors.New("boom")
	})
	st, _ := r.Poll("prod", 0)
	if len(st.Pending) != 0 || st.More || !st.Done {
		t.Errorf("status: %+v", st)
	}
	if len(st.Lines) != 1 || !strings.Contains(st.Lines[0], "1 queued job(s) dropped") {
		t.Errorf("the drop is not logged: %v", st.Lines)
	}
}

func TestAFailureIsNotCancelled(t *testing.T) {
	r := &Runner{root: t.TempDir()}
	job := domain.Job{Target: "prod", Op: domain.OpUp}
	seed(t, r, job, os.Getpid())
	_ = r.RunWorker(context.Background(), job.Args(), func(context.Context, domain.Job, app.Progress) (int, error) {
		return 0, errors.New("boom")
	})
	if st, _ := r.Poll("prod", 0); !st.Done || st.Cancelled || st.Err != "boom" {
		t.Errorf("status: %+v", st)
	}
}
