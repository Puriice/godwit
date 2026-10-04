package domain

import (
	"errors"
	"testing"
)

func TestRegistryPluginDriver(t *testing.T) {
	r := NewRegistry()
	r.Add(DriverInfo{
		Name: "SQLite", Aliases: []string{"sqlite3"}, NoHost: true,
		Parse: func(conn string) (Target, string, error) {
			if conn == "bad" {
				return Target{}, "", errors.New("nope")
			}
			return Target{Database: conn, Driver: "ignored"}, "pw", nil
		},
	})

	if got := r.Normalize(" SQLite3 "); got != "sqlite" {
		t.Errorf("normalize = %q", got)
	}
	if got := r.Normalize("pg"); got != "postgres" {
		t.Errorf("built-ins must remain: %q", got)
	}
	// A plugin owns validation, so no host, user or URL form is demanded.
	tg, pw, err := r.ParseConnectionString("sqlite3", "data/app.db")
	if err != nil || tg.Driver != "sqlite" || tg.Database != "data/app.db" || pw != "pw" || tg.Host != "" {
		t.Errorf("parsed %+v pw=%q err=%v", tg, pw, err)
	}
	if _, _, err := r.ParseConnectionString("sqlite", "bad"); err == nil {
		t.Error("plugin parse error swallowed")
	}
	if _, _, err := r.ParseConnectionString("oracle", "x"); err == nil {
		t.Error("unknown driver accepted")
	}
	// The package-level helpers only know the built-ins.
	if _, _, err := ParseConnectionString("sqlite", "x"); err == nil {
		t.Error("global registry must not know plugins")
	}
}

func TestRedactedStringWithoutUser(t *testing.T) {
	if got, want := (Target{Driver: "sqlite", Database: "a.db"}).RedactedString(false), "sqlite:///a.db"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
