package app

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
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
	f.recs[m.Version] = &domain.Record{Version: m.Version, Name: m.Name, Checksum: m.Checksum, AppliedAt: time.Now()}
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
	h.db.recs[9] = &domain.Record{Version: 9, Name: "ghost", Checksum: "g"}
	if _, err := h.svc.Down(context.Background(), "t", 0, nil); err == nil {
		t.Error("expected error reverting migration with no file")
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
