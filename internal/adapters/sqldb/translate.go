package sqldb

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

var _ app.ForeignImporter = (*conn)(nil)

// identRe is a table name, optionally schema-qualified.
var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)

// quoteTable quotes each part of a validated table name for the dialect.
func (c *conn) quoteTable(table string) (string, error) {
	if !identRe.MatchString(table) {
		return "", fmt.Errorf("invalid table name %q (use letters, digits and _, optionally schema.table)", table)
	}
	q := `"`
	if c.d.name == "mysql" {
		q = "`"
	}
	parts := strings.Split(table, ".")
	for i, p := range parts {
		parts[i] = q + p + q
	}
	return strings.Join(parts, "."), nil
}

// tableExists reports whether the (validated) table exists.
func (c *conn) tableExists(ctx context.Context, table, quoted string) (bool, error) {
	schema, name := "", table
	if s, n, ok := strings.Cut(table, "."); ok {
		schema, name = s, n
	}
	var n int
	var err error
	switch c.d.name {
	case "postgres":
		err = c.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM (SELECT to_regclass($1) AS r) t WHERE r IS NOT NULL", quoted).Scan(&n)
	case "mysql":
		var sch any
		if schema != "" {
			sch = schema
		}
		err = c.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = COALESCE(?, DATABASE()) AND table_name = ?",
			sch, name).Scan(&n)
	default: // sqlite
		master := "sqlite_master"
		if schema != "" {
			master = `"` + schema + `".sqlite_master`
		}
		err = c.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM "+master+" WHERE type = 'table' AND name = ?", name).Scan(&n)
	}
	return n > 0, err
}

// ForeignApplied reads another tool's version table. Only goose is known.
func (c *conn) ForeignApplied(ctx context.Context, tool, table string) ([]domain.ForeignRow, bool, error) {
	if tool != "goose" {
		return nil, false, fmt.Errorf("unknown tool %q", tool)
	}
	quoted, err := c.quoteTable(table)
	if err != nil {
		return nil, false, err
	}
	ok, err := c.tableExists(ctx, table, quoted)
	if err != nil || !ok {
		return nil, false, err
	}
	rows, err := c.db.QueryContext(ctx, "SELECT version_id, is_applied, tstamp FROM "+quoted+" ORDER BY id")
	if err != nil {
		return nil, true, err
	}
	defer rows.Close()
	var out []domain.ForeignRow
	for rows.Next() {
		var r domain.ForeignRow
		var at *time.Time
		if err := rows.Scan(&r.Version, &r.Applied, &at); err != nil {
			return nil, true, err
		}
		if at != nil {
			r.At = *at
		}
		out = append(out, r)
	}
	return out, true, rows.Err()
}

// ImportRecords adds records for versions that have no row yet, in one
// transaction, keeping each AppliedAt.
func (c *conn) ImportRecords(ctx context.Context, recs []domain.Record) (int, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	p := c.d.placeholder
	added := 0
	for _, r := range recs {
		var n int
		err := tx.QueryRowContext(ctx,
			fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE version = %s", Table, p(1)), r.Version).Scan(&n)
		if err != nil {
			return 0, err
		}
		if n > 0 {
			continue
		}
		at := r.AppliedAt
		if at.IsZero() {
			at = time.Now().UTC()
		}
		_, err = tx.ExecContext(ctx,
			fmt.Sprintf("INSERT INTO %s (version, name, checksum, applied_at, duration_ms, dirty, batch) VALUES (%s, %s, %s, %s, %s, %s, %s)",
				Table, p(1), p(2), p(3), p(4), p(5), p(6), p(7)),
			r.Version, r.Name, r.Checksum, at, r.DurationMS, false, r.Batch)
		if err != nil {
			return 0, err
		}
		added++
	}
	return added, tx.Commit()
}
