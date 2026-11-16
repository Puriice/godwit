package sqldb_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/puriice/godwit/internal/adapters/sqldb"
	"github.com/puriice/godwit/internal/domain"
)

func TestLockNamesForTables(t *testing.T) {
	// The default table keeps the locks of older godwit versions, so a run of
	// an older binary and a newer one still exclude each other.
	if got := sqldb.PgLockKeyFor(sqldb.Table); got != 0x676f64776974 {
		t.Errorf("default postgres lock key = %#x", got)
	}
	if got := sqldb.MySQLLockName(sqldb.Table); got != "godwit_migration_lock" {
		t.Errorf("default mysql lock name = %q", got)
	}
	users, orders := domain.StateTable("users"), domain.StateTable("orders")
	if sqldb.PgLockKeyFor(users) == sqldb.PgLockKeyFor(orders) || sqldb.PgLockKeyFor(users) == sqldb.PgLockKeyFor(sqldb.Table) {
		t.Error("services must not share a postgres lock")
	}
	if sqldb.MySQLLockName(users) != "godwit_migration_users_lock" {
		t.Errorf("mysql lock name = %q", sqldb.MySQLLockName(users))
	}
}

func TestOpenRejectsUnsafeTableNames(t *testing.T) {
	tg := domain.Target{Driver: "sqlite", Database: filepath.Join(t.TempDir(), "x.db")}
	for _, bad := range []string{"", "a b", "t; DROP TABLE x", "1abc", "a-b"} {
		if db, err := sqldb.NewFactory().Open(context.Background(), tg, "", bad); err == nil {
			db.Close()
			t.Errorf("table %q accepted", bad)
		}
	}
}

// Two services sharing one database record their migrations in separate
// tables, so the same version can be applied for one and pending for the other.
func TestTablesKeepServicesApart(t *testing.T) {
	ctx := context.Background()
	tg := domain.Target{Name: "dev", Driver: "sqlite", Database: filepath.Join(t.TempDir(), "dev.db")}
	open := func(table string) interface {
		EnsureTable(context.Context) error
		Applied(context.Context) ([]domain.Record, error)
		Apply(context.Context, *domain.Migration) error
		Close() error
	} {
		db, err := sqldb.NewFactory().Open(ctx, tg, "", table)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if err := db.EnsureTable(ctx); err != nil {
			t.Fatal(err)
		}
		return db
	}
	users, orders := open(domain.StateTable("users")), open(domain.StateTable("orders"))

	m := &domain.Migration{Version: 1, Name: "users", Checksum: "abc",
		Up: []domain.Statement{{Driver: domain.DriverAll, SQL: "CREATE TABLE users (id INTEGER PRIMARY KEY)"}}}
	if err := users.Apply(ctx, m); err != nil {
		t.Fatal(err)
	}
	if rs, err := users.Applied(ctx); err != nil || len(rs) != 1 {
		t.Fatalf("users = %+v, %v", rs, err)
	}
	if rs, err := orders.Applied(ctx); err != nil || len(rs) != 0 {
		t.Fatalf("orders sees the users history: %+v, %v", rs, err)
	}
	// The same version applies independently for the other service.
	m2 := &domain.Migration{Version: 1, Name: "orders", Checksum: "def",
		Up: []domain.Statement{{Driver: domain.DriverAll, SQL: "CREATE TABLE orders (id INTEGER PRIMARY KEY)"}}}
	if err := orders.Apply(ctx, m2); err != nil {
		t.Fatal(err)
	}
	if rs, _ := orders.Applied(ctx); len(rs) != 1 || rs[0].Name != "orders" {
		t.Fatalf("orders = %+v", rs)
	}
}
