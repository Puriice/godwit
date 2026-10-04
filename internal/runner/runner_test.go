package runner

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/puriice/godwit/internal/driver"
	"github.com/puriice/godwit/internal/migration"
)

// fakeConn is an in-memory driver.Conn.
type fakeConn struct {
	recs     map[int64]*driver.Record
	failOn   int64 // Apply of this version fails, leaving a dirty row
	locked   bool
	lockErr  error
	applied  []int64
	reverted []int64
}

func newFake() *fakeConn { return &fakeConn{recs: map[int64]*driver.Record{}} }

func (f *fakeConn) Close() error                      { return nil }
func (f *fakeConn) EnsureTable(context.Context) error { return nil }
func (f *fakeConn) ClearDirty(_ context.Context, v int64) error {
	f.recs[v].Dirty = false
	return nil
}
func (f *fakeConn) Lock(context.Context) (func() error, error) {
	if f.lockErr != nil {
		return nil, f.lockErr
	}
	f.locked = true
	return func() error { f.locked = false; return nil }, nil
}
func (f *fakeConn) Applied(context.Context) ([]driver.Record, error) {
	var out []driver.Record
	for _, r := range f.recs {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}
func (f *fakeConn) Apply(_ context.Context, m *migration.Migration) error {
	if m.Version == f.failOn {
		f.recs[m.Version] = &driver.Record{Version: m.Version, Name: m.Name, Checksum: m.Checksum, Dirty: true}
		return errors.New("boom")
	}
	f.recs[m.Version] = &driver.Record{Version: m.Version, Name: m.Name, Checksum: m.Checksum, AppliedAt: time.Now()}
	f.applied = append(f.applied, m.Version)
	return nil
}
func (f *fakeConn) Revert(_ context.Context, m *migration.Migration) error {
	delete(f.recs, m.Version)
	f.reverted = append(f.reverted, m.Version)
	return nil
}

func mig(v int64, name string) *migration.Migration {
	return &migration.Migration{Version: v, Name: name, Checksum: name, Parsed: &migration.Parsed{}}
}

func states(items []Item) []State {
	var s []State
	for _, it := range items {
		s = append(s, it.State)
	}
	return s
}

func TestStatusStates(t *testing.T) {
	f := newFake()
	f.recs[1] = &driver.Record{Version: 1, Name: "a", Checksum: "a"}
	f.recs[2] = &driver.Record{Version: 2, Name: "b", Checksum: "old"}
	f.recs[4] = &driver.Record{Version: 4, Name: "gone", Checksum: "x"}
	migs := []*migration.Migration{mig(1, "a"), mig(2, "b"), mig(3, "c")}

	items, err := Status(context.Background(), f, migs)
	if err != nil {
		t.Fatal(err)
	}
	want := []State{Applied, Modified, Pending, Missing}
	got := states(items)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("states = %v, want %v", got, want)
		}
	}
}

func TestMigrateUpLimitAndEvents(t *testing.T) {
	f := newFake()
	migs := []*migration.Migration{mig(1, "a"), mig(2, "b"), mig(3, "c")}
	var evs []Event

	n, err := MigrateUp(context.Background(), f, migs, 2, func(e Event) { evs = append(evs, e) })
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if len(evs) != 4 || evs[0].Phase != Started || evs[1].Phase != Done {
		t.Errorf("events = %+v", evs)
	}
	if f.locked {
		t.Error("lock not released")
	}
	n, _ = MigrateUp(context.Background(), f, migs, 0, nil)
	if n != 1 || len(f.recs) != 3 {
		t.Errorf("second run applied %d", n)
	}
}

func TestMigrateUpFailureLeavesDirtyAndBlocks(t *testing.T) {
	f := newFake()
	f.failOn = 2
	migs := []*migration.Migration{mig(1, "a"), mig(2, "b"), mig(3, "c")}
	var last Event

	n, err := MigrateUp(context.Background(), f, migs, 0, func(e Event) { last = e })
	if err == nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if last.Phase != Failed || last.Version != 2 || last.Err == nil {
		t.Errorf("last event = %+v", last)
	}
	if f.locked {
		t.Error("lock not released after failure")
	}

	f.failOn = 0
	if _, err := MigrateUp(context.Background(), f, migs, 0, nil); err == nil {
		t.Error("expected dirty target to block further runs")
	}
	if err := ClearDirty(context.Background(), f, 2); err != nil {
		t.Fatal(err)
	}
	if n, err := MigrateUp(context.Background(), f, migs, 0, nil); err != nil || n != 1 {
		t.Errorf("after clear: n=%d err=%v", n, err)
	}
}

func TestMigrateDown(t *testing.T) {
	f := newFake()
	migs := []*migration.Migration{mig(1, "a"), mig(2, "b"), mig(3, "c")}
	MigrateUp(context.Background(), f, migs, 0, nil)

	n, err := MigrateDown(context.Background(), f, migs, 2, nil)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if len(f.reverted) != 2 || f.reverted[0] != 3 || f.reverted[1] != 2 {
		t.Errorf("revert order = %v", f.reverted)
	}

	// Default n is 1; a recorded migration without a file cannot be reverted.
	f.recs[9] = &driver.Record{Version: 9, Name: "ghost", Checksum: "g"}
	if _, err := MigrateDown(context.Background(), f, migs, 0, nil); err == nil {
		t.Error("expected error reverting migration with no file")
	}
}

func TestLockError(t *testing.T) {
	f := newFake()
	f.lockErr = errors.New("held")
	if _, err := MigrateUp(context.Background(), f, nil, 0, nil); err == nil {
		t.Error("expected lock error")
	}
}
