package runner

import (
	"context"
	"net"
	"net/url"
	"strconv"
	"strings"
	"testing"

	mysqldrv "github.com/go-sql-driver/mysql"

	"github.com/puriice/godwit/internal/config"
	"github.com/puriice/godwit/internal/driver"
	"github.com/puriice/godwit/internal/migration"
)

// Integration tests run against real databases and are skipped unless
// GODWIT_TEST_POSTGRES / GODWIT_TEST_MYSQL are set (see docker-compose.yml).

func pgTarget(t *testing.T, raw string) (config.Target, string) {
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	pw, _ := u.User.Password()
	return config.Target{Name: "pg", Driver: "postgres", Host: u.Hostname(), Port: port,
		Database: strings.TrimPrefix(u.Path, "/"), User: u.User.Username()}, pw
}

func myTarget(t *testing.T, dsn string) (config.Target, string) {
	cfg, err := mysqldrv.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	host, p, _ := net.SplitHostPort(cfg.Addr)
	port, _ := strconv.Atoi(p)
	return config.Target{Name: "my", Driver: "mysql", Host: host, Port: port,
		Database: cfg.DBName, User: cfg.User}, cfg.Passwd
}

const integrationMigration = `-- +godwit Up
-- +godwit driver: all
CREATE TABLE godwit_it_users (id BIGINT PRIMARY KEY);

-- +godwit driver: postgres
CREATE TABLE godwit_it_pg_only (id BIGINT);

-- +godwit driver: mysql
CREATE TABLE godwit_it_my_only (id BIGINT);

-- +godwit Down
DROP TABLE godwit_it_users;
-- +godwit driver: postgres
DROP TABLE godwit_it_pg_only;
-- +godwit driver: mysql
DROP TABLE godwit_it_my_only;
`

func runIntegration(t *testing.T, d driver.Driver, tg config.Target, pw string) {
	ctx := context.Background()
	c, err := d.Open(ctx, tg, pw)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Start clean in case an earlier run died.
	p, err := migration.Parse(integrationMigration)
	if err != nil {
		t.Fatal(err)
	}
	m := &migration.Migration{Version: 1, Name: "it", Checksum: strings.Repeat("a", 64), Parsed: p}
	if err := c.EnsureTable(ctx); err != nil {
		t.Fatal(err)
	}
	MigrateDown(ctx, c, []*migration.Migration{m}, 10, nil) //nolint:errcheck

	n, err := MigrateUp(ctx, c, []*migration.Migration{m}, 0, nil)
	if err != nil || n != 1 {
		t.Fatalf("up: n=%d err=%v", n, err)
	}
	items, err := Status(ctx, c, []*migration.Migration{m})
	if err != nil || len(items) != 1 || items[0].State != Applied {
		t.Fatalf("status: %+v err=%v", items, err)
	}

	// Edited file shows as modified.
	m2 := *m
	m2.Checksum = strings.Repeat("b", 64)
	items, _ = Status(ctx, c, []*migration.Migration{&m2})
	if items[0].State != Modified {
		t.Errorf("state = %s, want modified", items[0].State)
	}

	// A second connection must wait for / be refused by the lock while held.
	unlock, err := c.Lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	unlock()

	if n, err := MigrateDown(ctx, c, []*migration.Migration{m}, 1, nil); err != nil || n != 1 {
		t.Fatalf("down: n=%d err=%v", n, err)
	}

	// A failing migration: Postgres rolls back cleanly, MySQL stays dirty.
	bad, _ := migration.Parse("-- +godwit Up\nCREATE TABLE godwit_it_ok (id INT);\nSELECT * FROM godwit_it_does_not_exist;\n-- +godwit Down\nDROP TABLE IF EXISTS godwit_it_ok;\n")
	bm := &migration.Migration{Version: 2, Name: "bad", Checksum: strings.Repeat("c", 64), Parsed: bad}
	if _, err := MigrateUp(ctx, c, []*migration.Migration{bm}, 0, nil); err == nil {
		t.Fatal("expected failure")
	}
	items, _ = Status(ctx, c, []*migration.Migration{bm})
	switch tg.Driver {
	case "postgres":
		if items[0].State != Pending {
			t.Errorf("postgres should roll back: state = %s", items[0].State)
		}
	case "mysql":
		if items[0].State != Dirty {
			t.Errorf("mysql should be dirty: state = %s", items[0].State)
		}
		if err := ClearDirty(ctx, c, 2); err != nil {
			t.Fatal(err)
		}
		MigrateDown(ctx, c, []*migration.Migration{bm}, 1, nil) //nolint:errcheck
	}
}

func TestIntegrationPostgres(t *testing.T) {
	raw := getenv(t, "GODWIT_TEST_POSTGRES")
	d, _ := driver.Get("postgres")
	tg, pw := pgTarget(t, raw)
	runIntegration(t, d, tg, pw)
}

func TestIntegrationMySQL(t *testing.T) {
	raw := getenv(t, "GODWIT_TEST_MYSQL")
	d, _ := driver.Get("mysql")
	tg, pw := myTarget(t, raw)
	runIntegration(t, d, tg, pw)
}
