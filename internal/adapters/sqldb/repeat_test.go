package sqldb_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/puriice/godwit/internal/adapters/sqldb"
	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

func openLite(t *testing.T) (app.Database, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "app.db")
	db, err := sqldb.NewFactory().Open(ctx, domain.Target{Name: "lite", Driver: "sqlite", Database: path}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.EnsureTable(ctx); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	return db, raw
}

func stmts(sqls ...string) []domain.Statement {
	out := make([]domain.Statement, len(sqls))
	for i, s := range sqls {
		out[i] = domain.Statement{Driver: domain.DriverAll, SQL: s}
	}
	return out
}

func count(t *testing.T, raw *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := raw.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func seed(t *testing.T, raw *sql.DB, table, col string, n int, extra string) {
	t.Helper()
	q := "WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < " + strconv.Itoa(n) +
		") INSERT INTO " + table + " (" + col + ") SELECT " + extra + " FROM n"
	if _, err := raw.Exec(q); err != nil {
		t.Fatal(err)
	}
}

func splitUp(cond bool) []domain.Statement {
	up := stmts(
		"ALTER TABLE users ADD COLUMN first_name TEXT",
		"ALTER TABLE users ADD COLUMN last_name TEXT",
		`UPDATE users SET first_name = substr(full_name, 1, instr(full_name, ' ') - 1),
			last_name = substr(full_name, instr(full_name, ' ') + 1)
		 WHERE id IN (SELECT id FROM users WHERE first_name IS NULL LIMIT 1000)`,
	)
	up[2].Group = 1
	if cond {
		up = append(up, domain.Statement{Driver: domain.DriverAll, Group: 1, Condition: true,
			SQL: "SELECT EXISTS (SELECT 1 FROM users WHERE first_name IS NULL)"})
	}
	return append(up, stmts("ALTER TABLE users DROP COLUMN full_name")...)
}

func TestSQLiteRepeatSplitsColumn(t *testing.T) {
	for name, cond := range map[string]bool{"until no rows": false, "condition": true} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db, raw := openLite(t)
			if _, err := raw.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY, full_name TEXT NOT NULL)"); err != nil {
				t.Fatal(err)
			}
			seed(t, raw, "users", "full_name", 2500, "'First' || i || ' Last' || i")
			m := &domain.Migration{Version: 1, Name: "split", Checksum: "c", Up: splitUp(cond)}
			if err := db.Apply(ctx, m); err != nil {
				t.Fatal(err)
			}
			if n := count(t, raw, "SELECT COUNT(*) FROM users WHERE first_name = 'First' || id AND last_name = 'Last' || id"); n != 2500 {
				t.Errorf("split rows = %d, want 2500", n)
			}
			if rs, _ := db.Applied(ctx); len(rs) != 1 || rs[0].Dirty {
				t.Errorf("Applied = %+v", rs)
			}
		})
	}
}

func TestSQLiteRepeatConditionOnEmptyTableRunsNothing(t *testing.T) {
	ctx := context.Background()
	db, raw := openLite(t)
	if _, err := raw.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY, first_name TEXT); CREATE TABLE log (n INTEGER)"); err != nil {
		t.Fatal(err)
	}
	up := stmts("INSERT INTO log VALUES (1)")
	up[0].Group = 1
	up = append(up, domain.Statement{Driver: domain.DriverAll, Group: 1, Condition: true,
		SQL: "SELECT EXISTS (SELECT 1 FROM users WHERE first_name IS NULL)"})
	if err := db.Apply(ctx, &domain.Migration{Version: 1, Name: "n", Checksum: "c", Up: up}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, raw, "SELECT COUNT(*) FROM log"); n != 0 {
		t.Errorf("body ran %d times, want 0", n)
	}
}

func TestSQLiteRepeatMultiStatementBlock(t *testing.T) {
	ctx := context.Background()
	db, raw := openLite(t)
	if _, err := raw.Exec("CREATE TABLE src (id INTEGER PRIMARY KEY); CREATE TABLE dst (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	seed(t, raw, "src", "id", 25, "i")
	// Move rows 10 at a time: copy a chunk, then delete what was copied.
	up := stmts(
		"INSERT INTO dst SELECT id FROM src ORDER BY id LIMIT 10",
		"DELETE FROM src WHERE id IN (SELECT id FROM dst)",
	)
	up[0].Group, up[1].Group = 1, 1
	if err := db.Apply(ctx, &domain.Migration{Version: 1, Name: "move", Checksum: "c", Up: up}); err != nil {
		t.Fatal(err)
	}
	if s, d := count(t, raw, "SELECT COUNT(*) FROM src"), count(t, raw, "SELECT COUNT(*) FROM dst"); s != 0 || d != 25 {
		t.Errorf("src=%d dst=%d, want 0 and 25", s, d)
	}
}

func TestSQLiteRepeatFailureLeavesDirtyAndKeepsEarlierIterations(t *testing.T) {
	ctx := context.Background()
	db, raw := openLite(t)
	// The CHECK fails on the third chunk of ten.
	if _, err := raw.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY, done INTEGER NOT NULL DEFAULT 0, CHECK (NOT (id = 25 AND done = 1)))"); err != nil {
		t.Fatal(err)
	}
	seed(t, raw, "t", "id", 30, "i")
	up := stmts("UPDATE t SET done = 1 WHERE id IN (SELECT id FROM t WHERE done = 0 ORDER BY id LIMIT 10)")
	up[0].Group = 1
	err := db.Apply(ctx, &domain.Migration{Version: 1, Name: "fail", Checksum: "c", Up: up})
	if err == nil || !strings.Contains(err.Error(), "iteration 3") {
		t.Fatalf("err = %v, want iteration 3 failure", err)
	}
	if n := count(t, raw, "SELECT COUNT(*) FROM t WHERE done = 1"); n != 20 {
		t.Errorf("committed rows = %d, want 20", n)
	}
	if rs, _ := db.Applied(ctx); len(rs) != 1 || !rs[0].Dirty {
		t.Fatalf("Applied = %+v, want one dirty row", rs)
	}
}

func TestSQLiteRepeatStopsAtCap(t *testing.T) {
	ctx := context.Background()
	db, raw := openLite(t)
	if _, err := raw.Exec("CREATE TABLE log (n INTEGER)"); err != nil {
		t.Fatal(err)
	}
	defer sqldb.SetMaxRepeat(5)()
	up := stmts("INSERT INTO log VALUES (1)") // always affects a row
	up[0].Group = 1
	err := db.Apply(ctx, &domain.Migration{Version: 1, Name: "loop", Checksum: "c", Up: up})
	if err == nil || !strings.Contains(err.Error(), "never finished") {
		t.Fatalf("err = %v", err)
	}
}

func TestSQLiteRepeatInDown(t *testing.T) {
	ctx := context.Background()
	db, raw := openLite(t)
	m := &domain.Migration{Version: 1, Name: "d", Checksum: "c",
		Up:   stmts("CREATE TABLE log (n INTEGER)", "INSERT INTO log VALUES (1), (2), (3)"),
		Down: stmts("DELETE FROM log WHERE rowid IN (SELECT rowid FROM log LIMIT 2)", "DROP TABLE log")}
	m.Down[0].Group = 1
	if err := db.Apply(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := db.Revert(ctx, m); err != nil {
		t.Fatal(err)
	}
	if n := count(t, raw, "SELECT COUNT(*) FROM sqlite_master WHERE name = 'log'"); n != 0 {
		t.Error("log table still there")
	}
	if rs, _ := db.Applied(ctx); len(rs) != 0 {
		t.Errorf("Applied = %+v", rs)
	}
}

func TestSQLiteRepeatDelay(t *testing.T) {
	ctx := context.Background()
	db, raw := openLite(t)
	if _, err := raw.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY, done INTEGER NOT NULL DEFAULT 0)"); err != nil {
		t.Fatal(err)
	}
	seed(t, raw, "t", "id", 25, "i")
	up := stmts("UPDATE t SET done = 1 WHERE id IN (SELECT id FROM t WHERE done = 0 LIMIT 10)")
	up[0].Group, up[0].Delay = 1, 40*time.Millisecond

	// 10 + 10 + 5 rows, then a pass that finds nothing: three waits.
	start := time.Now()
	if err := db.Apply(ctx, &domain.Migration{Version: 1, Name: "slow", Checksum: "c", Up: up}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 120*time.Millisecond {
		t.Errorf("took %v, want at least 3 delays of 40ms", d)
	}
	if n := count(t, raw, "SELECT COUNT(*) FROM t WHERE done = 1"); n != 25 {
		t.Errorf("done = %d", n)
	}
}

func TestSQLiteRepeatDelayStopsOnCancel(t *testing.T) {
	db, raw := openLite(t)
	if _, err := raw.Exec("CREATE TABLE log (n INTEGER)"); err != nil {
		t.Fatal(err)
	}
	up := stmts("INSERT INTO log VALUES (1)")
	up[0].Group, up[0].Delay = 1, time.Hour

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := db.Apply(ctx, &domain.Migration{Version: 1, Name: "long", Checksum: "c", Up: up})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Error("did not stop waiting")
	}
	if n := count(t, raw, "SELECT COUNT(*) FROM log"); n != 1 {
		t.Errorf("log rows = %d, want 1 (one pass before the wait)", n)
	}
}
