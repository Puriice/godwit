package domain

import (
	"errors"
	"testing"
)

func TestRegistryPluginDriver(t *testing.T) {
	r := NewRegistry()
	r.Add(DriverInfo{
		Name: "DuckDB", Aliases: []string{"duck3"}, NoHost: true,
		Parse: func(conn string) (Target, string, error) {
			if conn == "bad" {
				return Target{}, "", errors.New("nope")
			}
			return Target{Database: conn, Driver: "ignored"}, "pw", nil
		},
	})

	if got := r.Normalize(" Duck3 "); got != "duckdb" {
		t.Errorf("normalize = %q", got)
	}
	if got := r.Normalize("pg"); got != "postgres" {
		t.Errorf("built-ins must remain: %q", got)
	}
	// A plugin owns validation, so no host, user or URL form is demanded.
	tg, pw, err := r.ParseConnectionString("duck3", "data/app.db")
	if err != nil || tg.Driver != "duckdb" || tg.Database != "data/app.db" || pw != "pw" || tg.Host != "" {
		t.Errorf("parsed %+v pw=%q err=%v", tg, pw, err)
	}
	if _, _, err := r.ParseConnectionString("duckdb", "bad"); err == nil {
		t.Error("plugin parse error swallowed")
	}
	if _, _, err := r.ParseConnectionString("oracle", "x"); err == nil {
		t.Error("unknown driver accepted")
	}
	// The package-level helpers only know the built-ins.
	if _, _, err := ParseConnectionString("duckdb", "x"); err == nil {
		t.Error("global registry must not know plugins")
	}
}

func TestRedactedStringWithoutUser(t *testing.T) {
	if got, want := (Target{Driver: "duckdb", Database: "a.db"}).RedactedString(false), "duckdb:///a.db"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestParseSQLite(t *testing.T) {
	for conn, want := range map[string]string{
		"sqlite:///var/app.db":  "/var/app.db",
		"sqlite://app.db":       "app.db",
		"sqlite:app.db":         "app.db",
		"file:data/app.db":      "data/app.db",
		"app.db":                "app.db",
		"sqlite:///C:/dir/a.db": "C:/dir/a.db",
	} {
		tg, pw, err := ParseConnectionString("sqlite3", conn)
		if err != nil || tg.Database != want || tg.Driver != "sqlite" || pw != "" {
			t.Errorf("%q = %+v %q %v, want %q", conn, tg, pw, err, want)
		}
	}
	tg, _, _ := ParseConnectionString("sqlite", "app.db?_pragma=foreign_keys(1)")
	if tg.Database != "app.db" || tg.Params["_pragma"] != "foreign_keys(1)" {
		t.Errorf("params: %+v", tg)
	}
	if _, _, err := ParseConnectionString("sqlite", "sqlite://"); err == nil {
		t.Error("expected error for empty path")
	}
}
