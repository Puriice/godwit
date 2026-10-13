package plugin

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
	"github.com/puriice/godwit/pkg/godwit"
)

// These tests run a plugin written with the public pkg/godwit package
// through godwit's real adapter. The two define the wire protocol separately,
// so this is what keeps them in step.

func runPkgPlugin() {
	if err := godwit.ServeIO(os.Stdin, os.Stdout, pkgDriver{}); err != nil {
		os.Exit(1)
	}
}

type pkgDriver struct{}

func (pkgDriver) Info() godwit.Info {
	return godwit.Info{Name: "pkgdb", Aliases: []string{"pdb"}, Schemes: []string{"pkgdb"}, NoHost: true, Translate: []string{"goose"}}
}

func (pkgDriver) ParseConnection(conn string) (godwit.Target, string, error) {
	name, ok := strings.CutPrefix(conn, "pkgdb://")
	if !ok {
		return godwit.Target{}, "", errors.New("expected pkgdb://<name>")
	}
	return godwit.Target{Database: name, Params: map[string]string{"k": "v"}, Port: 7}, "pw", nil
}

func (pkgDriver) Open(_ context.Context, t godwit.Target, password string) (godwit.Connection, error) {
	if password != "secret" || t.Name != "tgt" || t.Database != "db" || t.Params["x"] != "y" {
		return nil, errors.New("target or password did not arrive intact")
	}
	return &pkgConn{}, nil
}

type pkgConn struct {
	recs   []godwit.Record
	locked bool
}

func (*pkgConn) Close() error                      { return nil }
func (*pkgConn) EnsureTable(context.Context) error { return nil }
func (c *pkgConn) Lock(context.Context) (func() error, error) {
	if c.locked {
		return nil, errors.New("lock held")
	}
	c.locked = true
	return func() error { c.locked = false; return nil }, nil
}
func (c *pkgConn) Applied(context.Context) ([]godwit.Record, error) { return c.recs, nil }
func (c *pkgConn) Apply(_ context.Context, m godwit.Migration) error {
	if len(m.Statements) != 1 || m.Statements[0] != "UP" || !m.NoTransaction {
		return errors.New("migration did not arrive intact")
	}
	c.recs = append(c.recs, godwit.Record{Version: m.Version, Name: m.Name, Checksum: m.Checksum,
		AppliedAt: time.Unix(1700000000, 0).UTC(), DurationMS: 4, Dirty: true})
	return nil
}
func (c *pkgConn) Revert(_ context.Context, m godwit.Migration) error {
	if len(m.Statements) != 1 || m.Statements[0] != "DOWN" {
		return errors.New("down statements did not arrive intact")
	}
	c.recs = nil
	return nil
}
func (c *pkgConn) ClearDirty(_ context.Context, v int64) error {
	for i := range c.recs {
		if c.recs[i].Version == v {
			c.recs[i].Dirty = false
		}
	}
	return nil
}

func TestPublicPackagePluginEndToEnd(t *testing.T) {
	t.Setenv(fakeEnv, "pkg")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := New(t.TempDir(), []domain.PluginSpec{{Name: "pkgdb", Command: exe}})
	if w := f.Warnings(); len(w) != 0 {
		t.Fatalf("warnings: %v", w)
	}

	r := domain.NewRegistry()
	for _, i := range f.DriverInfos() {
		r.Add(i)
	}
	if got := r.Normalize("PDB"); got != "pkgdb" {
		t.Errorf("alias = %q", got)
	}
	if i, _ := r.Info("pkgdb"); !i.NoHost {
		t.Error("NoHost not carried through")
	}
	tg, pw, err := r.ParseConnectionString("pdb", "pkgdb://db")
	if err != nil || tg.Driver != "pkgdb" || tg.Database != "db" || tg.Port != 7 || tg.Params["k"] != "v" || pw != "pw" {
		t.Fatalf("parse: %+v %q %v", tg, pw, err)
	}
	if _, _, err := r.ParseConnectionString("pkgdb", "bad"); err == nil || !strings.Contains(err.Error(), "expected pkgdb://") {
		t.Errorf("parse error: %v", err)
	}

	ctx := context.Background()
	target := domain.Target{Name: "tgt", Driver: "pkgdb", Database: "db", Params: map[string]string{"x": "y"}}
	if _, err := f.Open(ctx, domain.Target{Name: "tgt", Driver: "pkgdb"}, "secret"); err == nil {
		t.Error("open must surface the driver's error")
	}
	db, err := f.Open(ctx, target, "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.EnsureTable(ctx); err != nil {
		t.Fatal(err)
	}
	unlock, err := db.Lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Lock(ctx); err == nil || !strings.Contains(err.Error(), "already locked") {
		t.Errorf("second lock: %v", err)
	}
	m := &domain.Migration{Version: 3, Name: "n", Checksum: "c", NoTransaction: true,
		Up:   []domain.Statement{{Driver: domain.DriverAll, SQL: "UP"}, {Driver: "postgres", SQL: "PG"}},
		Down: []domain.Statement{{Driver: "pkgdb", SQL: "DOWN"}}}
	if err := db.Apply(ctx, m); err != nil {
		t.Fatal(err)
	}
	recs, err := db.Applied(ctx)
	if err != nil || len(recs) != 1 || recs[0] != (domain.Record{Version: 3, Name: "n", Checksum: "c",
		AppliedAt: time.Unix(1700000000, 0).UTC(), DurationMS: 4, Dirty: true}) {
		t.Fatalf("applied = %+v, %v", recs, err)
	}
	if err := db.ClearDirty(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if recs, _ := db.Applied(ctx); recs[0].Dirty {
		t.Error("dirty flag not cleared")
	}
	if err := db.Revert(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if u2, err := db.Lock(ctx); err != nil {
		t.Errorf("lock after unlock: %v", err)
	} else {
		u2()
	}
}

func (c *pkgConn) ReadForeign(_ context.Context, tool, table string) ([]godwit.ForeignRow, bool, error) {
	if tool != "goose" || table != "gv" {
		return nil, false, errors.New("tool or table did not arrive intact")
	}
	return []godwit.ForeignRow{{Version: 1, Applied: true, At: time.Unix(1700000000, 0).UTC()}, {Version: 2}}, true, nil
}

func (c *pkgConn) ImportRecords(_ context.Context, recs []godwit.Record) (int, error) {
	c.recs = append(c.recs, recs...)
	return len(recs), nil
}

func TestPublicPackagePluginTranslate(t *testing.T) {
	t.Setenv(fakeEnv, "pkg")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := New(t.TempDir(), []domain.PluginSpec{{Name: "pkgdb", Command: exe}})
	ctx := context.Background()
	db, err := f.Open(ctx, domain.Target{Name: "tgt", Driver: "pkgdb", Database: "db", Params: map[string]string{"x": "y"}}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	imp, ok := db.(app.ForeignImporter)
	if !ok {
		t.Fatal("plugin database is not a ForeignImporter")
	}
	rows, found, err := imp.ForeignApplied(ctx, "goose", "gv")
	if err != nil || !found || len(rows) != 2 || !rows[0].Applied || rows[0].Version != 1 || rows[1].Applied {
		t.Fatalf("rows = %+v found=%v err=%v", rows, found, err)
	}
	at := time.Unix(1700000000, 0).UTC()
	n, err := imp.ImportRecords(ctx, []domain.Record{{Version: 1, Name: "a", Checksum: "c", AppliedAt: at}})
	if err != nil || n != 1 {
		t.Fatalf("import n=%d err=%v", n, err)
	}
	if recs, _ := db.Applied(ctx); len(recs) != 1 || recs[0].Name != "a" || !recs[0].AppliedAt.Equal(at) {
		t.Errorf("applied = %+v", recs)
	}
	if _, _, err := imp.ForeignApplied(ctx, "flyway", "gv"); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Errorf("an unadvertised tool must be refused before reaching the plugin: %v", err)
	}
}
