package sqldb_test

import (
	"context"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	mysqldrv "github.com/go-sql-driver/mysql"

	"github.com/puriice/godwit/internal/adapters/filestore"
	"github.com/puriice/godwit/internal/adapters/fsmigrations"
	"github.com/puriice/godwit/internal/adapters/sqldb"
	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

// Integration tests run the real stack (filestore + fsmigrations + sqldb)
// against real databases. They are skipped unless GODWIT_TEST_POSTGRES /
// GODWIT_TEST_MYSQL are set (see docker-compose.yml).

func getenv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("%s not set; skipping integration test", key)
	}
	return v
}

func pgTarget(t *testing.T, raw string) (domain.Target, string) {
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	pw, _ := u.User.Password()
	return domain.Target{Name: "pg", Driver: "postgres", Host: u.Hostname(), Port: port,
		Database: strings.TrimPrefix(u.Path, "/"), User: u.User.Username()}, pw
}

func myTarget(t *testing.T, dsn string) (domain.Target, string) {
	cfg, err := mysqldrv.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	host, p, _ := net.SplitHostPort(cfg.Addr)
	port, _ := strconv.Atoi(p)
	return domain.Target{Name: "my", Driver: "mysql", Host: host, Port: port,
		Database: cfg.DBName, User: cfg.User}, cfg.Passwd
}

const goodMigration = `-- +godwit Up
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

const badMigration = `-- +godwit Up
CREATE TABLE godwit_it_ok (id INT);
SELECT * FROM godwit_it_does_not_exist;
-- +godwit Down
DROP TABLE IF EXISTS godwit_it_ok;
`

func runIntegration(t *testing.T, tg domain.Target, pw string) {
	ctx := context.Background()
	root := t.TempDir()
	migDir := filepath.Join(root, "migrations")
	os.MkdirAll(migDir, 0o755)
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(migDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	svc, err := app.New(filestore.New(root), fsmigrations.New(root), sqldb.NewFactory())
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AddTarget(tg, pw, false); err != nil {
		t.Fatal(err)
	}

	// Start clean in case an earlier run died.
	write("0001_it.sql", goodMigration)
	svc.Down(ctx, tg.Name, 10, nil) //nolint:errcheck

	n, err := svc.Up(ctx, tg.Name, 0, nil)
	if err != nil || n != 1 {
		t.Fatalf("up: n=%d err=%v", n, err)
	}
	items, err := svc.Status(ctx, tg.Name)
	if err != nil || len(items) != 1 || items[0].State != domain.Applied {
		t.Fatalf("status: %+v err=%v", items, err)
	}

	// Editing the file after it was applied shows as modified.
	write("0001_it.sql", goodMigration+"-- edited\n")
	items, _ = svc.Status(ctx, tg.Name)
	if items[0].State != domain.Modified {
		t.Errorf("state = %s, want modified", items[0].State)
	}
	write("0001_it.sql", goodMigration)

	if n, err := svc.Down(ctx, tg.Name, 1, nil); err != nil || n != 1 {
		t.Fatalf("down: n=%d err=%v", n, err)
	}

	// A failing migration: Postgres rolls back cleanly, MySQL stays dirty.
	os.Remove(filepath.Join(migDir, "0001_it.sql"))
	write("0002_bad.sql", badMigration)
	if _, err := svc.Up(ctx, tg.Name, 0, nil); err == nil {
		t.Fatal("expected failure")
	}
	items, _ = svc.Status(ctx, tg.Name)
	switch tg.Driver {
	case "postgres":
		if items[0].State != domain.Pending {
			t.Errorf("postgres should roll back: state = %s", items[0].State)
		}
	case "mysql":
		if items[0].State != domain.Dirty {
			t.Errorf("mysql should be dirty: state = %s", items[0].State)
		}
		// Dirty blocks further runs until cleared.
		if _, err := svc.Up(ctx, tg.Name, 0, nil); err == nil {
			t.Error("dirty target should block runs")
		}
		if err := svc.ClearDirty(ctx, tg.Name, 2); err != nil {
			t.Fatal(err)
		}
		svc.Down(ctx, tg.Name, 1, nil) //nolint:errcheck
	}
}

func TestIntegrationPostgres(t *testing.T) {
	tg, pw := pgTarget(t, getenv(t, "GODWIT_TEST_POSTGRES"))
	runIntegration(t, tg, pw)
}

func TestIntegrationMySQL(t *testing.T) {
	tg, pw := myTarget(t, getenv(t, "GODWIT_TEST_MYSQL"))
	runIntegration(t, tg, pw)
}
