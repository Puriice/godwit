package fsmigrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/puriice/godwit/internal/app"
)

var _ app.MigrationSource = (*Source)(nil)

func TestLoadAndCreate(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "migs")
	os.MkdirAll(dir, 0o755)
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("0002_b.sql", "-- +godwit Up\nSELECT 2;\n-- +godwit Down\nSELECT 0;\n")
	write("0001_a.sql", "-- +godwit Up\nSELECT 1;\n")
	write("README.md", "ignored")

	s := New(root, []string{"mysql", "postgres"}) // relative dir resolves against Root
	ms, err := s.Load("migs")
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].Version != 1 || ms[1].Name != "b" || len(ms[0].Checksum) != 64 {
		t.Fatalf("unexpected: %+v", ms)
	}
	if len(ms[1].Up) != 1 || len(ms[1].Down) != 1 {
		t.Errorf("statements not carried over: %+v", ms[1])
	}

	write("1_dup.sql", "-- +godwit Up\nSELECT 1;\n")
	if _, err := s.Load("migs"); err == nil {
		t.Error("expected duplicate version error")
	}

	if ms, err := s.Load("does-not-exist"); err != nil || len(ms) != 0 {
		t.Errorf("missing dir: %v %v", ms, err)
	}

	// Scaffold output must itself parse.
	path, err := s.Create("scaffold", "add_users")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Load("scaffold"); err != nil || len(got) != 1 {
		t.Errorf("scaffold does not load: %v %v (%s)", got, err, path)
	}
	if _, err := s.Create("scaffold", "bad name"); err == nil {
		t.Error("expected invalid name error")
	}
	if got := s.Resolve("x"); got != filepath.Join(root, "x") {
		t.Errorf("Resolve = %q", got)
	}
}

func TestLoadRejectsUnknownDriverNamingTheFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "migs")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "0001_bad.sql"), []byte("-- +godwit Up\n-- +godwit driver: postgress\nSELECT 1;\n"), 0o644)

	_, err := New(root, []string{"mysql", "postgres"}).Load("migs")
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"0001_bad.sql", "line 2", "postgress"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

func TestScanReportsEveryFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "migs")
	os.MkdirAll(dir, 0o755)
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("0001_bad.sql", "-- +godwit Up\n-- +godwit driver: postgress\nSELECT 1;\n")
	write("0002_ok.sql", "-- +godwit Up\nSELECT 2;\n")
	write("0002_dup.sql", "-- +godwit Up\nSELECT 3;\n")

	s := New(root, []string{"mysql", "postgres"})
	files, err := s.Scan("migs")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("Scan returned %d files, want 3: %+v", len(files), files)
	}
	if files[0].Name != "0001_bad.sql" || files[0].Err == nil || files[0].Migration != nil {
		t.Errorf("first file should be the broken one: %+v", files[0])
	}
	if !strings.Contains(files[0].Err.Error(), "line 2") {
		t.Errorf("error %q should name the line", files[0].Err)
	}
	// Files sort by version, then name: the duplicate comes first and wins the version.
	if files[1].Name != "0002_dup.sql" || files[1].Err != nil {
		t.Errorf("second file = %+v", files[1])
	}
	if files[2].Name != "0002_ok.sql" || files[2].Err == nil || !strings.Contains(files[2].Err.Error(), "duplicate version") {
		t.Errorf("third file should be flagged as a duplicate: %+v", files[2])
	}
	if _, err := s.Load("migs"); err == nil || !strings.Contains(err.Error(), "0001_bad.sql") {
		t.Errorf("Load should still fail naming the first bad file, got %v", err)
	}
}
