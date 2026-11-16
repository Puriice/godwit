package sqldb

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"net"
	"net/url"
	"strconv"

	_ "github.com/jackc/pgx/v5/stdlib" // registers "pgx"

	"github.com/puriice/godwit/internal/domain"
)

// pgLockKey is the advisory lock id of the default state table ("godwit" as
// bytes, fits in int64).
const pgLockKey int64 = 0x676f64776974

// pgLockKeyFor returns the advisory lock id guarding table. The default table
// keeps pgLockKey, so runs of older godwit versions still exclude each other;
// other tables (one per workspace service) get their own id, so services that
// share a database do not wait for one another.
func pgLockKeyFor(table string) int64 {
	if table == Table {
		return pgLockKey
	}
	h := fnv.New64a()
	h.Write([]byte(table)) //nolint:errcheck // never fails
	return int64(h.Sum64())
}

func postgresDSN(t domain.Target, password string) string {
	port := t.Port
	if port == 0 {
		port = 5432
	}
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(t.User, password),
		Host:   net.JoinHostPort(t.Host, strconv.Itoa(port)),
		Path:   "/" + t.Database,
	}
	q := u.Query()
	for k, v := range t.Params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func openPostgres(ctx context.Context, t domain.Target, password string) (*conn, error) {
	db, err := sql.Open("pgx", postgresDSN(t, password))
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &conn{db: db, d: dialect{
		name:          "postgres",
		placeholder:   func(n int) string { return fmt.Sprintf("$%d", n) },
		transactional: true,
		createTable: func(table string) string {
			return `CREATE TABLE IF NOT EXISTS ` + table + ` (
	version     BIGINT      PRIMARY KEY,
	name        TEXT        NOT NULL,
	checksum    CHAR(64)    NOT NULL,
	applied_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
	duration_ms BIGINT      NOT NULL DEFAULT 0,
	dirty       BOOLEAN     NOT NULL DEFAULT FALSE,
	batch       BIGINT      NOT NULL DEFAULT 0
)`
		},
		lock: func(ctx context.Context, c *sql.Conn, table string) (func() error, error) {
			key := pgLockKeyFor(table)
			if _, err := c.ExecContext(ctx, "SELECT pg_advisory_lock($1)", key); err != nil {
				return nil, err
			}
			return func() error {
				_, err := c.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", key)
				return err
			}, nil
		},
	}}, nil
}
