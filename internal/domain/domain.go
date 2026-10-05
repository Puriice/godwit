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
	// Group is the id of the RepeatStart/RepeatEnd block the statement belongs
	// to, or 0 for a statement that runs once. Ids are unique within a section.
	Group int
	// Condition marks the block's RepeatCondition query rather than a body
	// statement. It is only set when Group is non-zero.
	Condition bool
	// Delay is the block's delay between passes (from RepeatStart), repeated on
	// each of its statements; zero when there is none.
	Delay time.Duration
}

// Step is what runs as one unit: a single statement, or a repeat block.
type Step struct {
	SQL []string // the statement, or the block's body in order
	// Repeat marks a block, which runs again and again; see Condition.
	Repeat bool
	// Condition is the block's RepeatCondition query, or empty. With one, the
	// block runs while the query's first value is truthy; without one, until an
	// iteration affects no rows.
	Condition string
	// Delay is how long to wait between one pass and the next.
	Delay time.Duration
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
	// Batch is the run this migration is being applied in. The service sets it
	// just before applying; migration files do not carry one.
	Batch int64
}

// UpSQL returns the Up statements that apply to driver, in order.
func (m *Migration) UpSQL(driver string) []string { return statementsFor(m.Up, driver) }

// DownSQL returns the Down statements that apply to driver, in order.
func (m *Migration) DownSQL(driver string) []string { return statementsFor(m.Down, driver) }

// UpSteps returns the Up steps that apply to driver, in order.
func (m *Migration) UpSteps(driver string) []Step { return stepsFor(m.Up, driver) }

// DownSteps returns the Down steps that apply to driver, in order.
func (m *Migration) DownSteps(driver string) []Step { return stepsFor(m.Down, driver) }

// UpHasRepeat reports whether Up contains a repeat block.
func (m *Migration) UpHasRepeat() bool { return hasRepeat(m.Up) }

// DownHasRepeat reports whether Down contains a repeat block.
func (m *Migration) DownHasRepeat() bool { return hasRepeat(m.Down) }

func hasRepeat(stmts []Statement) bool {
	return slices.ContainsFunc(stmts, func(s Statement) bool { return s.Group != 0 })
}

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

// stepsFor groups the statements that apply to driver into steps. A repeat
// block whose body has no statement for driver is dropped.
func stepsFor(stmts []Statement, driver string) []Step {
	driver = strings.ToLower(driver)
	var out []Step
	cur := -1 // index in out of the open block, while its statements go by
	group := 0
	for _, s := range stmts {
		if s.Driver != DriverAll && s.Driver != driver {
			continue
		}
		if s.Group == 0 {
			out = append(out, Step{SQL: []string{s.SQL}})
			group, cur = 0, -1
			continue
		}
		if s.Group != group || cur < 0 {
			out = append(out, Step{Repeat: true, Delay: s.Delay})
			group, cur = s.Group, len(out)-1
		}
		if s.Condition {
			out[cur].Condition = s.SQL
		} else {
			out[cur].SQL = append(out[cur].SQL, s.SQL)
		}
	}
	// A block left with only a condition has no body to run.
	return slices.DeleteFunc(out, func(st Step) bool { return st.Repeat && len(st.SQL) == 0 })
}

// Record is a migration as recorded in a target's godwit_migration table.
type Record struct {
	Version    int64
	Name       string
	Checksum   string
	AppliedAt  time.Time
	DurationMS int64
	Dirty      bool
	// Batch numbers the apply run that recorded this migration. Rows written
	// before batches existed (or by drivers that do not store one) have 0.
	Batch int64
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
