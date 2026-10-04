package godwit

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// memDriver keeps the state table in memory.
type memDriver struct{ conn *memConn }

func (memDriver) Info() Info {
	return Info{Name: "MemDB", Aliases: []string{"mem"}, Schemes: []string{"memdb"}, NoHost: true}
}

func (memDriver) ParseConnection(conn string) (Target, string, error) {
	name, ok := strings.CutPrefix(conn, "memdb://")
	if !ok {
		return Target{}, "", errors.New("expected memdb://<name>")
	}
	return Target{Database: name}, "pw", nil
}

func (d *memDriver) Open(_ context.Context, t Target, _ string) (Connection, error) {
	if t.Database == "missing" {
		return nil, errors.New("no such database")
	}
	d.conn = &memConn{}
	return d.conn, nil
}

type memConn struct {
	recs                []Record
	locked, closed      bool
	lastApply           Migration
	panicOnEnsure, fail bool
}

func (c *memConn) Close() error { c.closed = true; return nil }
func (c *memConn) EnsureTable(context.Context) error {
	if c.panicOnEnsure {
		panic("kaboom")
	}
	return nil
}
func (c *memConn) Lock(context.Context) (func() error, error) {
	c.locked = true
	return func() error { c.locked = false; return nil }, nil
}
func (c *memConn) Applied(context.Context) ([]Record, error) { return c.recs, nil }
func (c *memConn) Apply(_ context.Context, m Migration) error {
	if c.fail {
		return errors.New("syntax error")
	}
	c.lastApply = m
	c.recs = append(c.recs, Record{Version: m.Version, Name: m.Name, Checksum: m.Checksum,
		AppliedAt: time.Unix(1700000000, 0).UTC(), DurationMS: 3})
	return nil
}
func (c *memConn) Revert(_ context.Context, m Migration) error {
	c.recs = c.recs[:0]
	return nil
}
func (c *memConn) ClearDirty(context.Context, int64) error { return nil }

// session feeds requests to ServeIO and returns the decoded responses.
func session(t *testing.T, d Driver, reqs ...string) []map[string]any {
	t.Helper()
	var out strings.Builder
	if err := ServeIO(strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out, d); err != nil {
		t.Fatal(err)
	}
	var resps []map[string]any
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("bad response line %q: %v", sc.Text(), err)
		}
		resps = append(resps, m)
	}
	if len(resps) != len(reqs) {
		t.Fatalf("got %d responses for %d requests:\n%s", len(resps), len(reqs), out.String())
	}
	return resps
}

func errOf(r map[string]any) string { s, _ := r["error"].(string); return s }

func TestHandshakeAndParse(t *testing.T) {
	r := session(t, &memDriver{},
		`{"id":1,"method":"handshake","params":{"protocol":1}}`,
		`{"id":2,"method":"parse_connection","params":{"connstring":"memdb://x"}}`,
		`{"id":3,"method":"parse_connection","params":{"connstring":"nope"}}`,
	)
	hs := r[0]["result"].(map[string]any)
	if hs["protocol"] != float64(ProtocolVersion) || hs["driver"] != "memdb" || hs["noHost"] != true || hs["aliases"].([]any)[0] != "mem" {
		t.Errorf("handshake = %v", hs)
	}
	res := r[1]["result"].(map[string]any)
	if res["password"] != "pw" || res["target"].(map[string]any)["database"] != "x" {
		t.Errorf("parse = %v", res)
	}
	if got := errOf(r[2]); got != "expected memdb://<name>" || r[2]["id"] != float64(3) {
		t.Errorf("parse error response = %v", r[2])
	}
}

func TestLifecycleAndCleanup(t *testing.T) {
	d := &memDriver{}
	r := session(t, d,
		`{"id":1,"method":"applied"}`, // before open
		`{"id":2,"method":"open","params":{"target":{"driver":"memdb","database":"a"},"password":"x"}}`,
		`{"id":3,"method":"open","params":{"target":{"driver":"memdb","database":"a"}}}`,
		`{"id":4,"method":"ensure_table"}`,
		`{"id":5,"method":"lock"}`,
		`{"id":6,"method":"lock"}`,
		`{"id":7,"method":"apply","params":{"migration":{"version":9,"name":"n","checksum":"c","noTransaction":true,"statements":["A","B"]}}}`,
		`{"id":8,"method":"applied"}`,
		`{"id":9,"method":"bogus"}`,
	)
	if !strings.Contains(errOf(r[0]), "not open") || !strings.Contains(errOf(r[2]), "already open") ||
		!strings.Contains(errOf(r[5]), "already locked") || !strings.Contains(errOf(r[8]), "unknown method") {
		t.Errorf("state errors: %v", r)
	}
	for _, i := range []int{1, 3, 4, 6, 7} {
		if e := errOf(r[i]); e != "" {
			t.Errorf("response %d: %s", i+1, e)
		}
	}
	m := d.conn.lastApply
	if m.Version != 9 || m.Name != "n" || m.Checksum != "c" || !m.NoTransaction || len(m.Statements) != 2 || m.Statements[1] != "B" {
		t.Errorf("migration = %+v", m)
	}
	rec := r[7]["result"].(map[string]any)["records"].([]any)[0].(map[string]any)
	if rec["version"] != float64(9) || rec["durationMs"] != float64(3) || rec["appliedAt"] != "2023-11-14T22:13:20Z" {
		t.Errorf("record = %v", rec)
	}
	// Input ended while locked and open: both must have been released.
	if d.conn.locked || !d.conn.closed {
		t.Errorf("not cleaned up: locked=%v closed=%v", d.conn.locked, d.conn.closed)
	}
}

func TestPanicIsContained(t *testing.T) {
	r := session(t, panicDriver{},
		`{"id":1,"method":"open","params":{"target":{"driver":"p"}}}`,
		`{"id":2,"method":"ensure_table"}`,
		`{"id":3,"method":"ensure_table"}`,
	)
	// A panic becomes an error response, and the process keeps serving.
	if errOf(r[0]) != "" || !strings.Contains(errOf(r[1]), "internal error: kaboom") || !strings.Contains(errOf(r[2]), "internal error") {
		t.Errorf("panic not contained: %v", r)
	}
}

type panicDriver struct{ memDriver }

func (panicDriver) Open(context.Context, Target, string) (Connection, error) {
	return &memConn{panicOnEnsure: true}, nil
}

func TestErrorsStayUsable(t *testing.T) {
	d := &memDriver{}
	r := session(t, d,
		`{"id":1,"method":"open","params":{"target":{"driver":"memdb","database":"missing"}}}`,
		`{"id":2,"method":"open","params":{"target":{"driver":"memdb","database":"ok"}}}`,
		`{"id":3,"method":"unlock"}`,
		`{"id":4,"method":"ensure_table"}`,
		`not json`,
		`{"id":6,"method":"close"}`,
		`{"id":7,"method":"ensure_table"}`,
	)
	if errOf(r[0]) != "no such database" || errOf(r[1]) != "" || errOf(r[2]) != "not locked" || errOf(r[3]) != "" {
		t.Errorf("responses = %v", r[:4])
	}
	if !strings.Contains(errOf(r[4]), "invalid request") {
		t.Errorf("garbage line: %v", r[4])
	}
	if errOf(r[5]) != "" || !strings.Contains(errOf(r[6]), "not open") {
		t.Errorf("close then use: %v %v", r[5], r[6])
	}
	if !d.conn.closed {
		t.Error("close did not close the connection")
	}
}

func TestLongLinesAndCRLF(t *testing.T) {
	big := strings.Repeat("x", 3<<20) // far beyond bufio.Scanner's default
	var out strings.Builder
	in := `{"id":1,"method":"parse_connection","params":{"connstring":"memdb://` + big + "\"}}\r\n"
	if err := ServeIO(strings.NewReader(in), &out, &memDriver{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"id":1`) || strings.Contains(out.String(), "error") {
		t.Errorf("long CRLF request failed: %.200s", out.String())
	}
}

func TestEOFWithoutNewline(t *testing.T) {
	var out strings.Builder
	if err := ServeIO(strings.NewReader(`{"id":1,"method":"handshake"}`), &out, &memDriver{}); err != nil || !strings.Contains(out.String(), `"id":1`) {
		t.Errorf("err=%v out=%q", err, out.String())
	}
}

func TestWriteFailureStopsServing(t *testing.T) {
	err := ServeIO(strings.NewReader(`{"id":1,"method":"handshake"}`+"\n"), failWriter{}, &memDriver{})
	if err == nil {
		t.Error("write error should end the session")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
