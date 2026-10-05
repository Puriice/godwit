// Package sqldb is the driven adapter that talks to real databases. It
// implements app.DatabaseFactory for PostgreSQL, MySQL/MariaDB and SQLite and records
// migration state in each target's godwit_migration table.
package sqldb

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

// Table is the name of the state table created in every target.
const Table = "godwit_migration"

// opener opens a connection for one database engine.
type opener func(ctx context.Context, t domain.Target, password string) (*conn, error)

// Factory implements app.DatabaseFactory.
type Factory struct {
	openers map[string]opener
}

// NewFactory returns a Factory supporting every built-in driver.
func NewFactory() *Factory {
	return &Factory{openers: map[string]opener{
		"postgres": openPostgres,
		"mysql":    openMySQL,
		"sqlite":   openSQLite,
	}}
}

var _ app.DatabaseFactory = (*Factory)(nil)

// Drivers lists the supported driver names, sorted.
func (f *Factory) Drivers() []string {
	names := make([]string, 0, len(f.openers))
	for n := range f.openers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Open connects to a target using the driver named by t.Driver
// (case-insensitive).
func (f *Factory) Open(ctx context.Context, t domain.Target, password string) (app.Database, error) {
	open, ok := f.openers[strings.ToLower(t.Driver)]
	if !ok {
		return nil, fmt.Errorf("unknown driver %q (available: %s)", t.Driver, strings.Join(f.Drivers(), ", "))
	}
	c, err := open(ctx, t, password)
	if err != nil {
		return nil, err
	}
	return c, nil
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

func (c *conn) Applied(ctx context.Context) ([]domain.Record, error) {
	rows, err := c.db.QueryContext(ctx,
		"SELECT version, name, checksum, applied_at, duration_ms, dirty FROM "+Table+" ORDER BY version")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Record
	for rows.Next() {
		var r domain.Record
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

func (c *conn) insert(ctx context.Context, e execer, m *domain.Migration, dirty bool, ms int64) error {
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

func (c *conn) Apply(ctx context.Context, m *domain.Migration) error {
	stmts := m.UpSQL(c.d.name)
	start := time.Now()

	if c.d.transactional && !m.NoTransaction {
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

func (c *conn) Revert(ctx context.Context, m *domain.Migration) error {
	stmts := m.DownSQL(c.d.name)
	if len(stmts) == 0 {
		return fmt.Errorf("migration %d_%s has no Down statements for %s", m.Version, m.Name, c.d.name)
	}

	if c.d.transactional && !m.NoTransaction {
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
