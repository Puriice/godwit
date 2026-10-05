package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/puriice/godwit/internal/domain"
)

// ForceState lets fakeDB act as a StateForcer, recording what it was asked.
func (f *fakeDB) ForceState(_ context.Context, m *domain.Migration, state domain.State) error {
	f.forced = append(f.forced, *m)
	switch state {
	case domain.Pending:
		delete(f.recs, m.Version)
	default:
		f.recs[m.Version] = &domain.Record{Version: m.Version, Name: m.Name, Checksum: m.Checksum,
			AppliedAt: time.Now(), Batch: m.Batch, Dirty: state == domain.Dirty}
	}
	return nil
}

func TestForceState(t *testing.T) {
	h := newHarness(t, mig(1, "a"), mig(2, "b"), mig(3, "c"))
	ctx := context.Background()
	h.db.recs[1] = &domain.Record{Version: 1, Name: "a", Checksum: "a", Batch: 4}

	status := func() []domain.State {
		items, err := h.svc.Status(ctx, "t")
		if err != nil {
			t.Fatal(err)
		}
		return states(items)
	}
	force := func(v int64, to domain.State) {
		t.Helper()
		if err := h.svc.ForceState(ctx, "t", v, to); err != nil {
			t.Fatal(err)
		}
	}

	force(2, domain.Applied) // pending -> applied, in a batch after the existing one
	force(3, domain.Dirty)   // pending -> dirty
	force(1, domain.Pending) // applied -> pending
	want := []domain.State{domain.Pending, domain.Applied, domain.Dirty}
	if got := status(); got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("states = %v, want %v", got, want)
	}
	if got := h.db.forced[0]; got.Version != 2 || got.Name != "b" || got.Checksum != "b" || got.Batch != 5 {
		t.Errorf("forced migration = %+v, want version 2, name b, checksum b, batch 5", got)
	}

	force(3, domain.Applied) // dirty -> applied keeps its recorded batch
	force(2, domain.Dirty)   // applied -> dirty
	if got := status(); got[1] != domain.Dirty || got[2] != domain.Applied {
		t.Errorf("states = %v", got)
	}

	if h.db.locked {
		t.Error("lock not released")
	}
	if len(h.db.reverted) != 0 {
		t.Errorf("forcing a state must not run Down: %v", h.db.reverted)
	}
}

func TestForceStateKeepsRecordForMissingFile(t *testing.T) {
	h := newHarness(t, mig(1, "a"))
	h.db.recs[9] = &domain.Record{Version: 9, Name: "gone", Checksum: "old", Batch: 2}
	if err := h.svc.ForceState(context.Background(), "t", 9, domain.Dirty); err != nil {
		t.Fatal(err)
	}
	if got := h.db.forced[0]; got.Name != "gone" || got.Checksum != "old" || got.Batch != 2 {
		t.Errorf("forced = %+v, want the recorded name, checksum and batch", got)
	}
}

func TestForceStateErrors(t *testing.T) {
	h := newHarness(t, mig(1, "a"))
	ctx := context.Background()
	if err := h.svc.ForceState(ctx, "t", 7, domain.Applied); err == nil || !strings.Contains(err.Error(), "no migration") {
		t.Errorf("unknown version: %v", err)
	}
	for _, bad := range []domain.State{domain.Modified, domain.Missing, "bogus"} {
		if err := h.svc.ForceState(ctx, "t", 1, bad); err == nil {
			t.Errorf("state %q should be refused", bad)
		}
	}
	if len(h.db.forced) != 0 {
		t.Errorf("nothing should have been forced: %+v", h.db.forced)
	}

	// A database without the capability (a plugin driver) is refused clearly.
	plain, err := New(h.store, h.src, plainFactory{h.db})
	if err != nil {
		t.Fatal(err)
	}
	if err := plain.ForceState(ctx, "t", 1, domain.Applied); err == nil || !strings.Contains(err.Error(), "cannot force") {
		t.Errorf("plain database: %v", err)
	}
}

// plainFactory hands out a Database that hides fakeDB's ForceState.
type plainFactory struct{ db *fakeDB }

type plainDB struct{ Database }

func (plainFactory) Drivers() []string { return []string{"mysql", "postgres"} }
func (f plainFactory) Open(context.Context, domain.Target, string) (Database, error) {
	return plainDB{f.db}, nil
}
