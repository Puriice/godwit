// Package domain holds godwit's core types. It has no I/O and imports nothing
// from the rest of the module; everything else depends on it.
package domain

import (
	"slices"
	"strings"
	"time"
)

// DriverAll marks statements that run on every database driver.
const DriverAll = "all"

// Target is a database a project migrates. It never holds the password.
type Target struct {
	Name     string
	Driver   string // built-in ("postgres", "mysql") or a plugin's driver name
	Host     string
	Port     int
	Database string
	User     string
	Params   map[string]string
	// Disabled targets are skipped by every operation until re-enabled.
	Disabled bool
	// DisabledMigrations and EnabledMigrations override the project's default
	// for single versions on this target: a disabled migration is skipped
	// while pending. A version is in at most one of the two.
	DisabledMigrations []int64
	EnabledMigrations  []int64
}

// Project is the project-level configuration.
type Project struct {
	MigrationsDir string
	Targets       []Target
	Plugins       []PluginSpec
	// DisabledMigrations is the default for every target: these versions are
	// skipped while pending unless a target overrides it.
	DisabledMigrations []int64
}

// MigrationDisabled reports whether version is disabled on t, honouring the
// target's override before the project default.
func (p Project) MigrationDisabled(t Target, version int64) bool {
	switch {
	case slices.Contains(t.DisabledMigrations, version):
		return true
	case slices.Contains(t.EnabledMigrations, version):
		return false
	}
	return slices.Contains(p.DisabledMigrations, version)
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
	// Disabled migrations are skipped when applying to this target.
	Disabled bool
	// Reason says where Disabled comes from, or notes an override of the
	// project default; empty when neither applies.
	Reason string
}

// Direction of a migration run.
type Direction string

const (
	Up   Direction = "up"
	Down Direction = "down"
	// Redo is a run that reverts one migration and applies it again; its
	// progress events are reported as Down followed by Up.
	Redo Direction = "redo"
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
