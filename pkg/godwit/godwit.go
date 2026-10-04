// Package godwit lets you write a godwit driver plugin in Go.
//
// A plugin is an executable that godwit starts and talks to over stdin and
// stdout (see docs/plugins.md for the wire protocol). This package hides the
// protocol: implement Driver and Connection for your database and call Serve
// from main. Users add the built plugin with "godwit plugin add <command>", or
// install it straight from your repository with "godwit plugin install <url>"
// if it is a main package that "go install" can build.
//
//	func main() {
//		if err := godwit.Serve(myDriver{}); err != nil {
//			fmt.Fprintln(os.Stderr, err)
//			os.Exit(1)
//		}
//	}
//
// Connection mirrors godwit's own database port, so the contract is the same
// one the built-in postgres and mysql drivers satisfy.
//
// # Rules
//
// Never write to standard output yourself: it carries the protocol. Serve
// redirects os.Stdout to os.Stderr while it runs, so stray fmt.Println calls
// become log lines instead of corrupting the stream. Log to stderr.
//
// godwit runs one plugin process per open database and calls it one request at
// a time, so a Driver needs no locking of its own. When godwit is done, or
// cancels a call, it closes stdin or kills the process; Serve then releases a
// held lock and closes the connection before returning.
package godwit

import (
	"context"
	"time"
)

// ProtocolVersion is the wire protocol version this package speaks. godwit
// rejects plugins whose version differs from its own.
const ProtocolVersion = 1

// Info describes the driver to godwit. It is sent in the handshake.
type Info struct {
	// Name is the driver name used in targets and in "-- +godwit driver:"
	// directives. It must be lower case, and must not be "postgres" or "mysql".
	Name string
	// Aliases are extra names that mean the same driver.
	Aliases []string
	// Schemes are the URL schemes your connection strings use. They are
	// informational; ParseConnection does the real parsing.
	Schemes []string
	// NoHost marks databases without a network endpoint (a file, for example).
	// godwit then asks for no host, user or password.
	NoHost bool
}

// Target is a database to migrate. Every field is optional except Driver.
type Target struct {
	Name     string // godwit's name for the target; set on Open only
	Driver   string
	Host     string
	Port     int
	Database string
	User     string
	Params   map[string]string
}

// Record is one row of the godwit_migration state table.
type Record struct {
	Version    int64
	Name       string
	Checksum   string
	AppliedAt  time.Time
	DurationMS int64
	Dirty      bool
}

// Migration is one migration to apply or revert.
type Migration struct {
	Version  int64
	Name     string
	Checksum string
	// NoTransaction asks you not to wrap the statements in a transaction.
	NoTransaction bool
	// Statements are the SQL statements for this driver, already selected and
	// in order. For Apply they are the Up statements, for Revert the Down ones.
	Statements []string
}

// Driver is the plugin's entry point: it describes the database engine, parses
// its connection strings and opens connections.
type Driver interface {
	// Info describes the driver.
	Info() Info

	// ParseConnection turns the text a user passed to
	// "godwit auth add <name> <driver> <connection string>" into a Target and a
	// password. The password is returned separately so godwit keeps it out of
	// config.json. You own the syntax and the validation. Never include the
	// connection string, which holds the password, in an error message.
	ParseConnection(conn string) (t Target, password string, err error)

	// Open connects to the target. It is called once per process.
	Open(ctx context.Context, t Target, password string) (Connection, error)
}

// Connection is an open database. It owns the target's godwit_migration state
// table, with these columns (adapt the types to your database):
//
//	version      integer primary key
//	name         text
//	checksum     text, 64 characters
//	applied_at   timestamp, default now
//	duration_ms  integer
//	dirty        boolean
type Connection interface {
	// Close releases the connection.
	Close() error

	// EnsureTable creates the state table if it does not exist.
	EnsureTable(ctx context.Context) error

	// Lock takes a database-wide migration lock so two godwit runs cannot
	// migrate the same database at once, and returns the function that releases
	// it. It should fail rather than wait forever. Databases without locking
	// may return a no-op.
	Lock(ctx context.Context) (unlock func() error, err error)

	// Applied returns every row of the state table, ordered by version.
	Applied(ctx context.Context) ([]Record, error)

	// Apply runs m.Statements and inserts m's row into the state table. If the
	// database can roll back DDL and m.NoTransaction is false, do both in one
	// transaction. Otherwise insert the row with Dirty set first and clear the
	// flag once the statements succeed, so a failure leaves a visible dirty row
	// rather than a half-applied migration nobody knows about.
	Apply(ctx context.Context, m Migration) error

	// Revert runs m.Statements and deletes m's row. Without transactional DDL,
	// set the row dirty first, run the statements, then delete it.
	Revert(ctx context.Context, m Migration) error

	// ClearDirty clears a version's dirty flag after a manual repair.
	ClearDirty(ctx context.Context, version int64) error
}
