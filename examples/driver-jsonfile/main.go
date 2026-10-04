// Command driver-jsonfile is a toy godwit driver plugin built on pkg/godwit,
// and a reference for writing your own. It "migrates" a JSON file: applied
// migrations are recorded in the file and their statements are appended to a
// log, instead of being executed by a real database.
//
//	go build -o godwit-driver-jsonfile ./examples/driver-jsonfile
//	godwit plugin add jsonfile ./godwit-driver-jsonfile
//	godwit auth add local jsonfile jsonfile://state.json
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/puriice/godwit/pkg/godwit"
)

func main() {
	if err := godwit.Serve(driver{}); err != nil {
		fmt.Fprintln(os.Stderr, "driver-jsonfile:", err)
		os.Exit(1)
	}
}

// driver describes the engine and opens connections.
type driver struct{}

func (driver) Info() godwit.Info {
	// NoHost: the "database" is a file, so godwit asks for no host, user or password.
	return godwit.Info{Name: "jsonfile", Schemes: []string{"jsonfile"}, NoHost: true}
}

// ParseConnection owns the syntax of "godwit auth add <name> jsonfile <conn>".
func (driver) ParseConnection(conn string) (godwit.Target, string, error) {
	file, ok := strings.CutPrefix(conn, "jsonfile://")
	if !ok || file == "" {
		return godwit.Target{}, "", errors.New("expected jsonfile://<file>")
	}
	return godwit.Target{Database: file}, "", nil
}

func (driver) Open(_ context.Context, t godwit.Target, _ string) (godwit.Connection, error) {
	if t.Database == "" {
		return nil, errors.New("target has no database (file name)")
	}
	return &conn{path: t.Database}, nil
}

// state is the whole "database": godwit's state table plus a log of the
// statements a real database would have executed.
type state struct {
	Records []godwit.Record `json:"records"`
	Log     []string        `json:"log"`
}

type conn struct{ path string }

func (c *conn) load() (s state, err error) {
	raw, err := os.ReadFile(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(raw, &s)
}

func (c *conn) save(s state) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, raw, 0o644)
}

func (c *conn) Close() error { return nil }

func (c *conn) EnsureTable(context.Context) error {
	s, err := c.load()
	if err != nil {
		return err
	}
	return c.save(s)
}

// Lock fails instead of waiting if another run holds the lock, as godwit
// expects. A lock file is the file-database equivalent of an advisory lock.
func (c *conn) Lock(context.Context) (func() error, error) {
	lock := c.path + ".lock"
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("another migration holds %s", lock)
	}
	if err != nil {
		return nil, err
	}
	f.Close()
	return func() error { return os.Remove(lock) }, nil
}

func (c *conn) Applied(context.Context) ([]godwit.Record, error) {
	s, err := c.load()
	return s.Records, err
}

// Apply follows the dirty-flag protocol for engines without transactional DDL:
// record the attempt as dirty first, run the statements, then clear the flag.
// A failure in between leaves a visible dirty row.
func (c *conn) Apply(_ context.Context, m godwit.Migration) error {
	start := time.Now()
	s, err := c.load()
	if err != nil {
		return err
	}
	s.Records = append(s.Records, godwit.Record{
		Version: m.Version, Name: m.Name, Checksum: m.Checksum, AppliedAt: start.UTC(), Dirty: true,
	})
	if err := c.save(s); err != nil {
		return err
	}

	s.Log = append(s.Log, m.Statements...) // "execute" the statements

	last := &s.Records[len(s.Records)-1]
	last.Dirty, last.DurationMS = false, time.Since(start).Milliseconds()
	return c.save(s)
}

// Revert mirrors Apply: mark dirty, run the statements, then delete the row.
func (c *conn) Revert(_ context.Context, m godwit.Migration) error {
	s, err := c.load()
	if err != nil {
		return err
	}
	for i := range s.Records {
		if s.Records[i].Version == m.Version {
			s.Records[i].Dirty = true
		}
	}
	if err := c.save(s); err != nil {
		return err
	}

	s.Log = append(s.Log, m.Statements...)

	kept := s.Records[:0]
	for _, r := range s.Records {
		if r.Version != m.Version {
			kept = append(kept, r)
		}
	}
	s.Records = kept
	return c.save(s)
}

func (c *conn) ClearDirty(_ context.Context, version int64) error {
	s, err := c.load()
	if err != nil {
		return err
	}
	for i := range s.Records {
		if s.Records[i].Version == version {
			s.Records[i].Dirty = false
		}
	}
	return c.save(s)
}
