package sqldb_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/puriice/godwit/internal/adapters/filestore"
	"github.com/puriice/godwit/internal/adapters/fsmigrations"
	"github.com/puriice/godwit/internal/adapters/sqldb"
	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

const gooseFile = `-- +goose Up
CREATE TABLE users (id INTEGER PRIMARY KEY);

-- +goose StatementBegin
CREATE TRIGGER t AFTER INSERT ON users BEGIN SELECT 1; END;
-- +goose StatementEnd

-- +goose Down
DROP TABLE users;
`

func TestTranslateGooseEndToEnd(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "app.db")
	migDir := filepath.Join(root, "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"00001_users.sql":   gooseFile,
		"00002_posts.sql":   "-- +goose Up\nCREATE TABLE posts (id INTEGER);\n-- +goose Down\nDROP TABLE posts;\n",
		"00003_rolled.sql":  "-- +goose Up\nCREATE TABLE a (id INTEGER);\n-- +goose Down\nDROP TABLE a;\n",
		"00004_nofile.sql":  "-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n",
		"00005_bad.name.go": "package migrations",
	} {
		if err := os.WriteFile(filepath.Join(migDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A goose table as goose creates it: 1 and 2 applied, 3 applied then rolled
	// back, 9 applied but with no file.
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, q := range []string{
		`CREATE TABLE goose_db_version (id INTEGER PRIMARY KEY AUTOINCREMENT, version_id BIGINT NOT NULL, is_applied BOOLEAN NOT NULL, tstamp TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1), (1, 1), (2, 1), (3, 1), (3, 0), (9, 1)`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	factory := sqldb.NewFactory()
	svc, err := app.New(filestore.New(root), fsmigrations.New(root, factory.Drivers()), factory)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AddTarget(domain.Target{Name: "lite", Driver: "sqlite", Database: dbPath}, "", false); err != nil {
		t.Fatal(err)
	}

	// A dry run changes nothing.
	files, err := svc.TranslateFiles("goose", true)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(migDir, "00001_users.sql"))
	if string(before) != gooseFile {
		t.Fatalf("dry run rewrote the file:\n%s", before)
	}
	res, err := svc.TranslateState(ctx, "lite", "goose", "goose_db_version", files, true)
	if err != nil || len(res.Imported) != 2 {
		t.Fatalf("dry-run state = %+v, %v", res, err)
	}
	if n := rowCount(t, raw, "godwit_migration"); n >= 0 {
		t.Errorf("dry run created godwit_migration (%d rows)", n)
	}

	for run := 1; run <= 2; run++ {
		files, err = svc.TranslateFiles("goose", false)
		if err != nil {
			t.Fatal(err)
		}
		res, err = svc.TranslateState(ctx, "lite", "goose", "goose_db_version", files, false)
		if err != nil {
			t.Fatal(err)
		}
		wantImported, wantExisting := 2, 0
		if run == 2 {
			wantImported, wantExisting = 0, 2
		}
		if len(res.Imported) != wantImported || len(res.Existing) != wantExisting {
			t.Errorf("run %d: %+v", run, res)
		}
		if len(res.NoFile) != 1 || res.NoFile[0] != 9 || res.Unapplied != 1 { // 9 has no file; 3 was rolled back
			t.Errorf("run %d: NoFile=%v Unapplied=%d", run, res.NoFile, res.Unapplied)
		}
	}

	// Files keep their goose lines and gained godwit ones.
	got, _ := os.ReadFile(filepath.Join(migDir, "00001_users.sql"))
	for _, want := range []string{"-- +goose Up", "-- +godwit Up", "-- +godwit StatementBegin", "-- +godwit StatementEnd", "-- +goose Down", "-- +godwit Down"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("converted file lacks %q:\n%s", want, got)
		}
	}

	// The goose table is untouched and the state matches the new files.
	if n := rowCount(t, raw, "goose_db_version"); n != 6 {
		t.Errorf("goose_db_version has %d rows, want 6", n)
	}
	items, err := svc.Status(ctx, "lite")
	if err != nil {
		t.Fatal(err)
	}
	state := map[int64]domain.State{}
	for _, it := range items {
		state[it.Version] = it.State
	}
	if state[1] != domain.Applied || state[2] != domain.Applied || state[3] != domain.Pending || state[4] != domain.Pending {
		t.Errorf("states = %v", state)
	}
}

// rowCount returns the rows in table, or -1 if it does not exist.
func rowCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		return -1
	}
	return n
}

func TestTranslateGooseNoTable(t *testing.T) {
	ctx := context.Background()
	tg := domain.Target{Name: "lite", Driver: "sqlite", Database: filepath.Join(t.TempDir(), "x.db")}
	db, err := sqldb.NewFactory().Open(ctx, tg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	imp := db.(app.ForeignImporter)
	if _, found, err := imp.ForeignApplied(ctx, "goose", "goose_db_version"); err != nil || found {
		t.Errorf("found=%v err=%v, want not found", found, err)
	}
	if _, _, err := imp.ForeignApplied(ctx, "goose", "x; DROP TABLE y"); err == nil {
		t.Error("an unsafe table name was accepted")
	}
}
