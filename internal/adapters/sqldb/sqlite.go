package sqldb

import (
	"context"
	"database/sql"
	"net/url"

	_ "modernc.org/sqlite" // pure Go SQLite driver; registers "sqlite"

	"github.com/puriice/godwit/internal/domain"
)

// sqliteDSN builds a modernc.org/sqlite DSN. The database file is t.Database;
// t.Params are passed through as DSN parameters (for example "_pragma").
func sqliteDSN(t domain.Target) string {
	q := url.Values{}
	for k, v := range t.Params {
		q.Set(k, v)
	}
	// Wait for a concurrent writer instead of failing at once, unless the
	// caller chose their own pragmas.
	if q.Get("_pragma") == "" {
		q.Set("_pragma", "busy_timeout(30000)")
	}
	return "file:" + escapeSQLitePath(t.Database) + "?" + q.Encode()
}

func escapeSQLitePath(p string) string {
	return (&url.URL{Path: p}).EscapedPath()
}

func openSQLite(ctx context.Context, t domain.Target, _ string) (*conn, error) {
	db, err := sql.Open("sqlite", sqliteDSN(t))
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &conn{db: db, d: dialect{
		name:          "sqlite",
		placeholder:   func(int) string { return "?" },
		transactional: true, // SQLite DDL is transactional
		createTable: `CREATE TABLE IF NOT EXISTS ` + Table + ` (
	version     INTEGER   NOT NULL PRIMARY KEY,
	name        TEXT      NOT NULL,
	checksum    TEXT      NOT NULL,
	applied_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	duration_ms INTEGER   NOT NULL DEFAULT 0,
	dirty       BOOLEAN   NOT NULL DEFAULT FALSE,
	batch       INTEGER   NOT NULL DEFAULT 0
)`,
		// SQLite has no advisory locks. The database file is already locked
		// per write, and the version primary key rejects a duplicate apply.
		lock: func(context.Context, *sql.Conn) (func() error, error) {
			return func() error { return nil }, nil
		},
	}}, nil
}
