package app

import (
	"context"
	"fmt"

	"github.com/puriice/godwit/internal/domain"
)

// RunStatus is a snapshot of a background run's log.
type RunStatus struct {
	Job    domain.Job
	Pid    int      // the worker's process id
	Lines  []string // progress lines written since the offset passed to Poll
	// Queued and Finished are the versions the run announced it would do, and
	// the ones it completed, since the offset passed to Poll.
	Queued   []int64
	Finished []int64
	Next     int64 // offset to pass to the next Poll
	Active bool     // the worker is still running
	Done   bool     // the worker finished and recorded its outcome
	Count  int      // migrations completed, when Done
	Err    string   // failure message, when Done
}

// BackgroundRunner starts runs in a separate process that keeps going after
// this one exits, and reports on them. Poll also finds runs started by an
// earlier process. A target without a run reports a zero RunStatus.
type BackgroundRunner interface {
	Start(job domain.Job) error
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
