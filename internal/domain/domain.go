// Package domain holds godwit's core types. It has no I/O and imports nothing
// from the rest of the module; everything else depends on it.
package domain

import (
	"strings"
	"time"
)

// DriverAll marks statements that run on every database driver.
const DriverAll = "all"

// Target is a database a project migrates. It never holds the password.
type Target struct {
	Name     string
	Driver   string // "postgres" | "mysql"
	Host     string
	Port     int
	Database string
	User     string
	Params   map[string]string
}

// Project is the project-level configuration.
type Project struct {
	MigrationsDir string
	Targets       []Target
}

// Target returns the named target.
func (p Project) Target(name string) (Target, bool) {
	for _, t := range p.Targets {
		if t.Name == name {
			return t, true
		}
	}
	return Target{}, false
}

// Statement is one SQL statement scoped to a driver.
type Statement struct {
	Driver string // lower-case driver name, or DriverAll
	SQL    string
}

// Migration is one versioned schema change.
type Migration struct {
	Version       int64
	Name          string
	Source        string // where it came from (a file path); informational
	Checksum      string // identifies the exact contents that were applied
	Up            []Statement
	Down          []Statement
	NoTransaction bool
}

// UpSQL returns the Up statements that apply to driver, in order.
func (m *Migration) UpSQL(driver string) []string { return statementsFor(m.Up, driver) }

// DownSQL returns the Down statements that apply to driver, in order.
func (m *Migration) DownSQL(driver string) []string { return statementsFor(m.Down, driver) }

func statementsFor(stmts []Statement, driver string) []string {
	driver = strings.ToLower(driver)
	var out []string
	for _, s := range stmts {
		if s.Driver == DriverAll || s.Driver == driver {
			out = append(out, s.SQL)
		}
	}
	return out
}

// Record is a migration as recorded in a target's godwit_migration table.
type Record struct {
	Version    int64
	Name       string
	Checksum   string
	AppliedAt  time.Time
	DurationMS int64
	Dirty      bool
}

// State describes a migration relative to a target.
type State string

const (
	Pending  State = "pending"
	Applied  State = "applied"
	Modified State = "modified" // applied, but the file's checksum changed
	Dirty    State = "dirty"    // a previous run failed part-way
	Missing  State = "missing"  // recorded in the target, no file on disk
)

// Item is one row of a target's migration status.
type Item struct {
	Version   int64
	Name      string
	State     State
	Migration *Migration // nil when Missing
	Record    *Record    // nil when Pending
}

// Direction of a migration run.
type Direction string

const (
	Up   Direction = "up"
	Down Direction = "down"
)

// Phase of a single migration within a run.
type Phase string

const (
	Started Phase = "started"
	Done    Phase = "done"
	Failed  Phase = "failed"
)

// Event reports progress of a run.
type Event struct {
	Direction Direction
	Version   int64
	Name      string
	Phase     Phase
	Err       error
}
