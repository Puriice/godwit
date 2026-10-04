// Package driver connects godwit to database targets and records migration
// state in each target's godwit_migration table.
package driver

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/puriice/godwit/internal/config"
	"github.com/puriice/godwit/internal/migration"
)

// Table is the name of the state table created in every target.
const Table = "godwit_migration"

// Record is one row of godwit_migration.
type Record struct {
	Version    int64
	Name       string
	Checksum   string
	AppliedAt  time.Time
	DurationMS int64
	Dirty      bool
}

// Conn is an open connection to one target.
type Conn interface {
	Close() error
	// EnsureTable creates godwit_migration if it does not exist.
	EnsureTable(ctx context.Context) error
	// Lock takes a target-wide migration lock; call the returned func to release.
	Lock(ctx context.Context) (unlock func() error, err error)
	Applied(ctx context.Context) ([]Record, error)
	Apply(ctx context.Context, m *migration.Migration) error
	Revert(ctx context.Context, m *migration.Migration) error
	// ClearDirty removes the dirty flag of a version after manual repair.
	ClearDirty(ctx context.Context, version int64) error
}

// Driver opens connections for one database engine.
type Driver interface {
	Name() string
	Open(ctx context.Context, t config.Target, password string) (Conn, error)
}

var registry = map[string]Driver{}

func register(d Driver) { registry[d.Name()] = d }

// Get returns the driver registered under name (case-insensitive).
func Get(name string) (Driver, error) {
	d, ok := registry[strings.ToLower(name)]
	if !ok {
		return nil, fmt.Errorf("unknown driver %q (available: %s)", name, strings.Join(Names(), ", "))
	}
	return d, nil
}

// Names lists registered driver names.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// dialect holds the engine-specific parts of the shared SQL implementation.
type dialect struct {
	name        string
	placeholder func(n int) string // 1-based
	createTable string
	// transactional reports whether DDL can be rolled back, so a migration can
	// be wrapped in a transaction instead of relying on the dirty flag.
	transactional bool
	lock          func(ctx context.Context, c *sql.Conn) (unlock func() error, err error)
}

type conn struct {
	db *sql.DB
	d  dialect
}

func (c *conn) Close() error { return c.db.Close() }

func (c *conn) EnsureTable(ctx context.Context) error {
	_, err := c.db.ExecContext(ctx, c.d.createTable)
	return err
}

func (c *conn) Lock(ctx context.Context) (func() error, error) {
	// Locks are session-scoped, so hold one dedicated connection.
	sc, err := c.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	unlock, err := c.d.lock(ctx, sc)
	if err != nil {
		sc.Close()
		return nil, err
	}
	return func() error {
		err := unlock()
		if cerr := sc.Close(); err == nil {
			err = cerr
		}
		return err
	}, nil
}

func (c *conn) Applied(ctx context.Context) ([]Record, error) {
	rows, err := c.db.QueryContext(ctx,
		"SELECT version, name, checksum, applied_at, duration_ms, dirty FROM "+Table+" ORDER BY version")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.Version, &r.Name, &r.Checksum, &r.AppliedAt, &r.DurationMS, &r.Dirty); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (c *conn) insert(ctx context.Context, e execer, m *migration.Migration, dirty bool, ms int64) error {
	p := c.d.placeholder
	_, err := e.ExecContext(ctx,
		fmt.Sprintf("INSERT INTO %s (version, name, checksum, duration_ms, dirty) VALUES (%s, %s, %s, %s, %s)",
			Table, p(1), p(2), p(3), p(4), p(5)),
		m.Version, m.Name, m.Checksum, ms, dirty)
	return err
}

func (c *conn) setDirty(ctx context.Context, e execer, version int64, dirty bool) error {
	p := c.d.placeholder
	_, err := e.ExecContext(ctx,
		fmt.Sprintf("UPDATE %s SET dirty = %s WHERE version = %s", Table, p(1), p(2)), dirty, version)
	return err
}

func (c *conn) remove(ctx context.Context, e execer, version int64) error {
	_, err := e.ExecContext(ctx,
		fmt.Sprintf("DELETE FROM %s WHERE version = %s", Table, c.d.placeholder(1)), version)
	return err
}

func (c *conn) ClearDirty(ctx context.Context, version int64) error {
	return c.setDirty(ctx, c.db, version, false)
}

func execAll(ctx context.Context, e execer, stmts []string) error {
	for i, s := range stmts {
		if _, err := e.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("statement %d: %w", i+1, err)
		}
	}
	return nil
}

func (c *conn) Apply(ctx context.Context, m *migration.Migration) error {
	stmts := migration.StatementsFor(m.Parsed.Up, c.d.name)
	start := time.Now()

	if c.d.transactional && !m.Parsed.NoTransaction {
		tx, err := c.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback() //nolint:errcheck // no-op after commit
		if err := execAll(ctx, tx, stmts); err != nil {
			return err
		}
		if err := c.insert(ctx, tx, m, false, time.Since(start).Milliseconds()); err != nil {
			return err
		}
		return tx.Commit()
	}

	// No rollback available: record the attempt as dirty first, so a crash or
	// failure part-way through is visible and blocks further migrations.
	if err := c.insert(ctx, c.db, m, true, 0); err != nil {
		return err
	}
	if err := execAll(ctx, c.db, stmts); err != nil {
		return err // row stays dirty
	}
	_, err := c.db.ExecContext(ctx,
		fmt.Sprintf("UPDATE %s SET dirty = %s, duration_ms = %s WHERE version = %s",
			Table, c.d.placeholder(1), c.d.placeholder(2), c.d.placeholder(3)),
		false, time.Since(start).Milliseconds(), m.Version)
	return err
}

func (c *conn) Revert(ctx context.Context, m *migration.Migration) error {
	stmts := migration.StatementsFor(m.Parsed.Down, c.d.name)
	if len(stmts) == 0 {
		return fmt.Errorf("migration %d_%s has no Down statements for %s", m.Version, m.Name, c.d.name)
	}

	if c.d.transactional && !m.Parsed.NoTransaction {
		tx, err := c.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback() //nolint:errcheck
		if err := execAll(ctx, tx, stmts); err != nil {
			return err
		}
		if err := c.remove(ctx, tx, m.Version); err != nil {
			return err
		}
		return tx.Commit()
	}

	if err := c.setDirty(ctx, c.db, m.Version, true); err != nil {
		return err
	}
	if err := execAll(ctx, c.db, stmts); err != nil {
		return err
	}
	return c.remove(ctx, c.db, m.Version)
}
