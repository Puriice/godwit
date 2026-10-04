package filestore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

var _ app.ProjectStore = (*Store)(nil)

func TestRoundTripAndGitignore(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	p, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Targets) != 0 {
		t.Fatalf("fresh project has targets: %+v", p)
	}
	p.MigrationsDir = "db/migs"
	p.Targets = []domain.Target{{Name: "prod-db", Driver: "postgres", Host: "h", Port: 5432, Database: "d", User: "u",
		Params: map[string]string{"sslmode": "disable"}, Disabled: true}}
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}

	gi, _ := os.ReadFile(filepath.Join(root, DirName, ".gitignore"))
	if !strings.Contains(string(gi), ".env") {
		t.Errorf(".gitignore = %q", gi)
	}

	p2, err := New(root).Load()
	if err != nil {
		t.Fatal(err)
	}
	if p2.MigrationsDir != "db/migs" || len(p2.Targets) != 1 || p2.Targets[0].Name != "prod-db" ||
		p2.Targets[0].Params["sslmode"] != "disable" || p2.Targets[0].Port != 5432 || !p2.Targets[0].Disabled {
		t.Errorf("round trip: %+v", p2)
	}
}

func TestGitignoreRepairKeepsExisting(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, DirName)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log"), 0o644)
	s := New(root)
	if err := s.Save(domain.Project{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(domain.Project{}); err != nil { // idempotent
		t.Fatal(err)
	}
	gi, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if string(gi) != "*.log\n.env\n" {
		t.Errorf(".gitignore = %q", gi)
	}
}

func TestPasswords(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	if got := PasswordKey("prod-db"); got != "GODWIT_PROD_DB_PASSWORD" {
		t.Errorf("key = %s", got)
	}
	if _, ok := s.Password("prod-db"); ok {
		t.Error("expected no password yet")
	}

	// Session-only: not on disk.
	if err := s.SetPassword("prod-db", "s3cret", false); err != nil {
		t.Fatal(err)
	}
	if pw, ok := s.Password("prod-db"); !ok || pw != "s3cret" {
		t.Error("session password not resolved")
	}
	if _, err := os.Stat(filepath.Join(root, DirName, ".env")); err == nil {
		t.Error(".env written for non-persisted password")
	}

	// Persisted: survives reload, .gitignore created first.
	if err := s.SetPassword("prod-db", `p"w #1`, true); err != nil {
		t.Fatal(err)
	}
	gi, _ := os.ReadFile(filepath.Join(root, DirName, ".gitignore"))
	if !strings.Contains(string(gi), ".env") {
		t.Error(".gitignore missing .env after persisting")
	}
	s2 := New(root)
	if _, err := s2.Load(); err != nil {
		t.Fatal(err)
	}
	if pw, ok := s2.Password("prod-db"); !ok || pw != `p"w #1` {
		t.Errorf("reloaded password = %q, %v", pw, ok)
	}

	// Process env overrides .env.
	t.Setenv("GODWIT_PROD_DB_PASSWORD", "fromenv")
	if pw, _ := s2.Password("prod-db"); pw != "fromenv" {
		t.Errorf("env override = %q", pw)
	}
}

func TestDeletePassword(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	s.SetPassword("a", "pa", true)
	s.SetPassword("b", "pb", true)

	if err := s.DeletePassword("a"); err != nil {
		t.Fatal(err)
	}
	s2 := New(root)
	s2.Load()
	if _, ok := s2.Password("a"); ok {
		t.Error("password for deleted target still in .env")
	}
	if pw, ok := s2.Password("b"); !ok || pw != "pb" {
		t.Error("other target's password was lost")
	}
	if _, ok := s.Password("a"); ok {
		t.Error("password still in memory")
	}

	// Deleting the last secret removes .env entirely.
	if err := s2.DeletePassword("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, DirName, ".env")); err == nil {
		t.Error(".env should be removed when empty")
	}
	// Deleting something that was never stored is not an error.
	if err := s2.DeletePassword("nope"); err != nil {
		t.Errorf("delete missing: %v", err)
	}
}
