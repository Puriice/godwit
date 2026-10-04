// Package runner plans and executes migrations against one target. It is the
// only layer the TUI talks to; it never exposes SQL.
package runner

import (
	"context"
	"fmt"
	"sort"

	"github.com/puriice/godwit/internal/config"
	"github.com/puriice/godwit/internal/driver"
	"github.com/puriice/godwit/internal/migration"
)

// State describes a migration relative to a target.
type State string

const (
	Pending  State = "pending"
	Applied  State = "applied"
	Modified State = "modified" // applied, but the file's checksum changed
	Dirty    State = "dirty"    // a previous run failed part-way
	Missing  State = "missing"  // recorded in the target, no file on disk
)

// Item is one row of a target's migration status.
type Item struct {
	Version   int64
	Name      string
	State     State
	Migration *migration.Migration // nil when Missing
	Record    *driver.Record       // nil when Pending
}

// Open connects to a target using the project's resolved password.
func Open(ctx context.Context, p *config.Project, t config.Target) (driver.Conn, error) {
	d, err := driver.Get(t.Driver)
	if err != nil {
		return nil, err
	}
	pw, ok := p.Password(t)
	if !ok {
		return nil, fmt.Errorf("no password for target %q (%s)", t.Name, config.PasswordKey(t.Name))
	}
	return d.Open(ctx, t, pw)
}

// Status lists every known migration with its state on the target. It creates
// godwit_migration if the target does not have it yet.
func Status(ctx context.Context, c driver.Conn, migs []*migration.Migration) ([]Item, error) {
	if err := c.EnsureTable(ctx); err != nil {
		return nil, err
	}
	recs, err := c.Applied(ctx)
	if err != nil {
		return nil, err
	}
	return merge(migs, recs), nil
}

func merge(migs []*migration.Migration, recs []driver.Record) []Item {
	byVer := make(map[int64]*driver.Record, len(recs))
	for i := range recs {
		byVer[recs[i].Version] = &recs[i]
	}
	var items []Item
	for _, m := range migs {
		it := Item{Version: m.Version, Name: m.Name, Migration: m, State: Pending}
		if r, ok := byVer[m.Version]; ok {
			it.Record = r
			switch {
			case r.Dirty:
				it.State = Dirty
			case r.Checksum != m.Checksum:
				it.State = Modified
			default:
				it.State = Applied
			}
			delete(byVer, m.Version)
		}
		items = append(items, it)
	}
	for _, r := range byVer {
		items = append(items, Item{Version: r.Version, Name: r.Name, Record: r, State: Missing})
		if r.Dirty {
			items[len(items)-1].State = Dirty
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Version < items[j].Version })
	return items
}

// Direction of a migration run.
type Direction string

const (
	Up   Direction = "up"
	Down Direction = "down"
)

// Phase of a single migration within a run.
type Phase string

const (
	Started Phase = "started"
	Done    Phase = "done"
	Failed  Phase = "failed"
)

// Event reports progress; the TUI turns these into messages.
type Event struct {
	Direction Direction
	Version   int64
	Name      string
	Phase     Phase
	Err       error
}

// Progress receives events. It may be nil.
type Progress func(Event)

func (p Progress) emit(e Event) {
	if p != nil {
		p(e)
	}
}

// lockedStatus takes the target lock and returns current status plus unlock.
// It refuses to continue while any migration is dirty.
func lockedStatus(ctx context.Context, c driver.Conn, migs []*migration.Migration) ([]Item, func() error, error) {
	if err := c.EnsureTable(ctx); err != nil {
		return nil, nil, err
	}
	unlock, err := c.Lock(ctx)
	if err != nil {
		return nil, nil, err
	}
	recs, err := c.Applied(ctx)
	if err != nil {
		unlock() //nolint:errcheck
		return nil, nil, err
	}
	items := merge(migs, recs)
	for _, it := range items {
		if it.State == Dirty {
			unlock() //nolint:errcheck
			return nil, nil, fmt.Errorf("migration %d_%s is dirty: a previous run failed part-way; repair the target, then clear the dirty flag", it.Version, it.Name)
		}
	}
	return items, unlock, nil
}

// MigrateUp applies pending migrations in version order. limit <= 0 applies
// all of them. It returns how many were applied.
func MigrateUp(ctx context.Context, c driver.Conn, migs []*migration.Migration, limit int, prog Progress) (applied int, err error) {
	items, unlock, err := lockedStatus(ctx, c, migs)
	if err != nil {
		return 0, err
	}
	defer func() {
		if uerr := unlock(); err == nil {
			err = uerr
		}
	}()

	for _, it := range items {
		if it.State != Pending {
			continue
		}
		if limit > 0 && applied >= limit {
			break
		}
		if err := ctx.Err(); err != nil {
			return applied, err
		}
		ev := Event{Direction: Up, Version: it.Version, Name: it.Name}
		ev.Phase = Started
		prog.emit(ev)
		if err := c.Apply(ctx, it.Migration); err != nil {
			ev.Phase, ev.Err = Failed, err
			prog.emit(ev)
			return applied, fmt.Errorf("%d_%s: %w", it.Version, it.Name, err)
		}
		applied++
		ev.Phase = Done
		prog.emit(ev)
	}
	return applied, nil
}

// MigrateDown reverts the n most recently applied migrations (n <= 0 means 1).
// It returns how many were reverted.
func MigrateDown(ctx context.Context, c driver.Conn, migs []*migration.Migration, n int, prog Progress) (reverted int, err error) {
	if n <= 0 {
		n = 1
	}
	items, unlock, err := lockedStatus(ctx, c, migs)
	if err != nil {
		return 0, err
	}
	defer func() {
		if uerr := unlock(); err == nil {
			err = uerr
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
		ev := Event{Direction: Down, Version: it.Version, Name: it.Name, Phase: Started}
		prog.emit(ev)
		if err := c.Revert(ctx, it.Migration); err != nil {
			ev.Phase, ev.Err = Failed, err
			prog.emit(ev)
			return reverted, fmt.Errorf("%d_%s: %w", it.Version, it.Name, err)
		}
		reverted++
		ev.Phase = Done
		prog.emit(ev)
	}
	return reverted, nil
}

// ClearDirty clears a dirty flag after the user repaired the target by hand.
func ClearDirty(ctx context.Context, c driver.Conn, version int64) error {
	return c.ClearDirty(ctx, version)
}
