package app

import (
	"context"
	"testing"

	"github.com/puriice/godwit/internal/domain"
)

// record collects a run's events, leaving out the up-front Queued ones.
func record(evs *[]domain.Event) Progress {
	return func(e domain.Event) {
		if e.Phase != domain.Queued {
			*evs = append(*evs, e)
		}
	}
}

func TestRunAnnouncesItsQueueFirst(t *testing.T) {
	h := newHarness(t, mig(1, "a"), mig(2, "b"), mig(3, "c"))
	var evs []domain.Event

	if _, err := h.svc.Up(context.Background(), "t", 2, func(e domain.Event) { evs = append(evs, e) }); err != nil {
		t.Fatal(err)
	}
	if len(evs) < 3 || evs[0].Phase != domain.Queued || evs[0].Version != 1 || evs[1].Phase != domain.Queued || evs[1].Version != 2 || evs[2].Phase != domain.Started {
		t.Errorf("events = %+v", evs)
	}
}
