package sqldb_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/puriice/godwit/internal/adapters/sqldb"
	"github.com/puriice/godwit/internal/domain"
)

func TestSQLiteApplyRevert(t *testing.T) {
	ctx := context.Background()
	tg := domain.Target{Name: "lite", Driver: "sqlite", Database: filepath.Join(t.TempDir(), "app.db")}
	db, err := sqldb.NewFactory().Open(ctx, tg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureTable(ctx); err != nil {
		t.Fatal(err)
	}
	unlock, err := db.Lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	m := &domain.Migration{Version: 1, Name: "users", Checksum: "abc",
		Up:   []domain.Statement{{Driver: domain.DriverAll, SQL: "CREATE TABLE users (id INTEGER PRIMARY KEY)"}},
		Down: []domain.Statement{{Driver: "sqlite", SQL: "DROP TABLE users"}}}
	if err := db.Apply(ctx, m); err != nil {
		t.Fatal(err)
	}
	rs, err := db.Applied(ctx)
	if err != nil || len(rs) != 1 || rs[0].Version != 1 || rs[0].Dirty || rs[0].AppliedAt.IsZero() {
		t.Fatalf("Applied = %+v, %v", rs, err)
	}

	bad := &domain.Migration{Version: 2, Name: "bad", Checksum: "x",
		Up: []domain.Statement{{Driver: domain.DriverAll, SQL: "CREATE TABLE a (id INTEGER)"}, {Driver: domain.DriverAll, SQL: "NOT SQL"}}}
	if err := db.Apply(ctx, bad); err == nil {
		t.Fatal("expected failure")
	}
	if rs, _ := db.Applied(ctx); len(rs) != 1 {
		t.Fatalf("failed migration should roll back, got %+v", rs)
	}

	if err := db.Revert(ctx, m); err != nil {
		t.Fatal(err)
	}
	if rs, _ := db.Applied(ctx); len(rs) != 0 {
		t.Fatalf("after revert: %+v", rs)
	}
}

func TestSQLiteBatchColumn(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")

	// A state table from before batches existed gets the column added.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE ` + sqldb.Table + ` (version INTEGER NOT NULL PRIMARY KEY, name TEXT NOT NULL,
		checksum TEXT NOT NULL, applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		duration_ms INTEGER NOT NULL DEFAULT 0, dirty BOOLEAN NOT NULL DEFAULT FALSE)`)
	if err == nil {
		_, err = raw.Exec(`INSERT INTO ` + sqldb.Table + ` (version, name, checksum) VALUES (1, 'old', 'c')`)
	}
	raw.Close()
	if err != nil {
		t.Fatal(err)
	}

	tg := domain.Target{Name: "lite", Driver: "sqlite", Database: path}
	db, err := sqldb.NewFactory().Open(ctx, tg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for range 2 { // idempotent
		if err := db.EnsureTable(ctx); err != nil {
			t.Fatal(err)
		}
	}
	m := &domain.Migration{Version: 2, Name: "new", Checksum: "n", Batch: 7,
		Up: []domain.Statement{{Driver: domain.DriverAll, SQL: "CREATE TABLE t (id INTEGER)"}}}
	if err := db.Apply(ctx, m); err != nil {
		t.Fatal(err)
	}
	rs, err := db.Applied(ctx)
	if err != nil || len(rs) != 2 || rs[0].Batch != 0 || rs[1].Batch != 7 {
		t.Fatalf("Applied = %+v, %v", rs, err)
	}
}
