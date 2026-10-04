package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTripAndGitignore(t *testing.T) {
	root := t.TempDir()
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	p.Targets = []Target{{Name: "prod-db", Driver: "postgres", Host: "h", Port: 5432, Database: "d", User: "u"}}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}

	gi, _ := os.ReadFile(filepath.Join(root, DirName, ".gitignore"))
	if !strings.Contains(string(gi), ".env") {
		t.Errorf(".gitignore = %q", gi)
	}

	p2, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Targets) != 1 || p2.Targets[0].Name != "prod-db" || p2.MigrationsDir != "migrations" {
		t.Errorf("round trip: %+v", p2.Config)
	}
}

func TestGitignoreRepairKeepsExisting(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, DirName)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log"), 0o644)
	p, _ := Load(root)
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	if err := p.Save(); err != nil { // idempotent
		t.Fatal(err)
	}
	gi, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if string(gi) != "*.log\n.env\n" {
		t.Errorf(".gitignore = %q", gi)
	}
}

func TestRemoveTargetDeletesPassword(t *testing.T) {
	root := t.TempDir()
	p, _ := Load(root)
	a, b := Target{Name: "a", Driver: "mysql"}, Target{Name: "b", Driver: "mysql"}
	p.Targets = []Target{a, b}
	p.SetPassword(a, "pa", true)
	p.SetPassword(b, "pb", true)

	if err := p.RemoveTarget("a"); err != nil {
		t.Fatal(err)
	}
	p2, _ := Load(root)
	if len(p2.Targets) != 1 || p2.Targets[0].Name != "b" {
		t.Errorf("targets = %+v", p2.Targets)
	}
	if _, ok := p2.Password(a); ok {
		t.Error("password for removed target still in .env")
	}
	if pw, ok := p2.Password(b); !ok || pw != "pb" {
		t.Error("other target's password was lost")
	}

	// Removing the last secret deletes .env entirely.
	if err := p2.RemoveTarget("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, DirName, ".env")); err == nil {
		t.Error(".env should be removed when empty")
	}
	if err := p2.RemoveTarget("nope"); err == nil {
		t.Error("expected error for unknown target")
	}
}

func TestPasswords(t *testing.T) {
	root := t.TempDir()
	p, _ := Load(root)
	tg := Target{Name: "prod-db", Driver: "mysql"}
	p.Targets = []Target{tg}

	if got := PasswordKey("prod-db"); got != "GODWIT_PROD_DB_PASSWORD" {
		t.Errorf("key = %s", got)
	}
	if len(p.MissingPasswords()) != 1 {
		t.Error("expected missing password")
	}

	// Session-only: not on disk.
	if err := p.SetPassword(tg, "s3cret", false); err != nil {
		t.Fatal(err)
	}
	if pw, ok := p.Password(tg); !ok || pw != "s3cret" {
		t.Error("session password not resolved")
	}
	if _, err := os.Stat(filepath.Join(root, DirName, ".env")); err == nil {
		t.Error(".env written for non-persisted password")
	}

	// Persisted: survives reload, .gitignore created first.
	if err := p.SetPassword(tg, `p"w #1`, true); err != nil {
		t.Fatal(err)
	}
	gi, _ := os.ReadFile(filepath.Join(root, DirName, ".gitignore"))
	if !strings.Contains(string(gi), ".env") {
		t.Error(".gitignore missing .env after persisting")
	}
	p2, _ := Load(root)
	p2.Targets = p.Targets
	if pw, ok := p2.Password(tg); !ok || pw != `p"w #1` {
		t.Errorf("reloaded password = %q, %v", pw, ok)
	}

	// Process env overrides .env.
	t.Setenv("GODWIT_PROD_DB_PASSWORD", "fromenv")
	if pw, _ := p2.Password(tg); pw != "fromenv" {
		t.Errorf("env override = %q", pw)
	}
}
