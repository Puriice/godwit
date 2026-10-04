# Writing a driver plugin

A plugin teaches godwit a database it does not support. It is an ordinary
executable. godwit starts it, writes **requests** to its stdin and reads
**responses** from its stdout. It works the same on Windows, macOS and Linux.

A complete example, built on the Go package described below, is in
[`examples/driver-jsonfile`](../examples/driver-jsonfile/main.go). It follows the
dirty-flag protocol and takes a real lock, so it is a good template.

## Writing a plugin in Go

Go plugins do not need to speak the protocol by hand. The public package
[`pkg/godwit`](../pkg/godwit) provides the interfaces and runs the
protocol for you:

```go
import "github.com/puriice/godwit/pkg/godwit"

func main() {
	if err := godwit.Serve(myDriver{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

Implement two interfaces:

- `godwit.Driver`: `Info()` (name, aliases, `NoHost`), `ParseConnection(conn)`
  and `Open(ctx, target, password)`.
- `godwit.Connection`: `Close`, `EnsureTable`, `Lock`, `Applied`, `Apply`,
  `Revert` and `ClearDirty`. This is the same contract as godwit's own database
  port, and the Go doc comments spell out the rules (the state table columns,
  how to use the dirty flag).

`Serve` handles the framing, request ids, `handshake`, lock bookkeeping and
cleanup when godwit closes the plugin. A panic in your code becomes an error
response instead of killing the process. It also points `os.Stdout` at
`os.Stderr`, so a stray `fmt.Println` cannot corrupt the protocol. Use
`ServeIO(r, w, driver)` to test a driver in-process without starting a binary.

The rest of this page describes the wire protocol, which you need only if you
write a plugin in another language or want to know what `Serve` does.

## Installing

Two commands register a plugin. Both start it once to check it before saving.

```
godwit plugin install [-g|-G] <url> [name] [-- args...]   # build from a Go package, then add
godwit plugin add [-g|-G] <command> [name] [-- args...]   # add an executable you already have
```

By default a plugin belongs to the current project only. Two flags change where
it is registered:

| Flag | Registered in | Binary built by `install` goes to |
|---|---|---|
| (none) | this project, `.godwit/config.json` | `<project>/.godwit/plugins/` |
| `-g` | your user, `~/.godwit/config.json` | `~/.godwit/plugins/` |
| `-G` | both | `~/.godwit/plugins/` |

A global plugin is available in every project, so you install it once. A project
entry replaces a global one with the same name, which lets one repository pin a
different build. `plugin remove` takes the same flags (`-G` removes it from
wherever it is).

`plugin list` groups plugins by where they are registered. A global plugin that
a project plugin replaces is marked with `✗` and `overridden by project`, and on
a terminal it is also dimmed and struck through, so it is clear it has no effect
in this project:

```
Project plugins
    jsonfile  ./tool -v

Global plugins (~/.godwit)
  ✗ jsonfile  /home/me/bin/jsonfile  overridden by project
    duck      duck
```

The marker is plain text, so it still shows when the output is piped or the
terminal has no color. An empty section prints `(none)`.
`-G` is all or nothing: if the project already has a different plugin by that
name, nothing is changed.

A relative path given with `-g` or `-G` is stored as an absolute path in the
global config, since it could not mean the same thing from every project. The
project's own entry keeps what you typed.

**`install`** runs `go install <url>` with `GOBIN` set to
`<project>/.godwit/plugins/`, then registers the result. `<url>` is a Go package
path to a `main` package, optionally with a version, for example
`github.com/you/godwit-driver-sqlite@v1.2.0`. Without a version it installs
`@latest`. It needs the Go toolchain on `PATH`. Running it again updates the
plugin. The directory gets a `.gitignore` so the binaries are not committed.

**`add`** takes a command that already exists, for plugins written in other
languages or built some other way.

`name` is what the plugin is called in `plugin list` and `plugin remove`. It is
optional and defaults to the driver name the plugin reports in its handshake. If
you give one, it may differ from the driver name; targets always use the driver
name. Anything after `--` is passed to the plugin as command-line arguments.

`go install` compiles code from the network and godwit then runs it, with your
privileges, so only install plugins you trust. godwit does not verify
signatures; pin a version (`@v1.2.0`) for repeatable installs.

`<command>` is resolved as follows:

- If it contains a path separator, it is a path, relative to the project root
  unless absolute.
- Otherwise godwit looks for it in `<project>/.godwit/plugins/`, then in
  `~/.godwit/plugins/`, then on `PATH`. On Windows the usual `PATHEXT` rules apply, so `godwit-driver-x` finds
  `godwit-driver-x.exe`.

The plugin is stored in `.godwit/config.json`:

```json
{ "plugins": [{ "name": "sqlite", "command": "godwit-driver-sqlite", "args": [] }] }
```

Ship one binary per OS and architecture. A plugin that fails to start is
skipped with a warning; the built-in drivers keep working. A plugin cannot
replace `postgres` or `mysql`.

## Transport

- One JSON object per line (UTF-8, `\n` or `\r\n`), in each direction.
- godwit sends `{"id": 1, "method": "...", "params": {...}}`.
- The plugin answers each request with exactly one line
  `{"id": 1, "result": {...}}` or `{"id": 1, "error": "message"}`, using the
  same `id`. Requests are sent one at a time.
- Anything on **stderr** is shown to the user; use it for logs. Never write
  anything but responses to stdout.
- When godwit is done it closes the plugin's stdin. Exit when stdin reaches EOF.
  A plugin that lingers is killed after 3 seconds.
- godwit starts **one plugin process per open database** and keeps it for the
  whole run, so a session-scoped lock lives as long as the process does. If the
  plugin dies or a call is cancelled, the process is killed and the call fails.

An `error` is reported to the user as `plugin <name>: <message>` and does not
stop the process; you may keep serving requests.

## Methods

| Method | Params | Result |
|---|---|---|
| `handshake` | `{"protocol": 1}` | `{"protocol": 1, "driver": "sqlite", "aliases": ["sqlite3"], "schemes": ["sqlite"], "noHost": true}` |
| `parse_connection` | `{"connstring": "..."}` | `{"target": {...}, "password": "..."}` |
| `open` | `{"target": {...}, "password": "..."}` | none |
| `ensure_table` | none | none |
| `lock` / `unlock` | none | none |
| `applied` | none | `{"records": [...]}` |
| `apply` / `revert` | `{"migration": {...}}` | none |
| `clear_dirty` | `{"version": 5}` | none |
| `close` | none | none |

**`handshake`** runs when godwit starts and for `plugin add`. `protocol` must be
`1`, otherwise the plugin is rejected. `driver` is the name used in targets and
in `-- +godwit driver:` directives. `aliases` are extra names that mean the same
driver. `noHost: true` says the database has no network endpoint (a file, for
example): godwit then does not require a host or user, and never asks for a
password.

**`parse_connection`** turns whatever the user typed into
`godwit auth add <name> <driver> <connstring>` into a target. You own the syntax
and the validation; return an `error` for bad input and never echo a password in
it. A target is:

```json
{ "driver": "sqlite", "host": "", "port": 0, "database": "app.db", "user": "", "params": { "mode": "rwc" } }
```

Any field may be omitted. Return the password separately so godwit stores it in
`.godwit/.env` and not in `config.json`.

**`open`** connects to the target. Return an `error` if it cannot. The target
also carries its `name`.

**`ensure_table`** creates godwit's state table, `godwit_migration`, if it is
missing. Use these columns (types adapted to your database):

| column | meaning |
|---|---|
| `version` | integer primary key |
| `name` | text |
| `checksum` | text, 64 characters |
| `applied_at` | timestamp, default now |
| `duration_ms` | integer |
| `dirty` | boolean |

**`lock` / `unlock`** take and release a database-wide lock so two godwit runs
cannot migrate the same database at once. `lock` should fail rather than wait
forever. If your database has no locking, serialize however you can, or make
`lock` a no-op.

**`applied`** returns every row of the state table, ordered by version:

```json
{ "records": [{ "version": 5, "name": "init", "checksum": "...", "appliedAt": "2026-01-02T03:04:05Z", "durationMs": 12, "dirty": false }] }
```

**`apply` / `revert`** carry the migration. `statements` is already filtered to
the statements that apply to your driver, in order, so you never parse godwit's
files:

```json
{ "migration": { "version": 5, "name": "init", "checksum": "...", "noTransaction": false, "statements": ["CREATE TABLE ..."] } }
```

- `apply` runs the statements, then inserts the row into the state table.
- `revert` runs the statements, then deletes the row.
- If your database can roll back DDL and `noTransaction` is false, do all of it
  in one transaction.
- Otherwise insert the row with `dirty = true` first and clear the flag once the
  statements succeed (for `revert`, set `dirty = true`, run, delete). A failure
  then leaves a visible dirty row that blocks further migrations until the user
  repairs it, rather than a half-applied migration nobody knows about.

**`clear_dirty`** sets `dirty = false` for a version, after a manual repair.

**`close`** is a courtesy before the process is stopped; release resources.

## Testing a plugin by hand

```
printf '{"id":1,"method":"handshake","params":{"protocol":1}}\n' | ./godwit-driver-sqlite
```

You should get one response line. Then `godwit plugin add` and a normal
`godwit migrate status` exercise the rest.

To see how godwit loads and talks to plugins, read [plugin-internals.md](plugin-internals.md).
