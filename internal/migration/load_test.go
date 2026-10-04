package migration

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndCreate(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("0002_b.sql", "-- +godwit Up\nSELECT 2;\n-- +godwit Down\nSELECT 0;\n")
	write("0001_a.sql", "-- +godwit Up\nSELECT 1;\n")
	write("README.md", "ignored")

	ms, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].Version != 1 || ms[1].Name != "b" || len(ms[0].Checksum) != 64 {
		t.Fatalf("unexpected: %+v", ms)
	}

	write("1_dup.sql", "-- +godwit Up\nSELECT 1;\n")
	if _, err := Load(dir); err == nil {
		t.Error("expected duplicate version error")
	}

	// Scaffold output must itself parse.
	dir2 := t.TempDir()
	path, err := Create(dir2, "add_users")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Dir(path)); err != nil {
		t.Errorf("scaffold does not parse: %v", err)
	}
	if _, err := Create(dir2, "bad name"); err == nil {
		t.Error("expected invalid name error")
	}
}
