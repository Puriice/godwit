package fsmigrations

import (
	"os"
	"path/filepath"
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

	s := New(root) // relative dir resolves against Root
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
