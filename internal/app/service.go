package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/puriice/godwit/internal/domain"
)

const defaultMigrationsDir = "migrations"

// ErrTargetDisabled is returned when an operation needs a disabled target.
var ErrTargetDisabled = errors.New("target is disabled")

// Progress receives run events. It may be nil.
type Progress func(domain.Event)

func (p Progress) emit(e domain.Event) {
	if p != nil {
		p(e)
	}
}

// Service implements godwit's use cases. It is safe for concurrent use.
type Service struct {
	store  ProjectStore
	source MigrationSource
	dbs    DatabaseFactory

	mu      sync.RWMutex
	project domain.Project
}

// New loads the project from store and returns a Service.
func New(store ProjectStore, source MigrationSource, dbs DatabaseFactory) (*Service, error) {
	p, err := store.Load()
	if err != nil {
		return nil, err
	}
	if p.MigrationsDir == "" {
		p.MigrationsDir = defaultMigrationsDir
	}
	return &Service{store: store, source: source, dbs: dbs, project: p}, nil
}

// ---- Project configuration ----

// Drivers lists the supported database drivers.
func (s *Service) Drivers() []string { return s.dbs.Drivers() }

// Targets returns a copy of the project's targets.
func (s *Service) Targets() []domain.Target {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.project.Targets)
}

// Target returns the named target.
func (s *Service) Target(name string) (domain.Target, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.project.Target(name)
}

// MigrationsDir returns the configured migrations directory as written in
// the project config.
func (s *Service) MigrationsDir() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.project.MigrationsDir
}

// MigrationsLocation returns the migrations directory as an absolute path.
func (s *Service) MigrationsLocation() string { return s.source.Resolve(s.MigrationsDir()) }

// SetMigrationsDir changes the migrations directory and saves the project.
func (s *Service) SetMigrationsDir(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return errors.New("migrations directory is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.project.MigrationsDir = filepath.Clean(dir)
	return s.store.Save(s.project)
}

// EnsureMigrationsDir creates the migrations directory if needed.
func (s *Service) EnsureMigrationsDir() error { return s.source.EnsureDir(s.MigrationsDir()) }

func (s *Service) validate(t domain.Target) error {
	switch {
	case strings.TrimSpace(t.Name) == "":
		return errors.New("target name is required")
	case !slices.Contains(s.dbs.Drivers(), strings.ToLower(t.Driver)):
		return fmt.Errorf("unknown driver %q (available: %s)", t.Driver, strings.Join(s.dbs.Drivers(), ", "))
	}
	return nil
}

// AddTarget adds a target and saves the project. A non-empty password is
// remembered (on disk if persist).
func (s *Service) AddTarget(t domain.Target, password string, persist bool) error {
	t.Name = strings.TrimSpace(t.Name)
	if err := s.validate(t); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.project.Target(t.Name); dup {
		return fmt.Errorf("a target named %q already exists", t.Name)
	}
	s.project.Targets = append(s.project.Targets, t)
	if err := s.store.Save(s.project); err != nil {
		return err
	}
	if password != "" {
		return s.store.SetPassword(t.Name, password, persist)
	}
	return nil
}

// UpdateTarget replaces the target with the same name. An empty password
// keeps the current one.
func (s *Service) UpdateTarget(t domain.Target, password string, persist bool) error {
	if err := s.validate(t); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.project.Targets, func(x domain.Target) bool { return x.Name == t.Name })
	if i < 0 {
		return fmt.Errorf("no target named %q", t.Name)
	}
	s.project.Targets[i] = t
	if err := s.store.Save(s.project); err != nil {
		return err
	}
	if password != "" {
		return s.store.SetPassword(t.Name, password, persist)
	}
	return nil
}

// RemoveTarget deletes a target and its stored password.
func (s *Service) RemoveTarget(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.project.Targets, func(x domain.Target) bool { return x.Name == name })
	if i < 0 {
		return fmt.Errorf("no target named %q", name)
	}
	s.project.Targets = slices.Delete(s.project.Targets, i, i+1)
	if err := s.store.DeletePassword(name); err != nil {
		return err
	}
	return s.store.Save(s.project)
}

// SetTargetEnabled enables or disables a target and saves the project. A
// disabled target keeps its settings and password but is never connected to.
func (s *Service) SetTargetEnabled(name string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.project.Targets, func(x domain.Target) bool { return x.Name == name })
	if i < 0 {
		return fmt.Errorf("no target named %q", name)
	}
	if s.project.Targets[i].Disabled == !enabled {
		return nil // already in the requested state
	}
	s.project.Targets[i].Disabled = !enabled
	return s.store.Save(s.project)
}

// HasPassword reports whether a password is available for the target.
func (s *Service) HasPassword(name string) bool {
	_, ok := s.store.Password(name)
	return ok
}

// TargetsMissingPassword lists enabled targets with no available password.
// Disabled targets are never connected to, so they are not asked for one.
func (s *Service) TargetsMissingPassword() []domain.Target {
	var out []domain.Target
	for _, t := range s.Targets() {
		if !t.Disabled && !s.HasPassword(t.Name) {
			out = append(out, t)
		}
	}
	return out
}

// SetPassword remembers a target's password (on disk if persist).
func (s *Service) SetPassword(name, password string, persist bool) error {
	if _, ok := s.Target(name); !ok {
		return fmt.Errorf("no target named %q", name)
	}
	return s.store.SetPassword(name, password, persist)
}

// ---- Migration files ----

// Migrations loads every migration definition, sorted by version.
func (s *Service) Migrations() ([]*domain.Migration, error) {
	return s.source.Load(s.MigrationsDir())
}

// CreateMigration scaffolds a new migration and returns its location.
func (s *Service) CreateMigration(name string) (string, error) {
	return s.source.Create(s.MigrationsDir(), strings.TrimSpace(name))
}

// ---- Running migrations ----

func (s *Service) open(ctx context.Context, target string) (Database, domain.Target, error) {
	t, ok := s.Target(target)
	if !ok {
		return nil, t, fmt.Errorf("no target named %q", target)
	}
	if t.Disabled {
		return nil, t, fmt.Errorf("%q: %w (enable it with: godwit auth enable %s)", target, ErrTargetDisabled, target)
	}
	pw, ok := s.store.Password(target)
	if !ok {
		return nil, t, fmt.Errorf("no password for target %q", target)
	}
	db, err := s.dbs.Open(ctx, t, pw)
	return db, t, err
}

// Status lists every known migration with its state on the target. It creates
// godwit_migration if the target does not have it yet.
func (s *Service) Status(ctx context.Context, target string) ([]domain.Item, error) {
	migs, err := s.Migrations()
	if err != nil {
		return nil, err
	}
	db, _, err := s.open(ctx, target)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := db.EnsureTable(ctx); err != nil {
		return nil, err
	}
	recs, err := db.Applied(ctx)
	if err != nil {
		return nil, err
	}
	return merge(migs, recs), nil
}

func merge(migs []*domain.Migration, recs []domain.Record) []domain.Item {
	byVer := make(map[int64]*domain.Record, len(recs))
	for i := range recs {
		byVer[recs[i].Version] = &recs[i]
	}
	var items []domain.Item
	for _, m := range migs {
		it := domain.Item{Version: m.Version, Name: m.Name, Migration: m, State: domain.Pending}
		if r, ok := byVer[m.Version]; ok {
			it.Record = r
			switch {
			case r.Dirty:
				it.State = domain.Dirty
			case r.Checksum != m.Checksum:
				it.State = domain.Modified
			default:
				it.State = domain.Applied
			}
			delete(byVer, m.Version)
		}
		items = append(items, it)
	}
	for _, r := range byVer {
		it := domain.Item{Version: r.Version, Name: r.Name, Record: r, State: domain.Missing}
		if r.Dirty {
			it.State = domain.Dirty
		}
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Version < items[j].Version })
	return items
}

// lockedStatus opens the target, takes its lock and returns the current
// status. It refuses to continue while any migration is dirty. release
// unlocks and closes; call it exactly once.
func (s *Service) lockedStatus(ctx context.Context, target string) (db Database, items []domain.Item, release func() error, err error) {
	migs, err := s.Migrations()
	if err != nil {
		return nil, nil, nil, err
	}
	db, _, err = s.open(ctx, target)
	if err != nil {
		return nil, nil, nil, err
	}
	fail := func(err error) (Database, []domain.Item, func() error, error) {
		db.Close()
		return nil, nil, nil, err
	}
	if err := db.EnsureTable(ctx); err != nil {
		return fail(err)
	}
	unlock, err := db.Lock(ctx)
	if err != nil {
		return fail(err)
	}
	release = func() error {
		err := unlock()
		if cerr := db.Close(); err == nil {
			err = cerr
		}
		return err
	}
	recs, err := db.Applied(ctx)
	if err != nil {
		release() //nolint:errcheck
		return nil, nil, nil, err
	}
	items = merge(migs, recs)
	for _, it := range items {
		if it.State == domain.Dirty {
			release() //nolint:errcheck
			return nil, nil, nil, fmt.Errorf("migration %d_%s is dirty: a previous run failed part-way; repair the target, then clear the dirty flag", it.Version, it.Name)
		}
	}
	return db, items, release, nil
}

// Up applies pending migrations in version order. limit <= 0 applies all of
// them. It returns how many were applied.
func (s *Service) Up(ctx context.Context, target string, limit int, prog Progress) (applied int, err error) {
	db, items, release, err := s.lockedStatus(ctx, target)
	if err != nil {
		return 0, err
	}
	defer func() {
		if rerr := release(); err == nil {
			err = rerr
		}
	}()

	for _, it := range items {
		if it.State != domain.Pending {
			continue
		}
		if limit > 0 && applied >= limit {
			break
		}
		if err := ctx.Err(); err != nil {
			return applied, err
		}
		ev := domain.Event{Direction: domain.Up, Version: it.Version, Name: it.Name, Phase: domain.Started}
		prog.emit(ev)
		if err := db.Apply(ctx, it.Migration); err != nil {
			ev.Phase, ev.Err = domain.Failed, err
			prog.emit(ev)
			return applied, fmt.Errorf("%d_%s: %w", it.Version, it.Name, err)
		}
		applied++
		ev.Phase = domain.Done
		prog.emit(ev)
	}
	return applied, nil
}

// Down reverts the n most recently applied migrations (n <= 0 means 1). It
// returns how many were reverted.
func (s *Service) Down(ctx context.Context, target string, n int, prog Progress) (reverted int, err error) {
	if n <= 0 {
		n = 1
	}
	db, items, release, err := s.lockedStatus(ctx, target)
	if err != nil {
		return 0, err
	}
	defer func() {
		if rerr := release(); err == nil {
			err = rerr
		}
	}()

	for i := len(items) - 1; i >= 0 && reverted < n; i-- {
		it := items[i]
		if it.Record == nil { // pending, nothing to revert
			continue
		}
		if it.Migration == nil {
			return reverted, fmt.Errorf("cannot revert %d_%s: migration file is missing", it.Version, it.Name)
		}
		if err := ctx.Err(); err != nil {
			return reverted, err
		}
		ev := domain.Event{Direction: domain.Down, Version: it.Version, Name: it.Name, Phase: domain.Started}
		prog.emit(ev)
		if err := db.Revert(ctx, it.Migration); err != nil {
			ev.Phase, ev.Err = domain.Failed, err
			prog.emit(ev)
			return reverted, fmt.Errorf("%d_%s: %w", it.Version, it.Name, err)
		}
		reverted++
		ev.Phase = domain.Done
		prog.emit(ev)
	}
	return reverted, nil
}

// ClearDirty clears a dirty flag after the user repaired the target by hand.
func (s *Service) ClearDirty(ctx context.Context, target string, version int64) error {
	db, _, err := s.open(ctx, target)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.ClearDirty(ctx, version)
}
