package driver

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"strconv"

	_ "github.com/jackc/pgx/v5/stdlib" // registers "pgx"

	"github.com/puriice/godwit/internal/config"
)

// pgLockKey is the advisory lock id ("godwit" as bytes, fits in int64).
const pgLockKey int64 = 0x676f64776974

type postgres struct{}

func init() { register(postgres{}) }

func (postgres) Name() string { return "postgres" }

func postgresDSN(t config.Target, password string) string {
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

func (postgres) Open(ctx context.Context, t config.Target, password string) (Conn, error) {
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
		createTable: `CREATE TABLE IF NOT EXISTS ` + Table + ` (
	version     BIGINT      PRIMARY KEY,
	name        TEXT        NOT NULL,
	checksum    CHAR(64)    NOT NULL,
	applied_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
	duration_ms BIGINT      NOT NULL DEFAULT 0,
	dirty       BOOLEAN     NOT NULL DEFAULT FALSE
)`,
		lock: func(ctx context.Context, c *sql.Conn) (func() error, error) {
			if _, err := c.ExecContext(ctx, "SELECT pg_advisory_lock($1)", pgLockKey); err != nil {
				return nil, err
			}
			return func() error {
				_, err := c.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", pgLockKey)
				return err
			}, nil
		},
	}}, nil
}
