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
	"time"

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

	svc, err := app.New(filestore.New(root), fsmigrations.New(root, sqldb.NewFactory().Drivers()), sqldb.NewFactory())
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AddTarget("", tg, pw, false); err != nil {
		t.Fatal(err)
	}
	ref := domain.TargetRef{Target: tg.Name}

	// Start clean in case an earlier run died.
	write("0001_it.sql", goodMigration)
	svc.Down(ctx, ref, 10, nil) //nolint:errcheck

	n, err := svc.Up(ctx, ref, 0, nil)
	if err != nil || n != 1 {
		t.Fatalf("up: n=%d err=%v", n, err)
	}
	items, err := svc.Status(ctx, ref)
	if err != nil || len(items) != 1 || items[0].State != domain.Applied {
		t.Fatalf("status: %+v err=%v", items, err)
	}

	// Editing the file after it was applied shows as modified.
	write("0001_it.sql", goodMigration+"-- edited\n")
	items, _ = svc.Status(ctx, ref)
	if items[0].State != domain.Modified {
		t.Errorf("state = %s, want modified", items[0].State)
	}
	write("0001_it.sql", goodMigration)

	if n, err := svc.Down(ctx, ref, 1, nil); err != nil || n != 1 {
		t.Fatalf("down: n=%d err=%v", n, err)
	}

	// A failing migration: Postgres rolls back cleanly, MySQL stays dirty.
	os.Remove(filepath.Join(migDir, "0001_it.sql"))
	write("0002_bad.sql", badMigration)
	if _, err := svc.Up(ctx, ref, 0, nil); err == nil {
		t.Fatal("expected failure")
	}
	items, _ = svc.Status(ctx, ref)
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
		if _, err := svc.Up(ctx, ref, 0, nil); err == nil {
			t.Error("dirty target should block runs")
		}
		if err := svc.ClearDirty(ctx, ref, 2); err != nil {
			t.Fatal(err)
		}
		svc.Down(ctx, ref, 1, nil) //nolint:errcheck
	}
}

// runServicesIntegration checks that two services sharing one database keep
// separate state tables and do not block each other's locks.
func runServicesIntegration(t *testing.T, tg domain.Target, pw string) {
	ctx := context.Background()
	open := func(table string) app.Database {
		db, err := sqldb.NewFactory().Open(ctx, tg, pw, table)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if err := db.EnsureTable(ctx); err != nil {
			t.Fatal(err)
		}
		return db
	}
	users, orders := open(domain.StateTable("it_users")), open(domain.StateTable("it_orders"))
	m := &domain.Migration{Version: 1, Name: "noop", Checksum: "c",
		Up:   []domain.Statement{{Driver: domain.DriverAll, SQL: "SELECT 1"}},
		Down: []domain.Statement{{Driver: domain.DriverAll, SQL: "SELECT 1"}}}
	users.Revert(ctx, m) //nolint:errcheck // start clean

	unlock, err := users.Lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Held by users: orders must still get its own lock at once.
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	unlockOrders, err := orders.Lock(lctx)
	if err != nil {
		t.Fatalf("orders blocked by the users lock: %v", err)
	}
	unlockOrders() //nolint:errcheck
	defer unlock() //nolint:errcheck

	if err := users.Apply(ctx, m); err != nil {
		t.Fatal(err)
	}
	if rs, _ := orders.Applied(ctx); len(rs) != 0 {
		t.Errorf("orders sees the users history: %+v", rs)
	}
	if err := users.Revert(ctx, m); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationPostgres(t *testing.T) {
	tg, pw := pgTarget(t, getenv(t, "GODWIT_TEST_POSTGRES"))
	runIntegration(t, tg, pw)
	runServicesIntegration(t, tg, pw)
}

func TestIntegrationMySQL(t *testing.T) {
	tg, pw := myTarget(t, getenv(t, "GODWIT_TEST_MYSQL"))
	runIntegration(t, tg, pw)
	runServicesIntegration(t, tg, pw)
}
