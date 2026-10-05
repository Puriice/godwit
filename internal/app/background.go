package app

import (
	"context"
	"fmt"

	"github.com/puriice/godwit/internal/domain"
)

// RunStatus is a snapshot of a background run's log.
type RunStatus struct {
	Job domain.Job
	Pid int // the worker's process id

	Lines []string // progress lines written since the offset passed to Poll
	// Queued and Finished are the versions the run announced it would do, and
	// the ones it completed, since the offset passed to Poll.
	Queued   []int64
	Finished []int64
	Next     int64 // offset to pass to the next Poll

	Active bool // the worker is still running
	// Cancelling is set once a cancel was requested but the worker has not
	// stopped yet.
	Cancelling bool

	// Pending are the jobs waiting behind this run, oldest first.
	Pending []domain.Job
	// More is set on a successful run that is handing over to the next queued
	// job: another run follows in a new worker.
	More bool

	Done      bool   // the worker finished and recorded its outcome
	Cancelled bool   // ... because it was cancelled
	Count     int    // migrations completed
	Err       string // failure message, also set when cancelled
}

// BackgroundRunner starts runs in a separate process that keeps going after
// this one exits, and reports on them. Poll also finds runs started by an
// earlier process. A target without a run reports a zero RunStatus.
type BackgroundRunner interface {
	// Start runs job in a new worker. When the target already has a run it
	// queues the job behind it instead and reports queued: the worker starts
	// it when the run ends well, and drops it if the run fails or is cancelled.
	Start(job domain.Job) (queued bool, err error)
	// Dequeue removes the job at position index (from 0, oldest first) of the
	// target's queue, provided it is still job. It fails when the queue has
	// changed since, for instance because that job has started.
	Dequeue(target string, index int, job domain.Job) error
	// ClearQueue removes every job queued behind the target's run and returns
	// how many there were. The run itself is not affected.
	ClearQueue(target string) (int, error)
	// Stop asks the target's run to cancel. It returns at once; Poll reports
	// Cancelling until the run has stopped, then Cancelled.
	Stop(target string) error
	// LogPath is where a target's run writes its progress.
	LogPath(target string) string
	Poll(target string, from int64) (RunStatus, error)
}

// RunJob performs j, whatever its operation, and returns how many migrations
// completed.
func (s *Service) RunJob(ctx context.Context, j domain.Job, prog Progress) (int, error) {
	switch j.Op {
	case domain.OpUp:
		return s.Up(ctx, j.Target, j.N, prog)
	case domain.OpDown:
		return s.Down(ctx, j.Target, j.N, prog)
	case domain.OpBatch:
		return s.DownBatch(ctx, j.Target, prog)
	case domain.OpUpTo:
		return s.UpTo(ctx, j.Target, j.Version, prog)
	case domain.OpDownTo:
		return s.DownTo(ctx, j.Target, j.Version, prog)
	case domain.OpOnly:
		return s.ApplyOnly(ctx, j.Target, j.Version, prog)
	case domain.OpRedo:
		if err := s.Redo(ctx, j.Target, j.Version, prog); err != nil {
			return 0, err
		}
		return 1, nil
	}
	return 0, fmt.Errorf("unknown job operation %q", j.Op)
}
