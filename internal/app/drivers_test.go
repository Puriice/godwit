package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/puriice/godwit/internal/domain"
)

// pluginFactory is a factory whose driver is not built in.
type pluginFactory struct{ opened int }

func (*pluginFactory) Drivers() []string { return []string{"sqlite"} }
func (p *pluginFactory) Open(context.Context, domain.Target, string) (Database, error) {
	p.opened++
	return newFakeDB(), nil
}
func (*pluginFactory) DriverInfos() []domain.DriverInfo {
	return []domain.DriverInfo{{
		Name: "sqlite", Aliases: []string{"sqlite3"}, NoHost: true,
		Parse: func(conn string) (domain.Target, string, error) {
			path, ok := strings.CutPrefix(conn, "sqlite://")
			if !ok {
				return domain.Target{}, "", errors.New("want sqlite://<file>")
			}
			return domain.Target{Database: path}, "", nil
		},
	}}
}

func TestCombineMergesAndDispatches(t *testing.T) {
	built, plug := &fakeFactory{db: newFakeDB()}, &pluginFactory{}
	c := Combine(built, plug)

	if got := c.Drivers(); !slices.Equal(got, []string{"mysql", "postgres", "sqlite"}) {
		t.Errorf("drivers = %v", got)
	}
	if _, err := c.Open(context.Background(), domain.Target{Driver: "SQLite"}, ""); err != nil || plug.opened != 1 {
		t.Errorf("sqlite should open via the plugin factory: err=%v opened=%d", err, plug.opened)
	}
	if _, err := c.Open(context.Background(), domain.Target{Driver: "postgres"}, ""); err != nil {
		t.Error(err)
	}
	if _, err := c.Open(context.Background(), domain.Target{Driver: "oracle"}, ""); err == nil || !strings.Contains(err.Error(), "available: mysql, postgres, sqlite") {
		t.Errorf("unknown driver error = %v", err)
	}
	if d, ok := c.(DriverDescriber); !ok || len(d.DriverInfos()) != 1 {
		t.Error("combined factory should expose plugin driver infos")
	}
}

func TestServiceKnowsPluginDrivers(t *testing.T) {
	store := newFakeStore()
	svc, err := New(store, &fakeSource{}, Combine(&fakeFactory{db: newFakeDB()}, &pluginFactory{}))
	if err != nil {
		t.Fatal(err)
	}

	tg, _, err := svc.ParseConnectionString("sqlite3", "sqlite://data/app.db")
	if err != nil || tg.Driver != "sqlite" || tg.Database != "data/app.db" {
		t.Fatalf("parsed %+v, %v", tg, err)
	}
	// Built-ins still parse as before.
	if tg, _, err := svc.ParseConnectionString("pg", "postgres://u:p@h/d"); err != nil || tg.Driver != "postgres" {
		t.Errorf("built-in parse: %+v, %v", tg, err)
	}
	if !svc.DriverNoHost("sqlite") || svc.DriverNoHost("postgres") || svc.DriverNoHost("nope") {
		t.Error("DriverNoHost")
	}

	// A target with no host or user is fine for a no-host plugin driver.
	tg.Name = "local"
	if err := svc.AddTarget(tg, "", false); err != nil {
		t.Fatal(err)
	}
	// File based drivers have no password, and must not be blocked for lacking one.
	if svc.NeedsPassword("local") || len(svc.TargetsMissingPassword()) != 0 {
		t.Error("no-host driver should not need a password")
	}
	if _, err := svc.Status(context.Background(), "local"); err != nil {
		t.Errorf("status without password: %v", err)
	}
	// Aliases are accepted when validating.
	if err := svc.AddTarget(domain.Target{Name: "other", Driver: "SQLite3"}, "", false); err != nil {
		t.Errorf("alias target: %v", err)
	}
}

func TestPluginConfig(t *testing.T) {
	store := newFakeStore()
	svc, err := New(store, &fakeSource{}, &fakeFactory{db: newFakeDB()})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AddPlugin(domain.PluginSpec{Name: " SQLite ", Command: "godwit-driver-sqlite", Args: []string{"-v"}}); err != nil {
		t.Fatal(err)
	}
	if got := store.project.Plugins; len(got) != 1 || got[0].Name != "sqlite" || got[0].Args[0] != "-v" {
		t.Fatalf("saved plugins = %+v", got)
	}
	if err := svc.AddPlugin(domain.PluginSpec{Name: "sqlite", Command: "x"}); err == nil {
		t.Error("duplicate plugin accepted")
	}
	for _, bad := range []domain.PluginSpec{{Command: "x"}, {Name: "n"}} {
		if err := svc.AddPlugin(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	if err := svc.RemovePlugin("nope"); err == nil {
		t.Error("removing unknown plugin succeeded")
	}
	if err := svc.RemovePlugin("SQLite"); err != nil || len(svc.Plugins()) != 0 || len(store.project.Plugins) != 0 {
		t.Errorf("remove: %v %v", err, store.project.Plugins)
	}
}
