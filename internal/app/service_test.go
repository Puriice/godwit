package app

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/puriice/godwit/internal/domain"
)

// ---- fakes for the driven ports ----

type fakeDB struct {
	recs     map[int64]*domain.Record
	failOn   int64 // Apply of this version fails, leaving a dirty row
	locked   bool
	lockErr  error
	reverted []int64
	closed   int
	forced   []domain.Migration // what ForceState was asked to write
}

func newFakeDB() *fakeDB { return &fakeDB{recs: map[int64]*domain.Record{}} }

func (f *fakeDB) Close() error                      { f.closed++; return nil }
func (f *fakeDB) EnsureTable(context.Context) error { return nil }
func (f *fakeDB) ClearDirty(_ context.Context, v int64) error {
	f.recs[v].Dirty = false
	return nil
}
func (f *fakeDB) Lock(context.Context) (func() error, error) {
	if f.lockErr != nil {
		return nil, f.lockErr
	}
	f.locked = true
	return func() error { f.locked = false; return nil }, nil
}
func (f *fakeDB) Applied(context.Context) ([]domain.Record, error) {
	var out []domain.Record
	for _, r := range f.recs {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}
func (f *fakeDB) Apply(_ context.Context, m *domain.Migration) error {
	if m.Version == f.failOn {
		f.recs[m.Version] = &domain.Record{Version: m.Version, Name: m.Name, Checksum: m.Checksum, Dirty: true}
		return errors.New("boom")
	}
	f.recs[m.Version] = &domain.Record{Version: m.Version, Name: m.Name, Checksum: m.Checksum, AppliedAt: time.Now(), Batch: m.Batch}
	return nil
}
func (f *fakeDB) Revert(_ context.Context, m *domain.Migration) error {
	delete(f.recs, m.Version)
	f.reverted = append(f.reverted, m.Version)
	return nil
}

type fakeFactory struct {
	db      *fakeDB
	openErr error
}

func (f *fakeFactory) Drivers() []string { return []string{"mysql", "postgres"} }
func (f *fakeFactory) Open(context.Context, domain.Target, string) (Database, error) {
	return f.db, f.openErr
}

type fakeSource struct {
	migs    []*domain.Migration
	created []string
}

func (f *fakeSource) Load(string) ([]*domain.Migration, error) { return f.migs, nil }
func (f *fakeSource) Resolve(dir string) string                { return "/abs/" + dir }
func (f *fakeSource) EnsureDir(string) error                   { return nil }
func (f *fakeSource) Create(dir, name string) (string, error) {
	f.created = append(f.created, dir+"/"+name)
	return dir + "/" + name, nil
}

type fakeStore struct {
	project domain.Project
	saves   int
	pw      map[string]string // persisted
	session map[string]string
}

func newFakeStore() *fakeStore {
	return &fakeStore{pw: map[string]string{}, session: map[string]string{}}
}
func (f *fakeStore) Load() (domain.Project, error) { return f.project, nil }
func (f *fakeStore) Save(p domain.Project) error   { f.project = p; f.saves++; return nil }
func (f *fakeStore) Password(t string) (string, bool) {
	if v, ok := f.session[t]; ok {
		return v, true
	}
	v, ok := f.pw[t]
	return v, ok
}
func (f *fakeStore) SetPassword(t, pw string, persist bool) error {
	if persist {
		f.pw[t] = pw
	} else {
		f.session[t] = pw
	}
	return nil
}
func (f *fakeStore) DeletePassword(t string) error {
	delete(f.pw, t)
	delete(f.session, t)
	return nil
}

func mig(v int64, name string) *domain.Migration {
	return &domain.Migration{Version: v, Name: name, Checksum: name}
}

// harness wires a Service to fakes with one target "t" that has a password.
type harness struct {
	svc   *Service
	db    *fakeDB
	src   *fakeSource
	store *fakeStore
}

func newHarness(t *testing.T, migs ...*domain.Migration) *harness {
	t.Helper()
	h := &harness{db: newFakeDB(), src: &fakeSource{migs: migs}, store: newFakeStore()}
	h.store.project = domain.Project{Targets: []domain.Target{{Name: "t", Driver: "postgres"}}}
	h.store.pw["t"] = "secret"
	svc, err := New(h.store, h.src, &fakeFactory{db: h.db})
	if err != nil {
		t.Fatal(err)
	}
	h.svc = svc
	return h
}

func states(items []domain.Item) []domain.State {
	var s []domain.State
	for _, it := range items {
		s = append(s, it.State)
	}
	return s
}

// ---- running migrations ----

func TestStatusStates(t *testing.T) {
	h := newHarness(t, mig(1, "a"), mig(2, "b"), mig(3, "c"))
	h.db.recs[1] = &domain.Record{Version: 1, Name: "a", Checksum: "a"}
	h.db.recs[2] = &domain.Record{Version: 2, Name: "b", Checksum: "old"}
	h.db.recs[4] = &domain.Record{Version: 4, Name: "gone", Checksum: "x"}

	items, err := h.svc.Status(context.Background(), "t")
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.State{domain.Applied, domain.Modified, domain.Pending, domain.Missing}
	got := states(items)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("states = %v, want %v", got, want)
		}
	}
	if h.db.closed != 1 {
		t.Errorf("connection closed %d times, want 1", h.db.closed)
	}
}

func TestUpLimitAndEvents(t *testing.T) {
	h := newHarness(t, mig(1, "a"), mig(2, "b"), mig(3, "c"))
	var evs []domain.Event

	n, err := h.svc.Up(context.Background(), "t", 2, func(e domain.Event) { evs = append(evs, e) })
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if len(evs) != 4 || evs[0].Phase != domain.Started || evs[1].Phase != domain.Done {
		t.Errorf("events = %+v", evs)
	}
	if h.db.locked {
		t.Error("lock not released")
	}
	if h.db.closed != 1 {
		t.Errorf("connection closed %d times, want 1", h.db.closed)
	}
	n, _ = h.svc.Up(context.Background(), "t", 0, nil)
	if n != 1 || len(h.db.recs) != 3 {
		t.Errorf("second run applied %d", n)
	}
}

func TestUpFailureLeavesDirtyAndBlocks(t *testing.T) {
	h := newHarness(t, mig(1, "a"), mig(2, "b"), mig(3, "c"))
	h.db.failOn = 2
	var last domain.Event

	n, err := h.svc.Up(context.Background(), "t", 0, func(e domain.Event) { last = e })
	if err == nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if last.Phase != domain.Failed || last.Version != 2 || last.Err == nil {
		t.Errorf("last event = %+v", last)
	}
	if h.db.locked {
		t.Error("lock not released after failure")
	}

	h.db.failOn = 0
	if _, err := h.svc.Up(context.Background(), "t", 0, nil); err == nil {
		t.Error("expected dirty target to block further runs")
	}
	if err := h.svc.ClearDirty(context.Background(), "t", 2); err != nil {
		t.Fatal(err)
	}
	if n, err := h.svc.Up(context.Background(), "t", 0, nil); err != nil || n != 1 {
		t.Errorf("after clear: n=%d err=%v", n, err)
	}
}

func TestDown(t *testing.T) {
	h := newHarness(t, mig(1, "a"), mig(2, "b"), mig(3, "c"))
	h.svc.Up(context.Background(), "t", 0, nil)

	n, err := h.svc.Down(context.Background(), "t", 2, nil)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if len(h.db.reverted) != 2 || h.db.reverted[0] != 3 || h.db.reverted[1] != 2 {
		t.Errorf("revert order = %v", h.db.reverted)
	}

	// Default n is 1; a recorded migration without a file cannot be reverted.
	h.db.recs[9] = &domain.Record{Version: 9, Name: "ghost", Checksum: "g", AppliedAt: time.Now().Add(time.Hour)}
	if _, err := h.svc.Down(context.Background(), "t", 0, nil); err == nil {
		t.Error("expected error reverting migration with no file")
	}
}

func TestDownRevertsLatestApplied(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, mig(1, "a"), mig(2, "b"), mig(3, "c"))
	h.svc.Up(ctx, "t", 0, nil)
	// Migration 2 was applied last, even though 3 has the higher version.
	base := time.Now()
	h.db.recs[1].AppliedAt = base.Add(-3 * time.Hour)
	h.db.recs[3].AppliedAt = base.Add(-2 * time.Hour)
	h.db.recs[2].AppliedAt = base.Add(-1 * time.Hour)

	if n, err := h.svc.Down(ctx, "t", 1, nil); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if len(h.db.reverted) != 1 || h.db.reverted[0] != 2 {
		t.Errorf("reverted = %v, want [2]", h.db.reverted)
	}
}

func TestDownBatch(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, mig(1, "a"), mig(2, "b"), mig(3, "c"), mig(4, "d"), mig(5, "e"))
	h.svc.Up(ctx, "t", 1, nil) // batch 1: 1
	h.svc.Up(ctx, "t", 3, nil) // batch 2: 2, 3, 4
	h.svc.Up(ctx, "t", 0, nil) // batch 3: 5
	if h.db.recs[1].Batch != 1 || h.db.recs[3].Batch != 2 || h.db.recs[5].Batch != 3 {
		t.Fatalf("batches = %d %d %d", h.db.recs[1].Batch, h.db.recs[3].Batch, h.db.recs[5].Batch)
	}

	// The latest batch holds only 5; then the next one rolls back 4, 3, 2 in reverse.
	if n, err := h.svc.DownBatch(ctx, "t", nil); err != nil || n != 1 {
		t.Fatalf("first: n=%d err=%v", n, err)
	}
	if n, err := h.svc.DownBatch(ctx, "t", nil); err != nil || n != 3 {
		t.Fatalf("second: n=%d err=%v", n, err)
	}
	want := []int64{5, 4, 3, 2}
	if len(h.db.reverted) != len(want) {
		t.Fatalf("reverted = %v, want %v", h.db.reverted, want)
	}
	for i := range want {
		if h.db.reverted[i] != want[i] {
			t.Errorf("reverted = %v, want %v", h.db.reverted, want)
		}
	}
	if len(h.db.recs) != 1 || h.db.recs[1] == nil {
		t.Errorf("recs = %v", h.db.recs)
	}

	// A row without a batch (written before batches existed) stands alone.
	h.db.recs[1].Batch = 0
	h.svc.Up(ctx, "t", 1, nil)
	h.db.recs[2].Batch = 0
	if n, err := h.svc.DownBatch(ctx, "t", nil); err != nil || n != 1 {
		t.Errorf("legacy: n=%d err=%v", n, err)
	}
}

func TestUpTo(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, mig(1, "a"), mig(2, "b"), mig(3, "c"), mig(4, "d"))
	var evs []domain.Event

	// Applies 1..3 inclusive, in order, and leaves 4 pending.
	n, err := h.svc.UpTo(ctx, "t", 3, func(e domain.Event) { evs = append(evs, e) })
	if err != nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if len(h.db.recs) != 3 || h.db.recs[4] != nil {
		t.Errorf("applied = %v", h.db.recs)
	}
	if len(evs) != 6 || evs[0].Version != 1 || evs[4].Version != 3 || evs[5].Phase != domain.Done {
		t.Errorf("events = %+v", evs)
	}
	if h.db.locked || h.db.closed != 1 {
		t.Errorf("locked=%v closed=%d", h.db.locked, h.db.closed)
	}

	// Only what is still pending up to the target is applied.
	h2 := newHarness(t, mig(1, "a"), mig(2, "b"), mig(3, "c"))
	h2.svc.Up(ctx, "t", 1, nil)
	if n, err := h2.svc.UpTo(ctx, "t", 2, nil); err != nil || n != 1 {
		t.Errorf("second run: n=%d err=%v", n, err)
	}

	// Errors: already applied, unknown version, and a failing migration stops the run.
	if _, err := h.svc.UpTo(ctx, "t", 2, nil); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("already applied: err = %v", err)
	}
	if _, err := h.svc.UpTo(ctx, "t", 99, nil); err == nil {
		t.Error("unknown version: expected error")
	}
	h3 := newHarness(t, mig(1, "a"), mig(2, "b"), mig(3, "c"))
	h3.db.failOn = 2
	if n, err := h3.svc.UpTo(ctx, "t", 3, nil); err == nil || n != 1 {
		t.Errorf("failure: n=%d err=%v", n, err)
	}
	if h3.db.recs[3] != nil {
		t.Error("migrations after the failure must not run")
	}
}

func TestDownTo(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, mig(1, "a"), mig(2, "b"), mig(3, "c"), mig(4, "d"))
	h.svc.Up(ctx, "t", 0, nil)
	var evs []domain.Event

	// Reverts 4 then 3, newest first; 2 becomes the latest and stays applied.
	n, err := h.svc.DownTo(ctx, "t", 2, func(e domain.Event) { evs = append(evs, e) })
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if len(h.db.reverted) != 2 || h.db.reverted[0] != 4 || h.db.reverted[1] != 3 {
		t.Errorf("reverted = %v, want [4 3]", h.db.reverted)
	}
	if h.db.recs[2] == nil || h.db.recs[1] == nil || len(h.db.recs) != 2 {
		t.Errorf("recs = %v", h.db.recs)
	}
	if len(evs) != 4 || evs[0].Direction != domain.Down || evs[0].Version != 4 {
		t.Errorf("events = %+v", evs)
	}

	// Already the latest: nothing to do, not an error.
	if n, err := h.svc.DownTo(ctx, "t", 2, nil); err != nil || n != 0 {
		t.Errorf("already latest: n=%d err=%v", n, err)
	}
	// Errors: pending target, unknown version.
	if _, err := h.svc.DownTo(ctx, "t", 3, nil); err == nil || !strings.Contains(err.Error(), "not applied") {
		t.Errorf("pending: err = %v", err)
	}
	if _, err := h.svc.DownTo(ctx, "t", 99, nil); err == nil {
		t.Error("unknown version: expected error")
	}

	// A missing file anywhere in the range aborts before anything is reverted.
	h2 := newHarness(t, mig(1, "a"), mig(2, "b"))
	h2.svc.Up(ctx, "t", 0, nil)
	h2.db.recs[3] = &domain.Record{Version: 3, Name: "ghost", Checksum: "g"} // newest, no file
	h2.db.recs[4] = &domain.Record{Version: 4, Name: "ghost2", Checksum: "g"}
	if _, err := h2.svc.DownTo(ctx, "t", 1, nil); err == nil {
		t.Error("expected error for missing file")
	}
	if len(h2.db.reverted) != 0 {
		t.Errorf("reverted %v before failing on the missing file", h2.db.reverted)
	}
}

func TestRedo(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, mig(1, "a"), mig(2, "b"), mig(3, "c"))
	h.svc.Up(ctx, "t", 0, nil)
	// Migration 2's file was edited after it was applied.
	h.db.recs[2].Checksum = "old"
	h.db.recs[2].AppliedAt = time.Time{}

	var evs []domain.Event
	if err := h.svc.Redo(ctx, "t", 2, func(e domain.Event) { evs = append(evs, e) }); err != nil {
		t.Fatal(err)
	}

	// Reverts then applies, for that migration only; the others are untouched.
	wantEvents := []struct {
		dir   domain.Direction
		phase domain.Phase
	}{{domain.Down, domain.Started}, {domain.Down, domain.Done}, {domain.Up, domain.Started}, {domain.Up, domain.Done}}
	if len(evs) != len(wantEvents) {
		t.Fatalf("events = %+v", evs)
	}
	for i, w := range wantEvents {
		if evs[i].Direction != w.dir || evs[i].Phase != w.phase || evs[i].Version != 2 {
			t.Errorf("event %d = %+v, want %v/%v for version 2", i, evs[i], w.dir, w.phase)
		}
	}
	if len(h.db.reverted) != 1 || h.db.reverted[0] != 2 {
		t.Errorf("reverted = %v, want only [2]", h.db.reverted)
	}
	if r := h.db.recs[2]; r.Checksum != "b" || r.AppliedAt.IsZero() {
		t.Errorf("record not refreshed: %+v", r)
	}
	if len(h.db.recs) != 3 || h.db.locked || h.db.closed != 2 {
		t.Errorf("recs=%d locked=%v closed=%d", len(h.db.recs), h.db.locked, h.db.closed)
	}
	if items, _ := h.svc.Status(ctx, "t"); items[1].State != domain.Applied {
		t.Errorf("state after redo = %s, want applied", items[1].State)
	}
}

func TestRedoRefusals(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, mig(1, "a"), mig(2, "b"))
	h.svc.Up(ctx, "t", 1, nil) // 1 applied, 2 pending
	h.db.recs[9] = &domain.Record{Version: 9, Name: "ghost", Checksum: "g", AppliedAt: time.Now().Add(time.Hour)}

	cases := map[string]int64{"pending": 2, "no such version": 42, "file missing": 9}
	for name, v := range cases {
		reverted := len(h.db.reverted)
		if err := h.svc.Redo(ctx, "t", v, nil); err == nil {
			t.Errorf("%s: expected error", name)
		}
		if len(h.db.reverted) != reverted || h.db.locked {
			t.Errorf("%s: touched the database or leaked the lock", name)
		}
	}

	// A dirty migration blocks redo until it is repaired.
	h.db.recs[1].Dirty = true
	if err := h.svc.Redo(ctx, "t", 1, nil); err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Errorf("dirty: err = %v", err)
	}
	h.db.recs[1].Dirty = false

	if err := h.svc.Redo(ctx, "nope", 1, nil); err == nil {
		t.Error("unknown target: expected error")
	}
	h.svc.SetTargetEnabled("t", false)
	if err := h.svc.Redo(ctx, "t", 1, nil); !errors.Is(err, ErrTargetDisabled) {
		t.Errorf("disabled: err = %v", err)
	}
}

func TestRedoReapplyFailureIsReported(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, mig(1, "a"))
	h.svc.Up(ctx, "t", 0, nil)
	h.db.failOn = 1 // the re-apply will fail

	var last domain.Event
	err := h.svc.Redo(ctx, "t", 1, func(e domain.Event) { last = e })
	if err == nil || !strings.Contains(err.Error(), "now reverted") {
		t.Fatalf("err = %v; it should say the migration was left reverted", err)
	}
	if last.Direction != domain.Up || last.Phase != domain.Failed || last.Err == nil {
		t.Errorf("last event = %+v", last)
	}
	if len(h.db.reverted) != 1 || h.db.locked {
		t.Errorf("reverted=%v locked=%v", h.db.reverted, h.db.locked)
	}
}

func TestRunErrors(t *testing.T) {
	h := newHarness(t, mig(1, "a"))
	h.db.lockErr = errors.New("held")
	if _, err := h.svc.Up(context.Background(), "t", 0, nil); err == nil {
		t.Error("expected lock error")
	}
	if h.db.closed != 1 {
		t.Errorf("connection leaked after lock error: closed=%d", h.db.closed)
	}
	if _, err := h.svc.Up(context.Background(), "nope", 0, nil); err == nil {
		t.Error("expected unknown target error")
	}

	delete(h.store.pw, "t")
	if _, err := h.svc.Status(context.Background(), "t"); err == nil {
		t.Error("expected missing password error")
	}
}

// ---- project configuration ----

func TestTargetLifecycle(t *testing.T) {
	h := newHarness(t)
	svc := h.svc

	if err := svc.AddTarget(domain.Target{Name: " prod ", Driver: "mysql"}, "pw", true); err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.Target("prod"); !ok || h.store.pw["prod"] != "pw" {
		t.Errorf("add: targets=%v pw=%v", svc.Targets(), h.store.pw)
	}
	if err := svc.AddTarget(domain.Target{Name: "prod", Driver: "mysql"}, "", true); err == nil {
		t.Error("expected duplicate name error")
	}
	if err := svc.AddTarget(domain.Target{Name: "x", Driver: "oracle"}, "", true); err == nil {
		t.Error("expected unknown driver error")
	}
	if err := svc.AddTarget(domain.Target{Name: " ", Driver: "mysql"}, "", true); err == nil {
		t.Error("expected empty name error")
	}

	// Update keeps the password when none is given.
	if err := svc.UpdateTarget(domain.Target{Name: "prod", Driver: "mysql", Host: "db"}, "", true); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Target("prod"); got.Host != "db" || h.store.pw["prod"] != "pw" {
		t.Errorf("update: %+v pw=%q", got, h.store.pw["prod"])
	}
	if err := svc.UpdateTarget(domain.Target{Name: "ghost", Driver: "mysql"}, "", true); err == nil {
		t.Error("expected unknown target error")
	}

	// Remove deletes the target and its password, and saves.
	saves := h.store.saves
	if err := svc.RemoveTarget("prod"); err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.Target("prod"); ok {
		t.Error("target still present")
	}
	if _, ok := h.store.pw["prod"]; ok {
		t.Error("password still stored")
	}
	if h.store.saves == saves {
		t.Error("project not saved after removal")
	}
	if err := svc.RemoveTarget("prod"); err == nil {
		t.Error("expected error removing unknown target")
	}
}

func TestPasswords(t *testing.T) {
	h := newHarness(t)
	h.svc.AddTarget(domain.Target{Name: "nopw", Driver: "mysql"}, "", true)

	missing := h.svc.TargetsMissingPassword()
	if len(missing) != 1 || missing[0].Name != "nopw" {
		t.Fatalf("missing = %+v", missing)
	}
	if err := h.svc.SetPassword("nopw", "s", false); err != nil {
		t.Fatal(err)
	}
	if !h.svc.HasPassword("nopw") || len(h.store.pw) != 1 { // session-only: not persisted
		t.Errorf("session password: has=%v persisted=%v", h.svc.HasPassword("nopw"), h.store.pw)
	}
	if err := h.svc.SetPassword("ghost", "s", true); err == nil {
		t.Error("expected unknown target error")
	}
}

func TestDisabledTarget(t *testing.T) {
	h := newHarness(t, mig(1, "a"))
	ctx := context.Background()

	if err := h.svc.SetTargetEnabled("t", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.svc.Target("t"); !got.Disabled || !h.store.project.Targets[0].Disabled {
		t.Errorf("not disabled / not saved: %+v", got)
	}

	// Every operation that would connect refuses, and never opens a connection.
	ops := map[string]func() error{
		"status": func() error { _, err := h.svc.Status(ctx, "t"); return err },
		"up":     func() error { _, err := h.svc.Up(ctx, "t", 0, nil); return err },
		"down":   func() error { _, err := h.svc.Down(ctx, "t", 0, nil); return err },
		"clear":  func() error { return h.svc.ClearDirty(ctx, "t", 1) },
	}
	for name, op := range ops {
		if err := op(); !errors.Is(err, ErrTargetDisabled) {
			t.Errorf("%s: err = %v, want ErrTargetDisabled", name, err)
		}
	}
	if h.db.closed != 0 || len(h.db.recs) != 0 {
		t.Errorf("a disabled target was touched: closed=%d recs=%d", h.db.closed, len(h.db.recs))
	}

	// Disabled targets are not asked for passwords; their settings are kept.
	delete(h.store.pw, "t")
	if got := h.svc.TargetsMissingPassword(); len(got) != 0 {
		t.Errorf("missing = %+v", got)
	}

	// Disabling twice is a no-op; enabling restores normal operation.
	saves := h.store.saves
	if err := h.svc.SetTargetEnabled("t", false); err != nil || h.store.saves != saves {
		t.Errorf("repeat disable: err=%v saves %d->%d", err, saves, h.store.saves)
	}
	h.store.pw["t"] = "secret"
	if err := h.svc.SetTargetEnabled("t", true); err != nil {
		t.Fatal(err)
	}
	if n, err := h.svc.Up(ctx, "t", 0, nil); err != nil || n != 1 {
		t.Errorf("after enable: n=%d err=%v", n, err)
	}
	if err := h.svc.SetTargetEnabled("ghost", true); err == nil {
		t.Error("expected error for unknown target")
	}
}

func TestMigrationsDir(t *testing.T) {
	h := newHarness(t)
	if h.svc.MigrationsDir() != "migrations" {
		t.Errorf("default dir = %q", h.svc.MigrationsDir())
	}
	if err := h.svc.SetMigrationsDir("  db/migs "); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Clean("db/migs"); h.store.project.MigrationsDir != want {
		t.Errorf("saved dir = %q, want %q", h.store.project.MigrationsDir, want)
	}
	if err := h.svc.SetMigrationsDir(" "); err == nil {
		t.Error("expected error for empty dir")
	}
	loc, err := h.svc.CreateMigration(" add_users ")
	if err != nil || h.src.created[0] != h.svc.MigrationsDir()+"/add_users" {
		t.Errorf("create: %q %v %v", loc, err, h.src.created)
	}
}
