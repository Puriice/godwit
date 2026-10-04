package driver

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strconv"

	mysqldrv "github.com/go-sql-driver/mysql"

	"github.com/puriice/godwit/internal/config"
)

const mysqlLockName = "godwit_migration_lock"

type mysql struct{}

func init() { register(mysql{}) }

func (mysql) Name() string { return "mysql" }

func mysqlDSN(t config.Target, password string) string {
	port := t.Port
	if port == 0 {
		port = 3306
	}
	cfg := mysqldrv.NewConfig()
	cfg.User = t.User
	cfg.Passwd = password
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(t.Host, strconv.Itoa(port))
	cfg.DBName = t.Database
	cfg.ParseTime = true
	cfg.Params = t.Params
	return cfg.FormatDSN()
}

func (mysql) Open(ctx context.Context, t config.Target, password string) (Conn, error) {
	db, err := sql.Open("mysql", mysqlDSN(t, password))
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &conn{db: db, d: dialect{
		name:        "mysql",
		placeholder: func(int) string { return "?" },
		// MySQL DDL auto-commits, so migrations rely on the dirty flag.
		transactional: false,
		createTable: `CREATE TABLE IF NOT EXISTS ` + Table + ` (
	version     BIGINT       NOT NULL PRIMARY KEY,
	name        VARCHAR(255) NOT NULL,
	checksum    CHAR(64)     NOT NULL,
	applied_at  TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
	duration_ms BIGINT       NOT NULL DEFAULT 0,
	dirty       BOOLEAN      NOT NULL DEFAULT FALSE
)`,
		lock: func(ctx context.Context, c *sql.Conn) (func() error, error) {
			var got sql.NullInt64
			// Wait up to 30s for a concurrent run to finish.
			if err := c.QueryRowContext(ctx, "SELECT GET_LOCK(?, 30)", mysqlLockName).Scan(&got); err != nil {
				return nil, err
			}
			if !got.Valid || got.Int64 != 1 {
				return nil, fmt.Errorf("another godwit run holds the migration lock")
			}
			return func() error {
				_, err := c.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", mysqlLockName)
				return err
			}, nil
		},
	}}, nil
}
