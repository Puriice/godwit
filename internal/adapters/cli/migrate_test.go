package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func runMigrate(t *testing.T, args ...string) (string, error) {
	t.Helper()
	svc, _ := newService(t)
	var out bytes.Buffer
	err := Migrate(context.Background(), svc, args, &out)
	return out.String(), err
}

func TestMigrateNew(t *testing.T) {
	svc, root := newService(t)
	var out bytes.Buffer
	if err := Migrate(context.Background(), svc, []string{"new", "add_x"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "add_x.sql") || !strings.Contains(out.String(), root) {
		t.Fatalf("output = %q", out.String())
	}
}

func TestMigrateArgumentErrors(t *testing.T) {
	cases := map[string][]string{
		"unknown":        {"bogus"},
		"none":           nil,
		"both flags":     {"up", "-n", "1", "--to", "5"},
		"bad version":    {"up", "--to", "abc"},
		"negative n":     {"down", "-n", "-1"},
		"no targets":     {"status"},
		"unknown target": {"up", "nope"},
		"redo no ver":    {"redo"},
		"clear args":     {"clear-dirty", "1"},
		"new args":       {"new"},
	}
	for name, args := range cases {
		if _, err := runMigrate(t, args...); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestMigrateSkipsDisabledAndContinuesAfterFailure(t *testing.T) {
	svc, _ := newService(t)
	for _, n := range []string{"a", "b", "c"} {
		if err := Auth(svc, []string{"add", n, "postgres", "postgres://u:p@h/db"}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.SetTargetEnabled("b", false); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := Migrate(context.Background(), svc, []string{"up"}, &out)
	if err == nil || !strings.Contains(err.Error(), "a, c") {
		t.Fatalf("err = %v", err)
	}
	if got := out.String(); !strings.Contains(got, "== a ==") || !strings.Contains(got, "== c ==") || strings.Contains(got, "== b ==") {
		t.Fatalf("output = %q", got)
	}
}
