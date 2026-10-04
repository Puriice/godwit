package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/puriice/godwit/internal/app"
	"github.com/puriice/godwit/internal/domain"
)

const fakeEnv = "GODWIT_FAKE_PLUGIN"

// The test binary doubles as the plugin: with fakeEnv set it speaks the
// protocol instead of running tests, so no external tooling is needed on any OS.
func TestMain(m *testing.M) {
	if isFakeGo() {
		runFakeGo()
		return
	}
	if mode := os.Getenv(fakeEnv); mode != "" {
		if mode == "pkg" {
			runPkgPlugin()
			return
		}
		runFake(mode)
		return
	}
	os.Exit(m.Run())
}

func runFake(mode string) {
	driver, protocol := "fakedb", ProtocolVersion
	switch mode {
	case "oldproto":
		protocol = 99
	case "builtin":
		driver = "postgres"
	}
	var (
		records []wireRecord
		locked  bool
		params  map[string]string
	)
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(nil, 1<<20)
	out := json.NewEncoder(os.Stdout)
	for in.Scan() {
		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		json.Unmarshal(in.Bytes(), &req)
		reply := func(result any, errMsg string) {
			resp := map[string]any{"id": req.ID}
			if errMsg != "" {
				resp["error"] = errMsg
			} else if result != nil {
				resp["result"] = result
			}
			out.Encode(resp)
		}
		switch req.Method {
		case "handshake":
			reply(handshakeResult{Protocol: protocol, Driver: driver, Aliases: []string{"fdb"}, Schemes: []string{"fakedb"}, NoHost: true}, "")
		case "parse_connection":
			var p parseParams
			json.Unmarshal(req.Params, &p)
			path, ok := strings.CutPrefix(p.ConnString, "fakedb://")
			if !ok {
				reply(nil, "expected fakedb://<file>?pw=<password>")
				continue
			}
			path, pw, _ := strings.Cut(path, "?pw=")
			reply(parseResult{Target: wireTarget{Driver: driver, Database: path}, Password: pw}, "")
		case "open":
			var p openParams
			json.Unmarshal(req.Params, &p)
			params = p.Target.Params
			if p.Password != "secret" {
				reply(nil, "bad password")
				continue
			}
			reply(struct{}{}, "")
		case "lock":
			if locked {
				reply(nil, "already locked")
				continue
			}
			locked = true
			reply(struct{}{}, "")
		case "unlock":
			locked = false
			reply(struct{}{}, "")
		case "ensure_table", "close":
			reply(struct{}{}, "")
		case "applied":
			reply(appliedResult{Records: records}, "")
		case "apply":
			if params["crash"] == "apply" {
				os.Exit(3)
			}
			var p migrationParams
			json.Unmarshal(req.Params, &p)
			if len(p.Migration.Statements) == 0 {
				reply(nil, "no statements")
				continue
			}
			records = append(records, wireRecord{Version: p.Migration.Version, Name: p.Migration.Name,
				Checksum: p.Migration.Checksum, AppliedAt: time.Unix(1700000000, 0).UTC(), DurationMS: 7})
			reply(struct{}{}, "")
		case "revert":
			var p migrationParams
			json.Unmarshal(req.Params, &p)
			for i, r := range records {
				if r.Version == p.Migration.Version {
					records = append(records[:i], records[i+1:]...)
					break
				}
			}
			reply(struct{}{}, "")
		case "clear_dirty":
			reply(struct{}{}, "")
		default:
			reply(nil, fmt.Sprintf("unknown method %q", req.Method))
		}
	}
	if err := in.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "fake plugin:", err)
		os.Exit(1)
	}
}

func fakeFactory(t *testing.T, mode string) *Factory {
	t.Helper()
	t.Setenv(fakeEnv, mode)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return New(t.TempDir(), []domain.PluginSpec{{Name: "fakedb", Command: exe}})
}

func TestHandshakeAndRegistry(t *testing.T) {
	f := fakeFactory(t, "ok")
	if w := f.Warnings(); len(w) != 0 {
		t.Fatalf("warnings: %v", w)
	}
	if got := f.Drivers(); len(got) != 1 || got[0] != "fakedb" {
		t.Fatalf("drivers = %v", got)
	}

	r := domain.NewRegistry()
	for _, i := range f.DriverInfos() {
		r.Add(i)
	}
	if got := r.Normalize("FDB"); got != "fakedb" {
		t.Errorf("alias normalize = %q", got)
	}
	tg, pw, err := r.ParseConnectionString("fdb", "fakedb://data/app.db?pw=secret")
	if err != nil {
		t.Fatal(err)
	}
	if tg.Driver != "fakedb" || tg.Database != "data/app.db" || tg.Host != "" || pw != "secret" {
		t.Errorf("parsed %+v pw=%q", tg, pw)
	}
	if _, _, err := r.ParseConnectionString("fakedb", "nonsense"); err == nil || !strings.Contains(err.Error(), "expected fakedb://") {
		t.Errorf("plugin parse error not surfaced: %v", err)
	}
	if i, _ := r.Info("fakedb"); !i.NoHost {
		t.Error("NoHost lost")
	}
}

func TestLifecycle(t *testing.T) {
	f := fakeFactory(t, "ok")
	ctx := context.Background()
	db, err := app.Combine(f).Open(ctx, domain.Target{Name: "x", Driver: "FakeDB"}, "secret")
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
	m := &domain.Migration{Version: 5, Name: "init", Checksum: "abc", Up: []domain.Statement{
		{Driver: domain.DriverAll, SQL: "CREATE TABLE a"},
		{Driver: "postgres", SQL: "ignored"},
	}}
	if err := db.Apply(ctx, m); err != nil {
		t.Fatal(err)
	}
	recs, err := db.Applied(ctx)
	if err != nil || len(recs) != 1 || recs[0].Version != 5 || recs[0].Checksum != "abc" || recs[0].DurationMS != 7 {
		t.Fatalf("applied = %+v, %v", recs, err)
	}
	if err := db.Revert(ctx, m); err == nil || !strings.Contains(err.Error(), "no Down statements") {
		t.Errorf("revert without down: %v", err)
	}
	m.Down = []domain.Statement{{Driver: domain.DriverAll, SQL: "DROP TABLE a"}}
	if err := db.Revert(ctx, m); err != nil {
		t.Fatal(err)
	}
	if recs, _ := db.Applied(ctx); len(recs) != 0 {
		t.Errorf("after revert: %+v", recs)
	}
	if err := db.ClearDirty(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	// The lock really was released in the plugin: taking it again works.
	unlock, err = db.Lock(ctx)
	if err != nil {
		t.Fatalf("relock: %v", err)
	}
	unlock()
}

func TestRemoteErrorKeepsConnection(t *testing.T) {
	f := fakeFactory(t, "ok")
	ctx := context.Background()
	if _, err := f.Open(ctx, domain.Target{Driver: "fakedb"}, "wrong"); err == nil || !strings.Contains(err.Error(), "bad password") {
		t.Fatalf("open with bad password: %v", err)
	}
	db, err := f.Open(ctx, domain.Target{Driver: "fakedb"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// An error from the plugin must not poison later calls.
	if err := db.Apply(ctx, &domain.Migration{Version: 1}); err == nil || !strings.Contains(err.Error(), "no statements") {
		t.Fatalf("apply: %v", err)
	}
	if err := db.EnsureTable(ctx); err != nil {
		t.Fatalf("connection should survive a plugin error: %v", err)
	}
}

func TestPluginCrash(t *testing.T) {
	f := fakeFactory(t, "ok")
	ctx := context.Background()
	db, err := f.Open(ctx, domain.Target{Driver: "fakedb", Params: map[string]string{"crash": "apply"}}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := &domain.Migration{Version: 1, Up: []domain.Statement{{Driver: domain.DriverAll, SQL: "x"}}}
	err = db.Apply(ctx, m)
	if err == nil || !strings.Contains(err.Error(), "connection lost") {
		t.Fatalf("crash error = %v", err)
	}
	if err2 := db.Apply(ctx, m); err2 == nil {
		t.Error("calls after a crash must keep failing, not hang")
	}
}

func TestCancelledContextFailsCall(t *testing.T) {
	f := fakeFactory(t, "ok")
	db, err := f.Open(context.Background(), domain.Target{Driver: "fakedb"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := db.EnsureTable(ctx); err == nil {
		t.Error("cancelled context should fail the call")
	}
	if err := db.EnsureTable(context.Background()); err != nil {
		t.Errorf("a call refused before sending must not kill the plugin: %v", err)
	}
}

func TestRejectedPlugins(t *testing.T) {
	for mode, want := range map[string]string{
		"oldproto": "protocol 99",
		"builtin":  "built in",
	} {
		f := fakeFactory(t, mode)
		if len(f.Drivers()) != 0 {
			t.Errorf("%s: plugin should not load", mode)
		}
		if w := f.Warnings(); len(w) != 1 || !strings.Contains(w[0], want) {
			t.Errorf("%s: warnings = %v", mode, w)
		}
	}
}

func TestMissingExecutable(t *testing.T) {
	f := New(t.TempDir(), []domain.PluginSpec{{Name: "nope", Command: "godwit-driver-does-not-exist"}})
	if w := f.Warnings(); len(w) != 1 || !strings.Contains(w[0], "cannot find") {
		t.Errorf("warnings = %v", w)
	}
}

func TestOpenUnknownDriver(t *testing.T) {
	f := fakeFactory(t, "ok")
	if _, err := f.Open(context.Background(), domain.Target{Driver: "oracle"}, ""); err == nil {
		t.Error("expected unknown driver error")
	}
}
