// Package sqldb is the driven adapter that talks to real databases. It
// implements app.DatabaseFactory for PostgreSQL, MySQL/MariaDB and SQLite and records
// migration state in each target's godwit_migration table.
package sqldb

import (
	"context"
	"database/sql"
	"errors"
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
	if _, err := c.db.ExecContext(ctx, c.d.createTable); err != nil {
		return err
	}
	// Tables created before batches existed lack the column; add it with 0
	// ("unknown") for the old rows.
	rows, err := c.db.QueryContext(ctx, "SELECT batch FROM "+Table+" WHERE 1 = 0")
	if err == nil {
		return rows.Close()
	}
	_, err = c.db.ExecContext(ctx, "ALTER TABLE "+Table+" ADD COLUMN batch BIGINT NOT NULL DEFAULT 0")
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
		"SELECT version, name, checksum, applied_at, duration_ms, dirty, batch FROM "+Table+" ORDER BY version")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Record
	for rows.Next() {
		var r domain.Record
		if err := rows.Scan(&r.Version, &r.Name, &r.Checksum, &r.AppliedAt, &r.DurationMS, &r.Dirty, &r.Batch); err != nil {
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
		fmt.Sprintf("INSERT INTO %s (version, name, checksum, duration_ms, dirty, batch) VALUES (%s, %s, %s, %s, %s, %s)",
			Table, p(1), p(2), p(3), p(4), p(5), p(6)),
		m.Version, m.Name, m.Checksum, ms, dirty, m.Batch)
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

// maxRepeat caps the iterations of one repeat block, so a body or condition
// that never converges ends in an error instead of looping forever. A variable
// so tests can lower it.
var maxRepeat = 10_000_000

// runner is what both *sql.DB and *sql.Tx offer.
type runner interface {
	execer
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// truthy reads the first value of a RepeatCondition query as a boolean: NULL,
// false, 0 and the text "", "0", "false" and "f" are false; anything else is
// true.
func truthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case bool:
		return v
	case int64:
		return v != 0
	case float64:
		return v != 0
	case []byte:
		return truthy(string(v))
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "", "0", "false", "f":
			return false
		}
	}
	return true
}

// iterate runs one pass of a repeat step on r and reports whether another
// should follow. With a condition it is checked first (a while loop); without
// one the pass runs and the loop ends once it affects no rows.
func iterate(ctx context.Context, r runner, st domain.Step) (more bool, err error) {
	if st.Condition != "" {
		var v any
		switch err := r.QueryRowContext(ctx, st.Condition).Scan(&v); {
		case errors.Is(err, sql.ErrNoRows):
			return false, nil
		case err != nil:
			return false, fmt.Errorf("condition: %w", err)
		}
		if !truthy(v) {
			return false, nil
		}
	}
	var affected int64
	for i, s := range st.SQL {
		res, err := r.ExecContext(ctx, s)
		if err != nil {
			return false, fmt.Errorf("statement %d: %w", i+1, err)
		}
		if n, err := res.RowsAffected(); err == nil {
			affected += n
		}
	}
	return st.Condition != "" || affected > 0, nil
}

// repeat runs a repeat step until it is done. Where DDL is transactional every
// iteration is its own transaction, so locks stay short and finished
// iterations survive a later failure.
func (c *conn) repeat(ctx context.Context, st domain.Step) error {
	for n := 1; ; n++ {
		if n > maxRepeat {
			return fmt.Errorf("stopped after %d iterations; the block never finished (check its WHERE clause or add a RepeatCondition)", maxRepeat)
		}
		more, err := c.pass(ctx, st)
		if err != nil {
			return fmt.Errorf("iteration %d: %w", n, err)
		}
		if !more {
			return nil
		}
		if err := sleep(ctx, st.Delay); err != nil {
			return fmt.Errorf("iteration %d: %w", n, err)
		}
	}
}

// sleep waits for d between two passes, which run in separate transactions, so
// nothing is held open meanwhile. It returns early if ctx is cancelled.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *conn) pass(ctx context.Context, st domain.Step) (bool, error) {
	if !c.d.transactional {
		return iterate(ctx, c.db, st)
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	more, err := iterate(ctx, tx, st)
	if err != nil {
		return false, err
	}
	return more, tx.Commit()
}

// runSteps runs steps outside any migration-wide transaction: plain statements
// as they come, repeat blocks one transaction per iteration.
func (c *conn) runSteps(ctx context.Context, steps []domain.Step) error {
	blocks := 0
	for i, st := range steps {
		if !st.Repeat {
			if _, err := c.db.ExecContext(ctx, st.SQL[0]); err != nil {
				return fmt.Errorf("statement %d: %w", i+1, err)
			}
			continue
		}
		blocks++
		if err := c.repeat(ctx, st); err != nil {
			return fmt.Errorf("repeat block %d (statement %d): %w", blocks, i+1, err)
		}
	}
	return nil
}

func (c *conn) Apply(ctx context.Context, m *domain.Migration) error {
	stmts := m.UpSQL(c.d.name)
	start := time.Now()

	if c.d.transactional && !m.NoTransaction && !m.UpHasRepeat() {
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
	if err := c.runSteps(ctx, m.UpSteps(c.d.name)); err != nil {
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

	if c.d.transactional && !m.NoTransaction && !m.DownHasRepeat() {
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
	if err := c.runSteps(ctx, m.DownSteps(c.d.name)); err != nil {
		return err
	}
	return c.remove(ctx, c.db, m.Version)
}
