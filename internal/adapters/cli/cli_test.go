package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/puriice/godwit/internal/adapters/filestore"
	"github.com/puriice/godwit/internal/adapters/fsmigrations"
	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

type noDBs struct{}

func (noDBs) Drivers() []string { return []string{"mysql", "postgres"} }
func (noDBs) Open(context.Context, domain.Target, string) (app.Database, error) {
	return nil, errors.New("no database in tests")
}

func newService(t *testing.T) (*app.Service, string) {
	t.Helper()
	root := t.TempDir()
	svc, err := app.New(filestore.New(root), fsmigrations.New(root, noDBs{}.Drivers()), noDBs{})
	if err != nil {
		t.Fatal(err)
	}
	return svc, root
}

func TestAuthAdd(t *testing.T) {
	svc, root := newService(t)
	var out bytes.Buffer

	err := Auth(svc, []string{"add", "prod", "postgres", "postgres://app:s3cret@db.example.com:5433/shop?sslmode=disable"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := svc.Target("prod")
	if !ok || got.Host != "db.example.com" || got.Port != 5433 || got.Database != "shop" || got.User != "app" ||
		got.Params["sslmode"] != "disable" {
		t.Fatalf("target = %+v", got)
	}
	if !svc.HasPassword("prod") {
		t.Error("password not stored")
	}

	// Persisted correctly: secret only in .env, never in config.json or output.
	cfg, _ := os.ReadFile(filepath.Join(root, ".godwit", "config.json"))
	env, _ := os.ReadFile(filepath.Join(root, ".godwit", ".env"))
	gi, _ := os.ReadFile(filepath.Join(root, ".godwit", ".gitignore"))
	if strings.Contains(string(cfg), "s3cret") || strings.Contains(out.String(), "s3cret") {
		t.Error("password leaked into config.json or command output")
	}
	if !strings.Contains(string(env), "s3cret") || !strings.Contains(string(gi), ".env") {
		t.Errorf(".env=%q .gitignore=%q", env, gi)
	}
}

func TestAuthAddWithoutPasswordAndMySQLDSN(t *testing.T) {
	svc, _ := newService(t)
	var out bytes.Buffer
	if err := Auth(svc, []string{"add", "staging", "mysql", "u@tcp(h:3306)/d"}, &out); err != nil {
		t.Fatal(err)
	}
	if svc.HasPassword("staging") {
		t.Error("no password was given")
	}
	if !strings.Contains(out.String(), "ask for it") {
		t.Errorf("output = %q", out.String())
	}
}

func TestAuthList(t *testing.T) {
	svc, _ := newService(t)

	var empty bytes.Buffer
	if err := Auth(svc, []string{"list"}, &empty); err != nil || !strings.Contains(empty.String(), "No targets") {
		t.Fatalf("empty list: %q %v", empty.String(), err)
	}

	for _, a := range [][]string{
		{"add", "prod", "postgres", "postgres://app:s3cret@db.example.com:5433/shop?sslmode=disable"},
		{"add", "stg", "mysql", "root:hunter2@tcp(localhost:3306)/app"},
		{"add", "nopw", "mysql", "u@tcp(h)/d"},
	} {
		if err := Auth(svc, a, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	if err := Auth(svc, []string{"list"}, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, secret := range []string{"s3cret", "hunter2"} {
		if strings.Contains(got, secret) {
			t.Errorf("output leaks %q:\n%s", secret, got)
		}
	}
	for _, want := range []string{
		"prod", "postgres://app:****@db.example.com:5433/shop?sslmode=disable",
		"stg", "mysql://root:****@localhost:3306/app",
		"nopw", "mysql://u@h/d", "(no password)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "(no password)") != 1 {
		t.Errorf("only nopw should be flagged:\n%s", got)
	}
	if strings.Index(got, "prod") > strings.Index(got, "stg") {
		t.Errorf("targets should keep insertion order:\n%s", got)
	}

	if err := Auth(svc, []string{"list", "extra"}, &bytes.Buffer{}); err == nil {
		t.Error("expected error for extra arguments")
	}
}

func TestAuthRemove(t *testing.T) {
	svc, root := newService(t)
	for _, a := range [][]string{
		{"add", "keep", "postgres", "postgres://u:keepsecret@h/d"},
		{"add", "drop", "mysql", "u:dropsecret@tcp(h)/d"},
	} {
		if err := Auth(svc, a, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	if err := Auth(svc, []string{"remove", "drop"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"drop"`) || strings.Contains(out.String(), "dropsecret") {
		t.Errorf("output = %q", out.String())
	}
	if _, ok := svc.Target("drop"); ok {
		t.Error("target still present")
	}
	if _, ok := svc.Target("keep"); !ok || !svc.HasPassword("keep") {
		t.Error("other target or its password was affected")
	}

	// Gone from disk too: config.json and .env.
	cfg, _ := os.ReadFile(filepath.Join(root, ".godwit", "config.json"))
	env, _ := os.ReadFile(filepath.Join(root, ".godwit", ".env"))
	if strings.Contains(string(cfg), `"drop"`) || strings.Contains(string(env), "dropsecret") || strings.Contains(string(env), "DROP") {
		t.Errorf("remnants on disk:\n%s\n%s", cfg, env)
	}
	if !strings.Contains(string(env), "keepsecret") {
		t.Errorf(".env lost the other target's password: %q", env)
	}

	if err := Auth(svc, []string{"remove", "drop"}, &bytes.Buffer{}); err == nil {
		t.Error("expected error removing an unknown target")
	}
	for _, args := range [][]string{{"remove"}, {"remove", "a", "b"}} {
		if err := Auth(svc, args, &bytes.Buffer{}); err == nil {
			t.Errorf("%v: expected error", args)
		}
	}
	if len(svc.Targets()) != 1 {
		t.Errorf("failed commands changed the target list: %+v", svc.Targets())
	}
}

func TestEnableDisable(t *testing.T) {
	svc, root := newService(t)
	for _, a := range [][]string{
		{"add", "prod", "postgres", "postgres://u:pw@h/d"},
		{"add", "stg", "mysql", "u@tcp(h)/d"},
	} {
		if err := Auth(svc, a, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	if err := Auth(svc, []string{"disable", "prod"}, &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Target("prod"); !got.Disabled {
		t.Fatal("prod not disabled")
	}
	if got, _ := svc.Target("stg"); got.Disabled {
		t.Error("stg should be unaffected")
	}
	if !strings.Contains(out.String(), "Disabled") || !strings.Contains(out.String(), "auth enable prod") {
		t.Errorf("output = %q", out.String())
	}

	// Persisted, and the password is kept.
	cfg, _ := os.ReadFile(filepath.Join(root, ".godwit", "config.json"))
	env, _ := os.ReadFile(filepath.Join(root, ".godwit", ".env"))
	if !strings.Contains(string(cfg), `"disabled": true`) || !strings.Contains(string(env), "pw") {
		t.Errorf("config=%s env=%s", cfg, env)
	}

	// list marks it, combined with other notes.
	var list bytes.Buffer
	Auth(svc, []string{"list"}, &list)
	if !strings.Contains(list.String(), "(disabled)") || !strings.Contains(list.String(), "(no password)") {
		t.Errorf("list:\n%s", list.String())
	}
	svc.SetTargetEnabled("stg", false)
	list.Reset()
	Auth(svc, []string{"list"}, &list)
	if !strings.Contains(list.String(), "(disabled, no password)") {
		t.Errorf("combined notes missing:\n%s", list.String())
	}

	// Idempotent, with a clear message.
	out.Reset()
	if err := Auth(svc, []string{"disable", "prod"}, &out); err != nil || !strings.Contains(out.String(), "already disabled") {
		t.Errorf("repeat disable: %q %v", out.String(), err)
	}

	out.Reset()
	if err := Auth(svc, []string{"enable", "prod"}, &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Target("prod"); got.Disabled || !strings.Contains(out.String(), "Enabled") {
		t.Errorf("enable: %+v %q", got, out.String())
	}
	out.Reset()
	if err := Auth(svc, []string{"enable", "prod"}, &out); err != nil || !strings.Contains(out.String(), "already enabled") {
		t.Errorf("repeat enable: %q %v", out.String(), err)
	}

	for _, args := range [][]string{{}, {"a", "b"}, {"ghost"}} {
		if err := Auth(svc, append([]string{"enable"}, args...), &bytes.Buffer{}); err == nil {
			t.Errorf("enable %v: expected error", args)
		}
		if err := Auth(svc, append([]string{"disable"}, args...), &bytes.Buffer{}); err == nil {
			t.Errorf("disable %v: expected error", args)
		}
	}
}

func TestAuthAddErrors(t *testing.T) {
	svc, _ := newService(t)
	cases := map[string][]string{
		"no subcommand":   {},
		"unknown":         {"remove", "x"},
		"too few args":    {"add", "x", "postgres"},
		"unquoted spaces": {"add", "x", "postgres", "postgres://u@h/d", "extra"},
		"bad driver":      {"add", "x", "oracle", "oracle://u@h/d"},
		"bad conn string": {"add", "x", "postgres", "nonsense"},
		"scheme mismatch": {"add", "x", "postgres", "mysql://u@h/d"},
		"empty name":      {"add", " ", "postgres", "postgres://u@h/d"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if err := Auth(svc, args, &bytes.Buffer{}); err == nil {
				t.Error("expected error")
			}
		})
	}
	if n := len(svc.Targets()); n != 0 {
		t.Errorf("failed commands added %d targets", n)
	}

	if err := Auth(svc, []string{"add", "dup", "postgres", "postgres://u@h/d"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := Auth(svc, []string{"add", "dup", "postgres", "postgres://u@h/d"}, &bytes.Buffer{}); err == nil {
		t.Error("expected duplicate name error")
	}
}
