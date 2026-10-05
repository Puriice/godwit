# godwit

A terminal UI and CLI for applying versioned SQL schema migrations to several
databases at once. PostgreSQL, MySQL/MariaDB and SQLite are supported, and one set of
migration files can serve both.

- **Multiple targets:** apply the same migrations to any number of databases.
- **One file per migration**, with optional per-engine SQL blocks.
- **State lives in each database**, in a `godwit_migration` table, so every
  target tracks its own progress.
- **TUI and CLI:** every action in the TUI also exists as a command, for scripts and CI.
- **Safe by default:** checksums detect edited migrations, locks stop concurrent
  runs, and a failed run is flagged until you resolve it.

## Install

```sh
go install github.com/puriice/godwit/cmd/godwit@latest
```

Or download a binary from the GitHub releases page.

## Quick start

```sh
godwit init                                   # choose a migrations directory, add targets
godwit auth add prod postgres 'postgres://user:pass@host:5432/db?sslmode=disable'
godwit migrate new create_users               # creates migrations/<timestamp>_create_users.sql
godwit migrate status
godwit migrate up
godwit                                        # or do all of this in the TUI
```

## Project configuration

Settings live in `.godwit/`:

| File | Contents | Commit it? |
|---|---|---|
| `.godwit/config.json` | migrations directory and targets (no passwords) | yes |
| `.godwit/.env` | `GODWIT_<TARGET>_PASSWORD=...` | no |
| `.godwit/.gitignore` | created automatically; ignores `.env` | yes |

A password in the process environment overrides `.env`, so CI needs no file.
After a fresh clone, the TUI asks for each missing password. The CLI never
prompts; it fails on that target instead.

## Migration files

`migrations/<version>_<name>.sql`, with sections and optional driver blocks:

```sql
-- +godwit Up
CREATE TABLE users (id BIGINT PRIMARY KEY);

-- +godwit driver: postgres
ALTER TABLE users ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- +godwit driver: mysql
ALTER TABLE users ADD COLUMN created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP;

-- +godwit StatementBegin
CREATE FUNCTION touch() RETURNS trigger AS $$ BEGIN RETURN NEW; END; $$ LANGUAGE plpgsql;
-- +godwit StatementEnd

-- +godwit Down
DROP TABLE users;
```

| Directive | Meaning |
|---|---|
| `-- +godwit Up` / `Down` | start the apply or roll-back section |
| `-- +godwit driver: <name>` | statements below run only on that driver (`postgres`, `mysql` or `sqlite`) until the next directive. No directive, or a blank name, means all drivers. Unknown names are rejected. |
| `-- +godwit StatementBegin` / `StatementEnd` | keep a body containing `;` as one statement |
| `-- +godwit NoTransaction` | do not wrap the migration in a transaction (PostgreSQL) |

Statements are split on `;` outside `StatementBegin`/`StatementEnd`. Aliases:
`postgresql` and `pg` mean `postgres`; `mariadb` means `mysql`; `sqlite3` means `sqlite`.

## Commands

```
Interface:
  (none)                                   open the migration TUI
  init                                     set the migrations directory and add targets

Targets:
  auth add <name> <driver> <conn string>   add a target from a connection string
  auth list                                list targets (passwords redacted)
  auth remove <name>                       remove a target and its saved password
  auth disable <name>                      temporarily skip a target
  auth enable <name>                       use a disabled target again

Plugins:
  plugin install <url> [name]              build a driver plugin from a Go package and add it
  plugin add <command> [name]              add a driver plugin you already have
  plugin list                              list plugins
  plugin remove <name>                     remove a plugin

Migrations:
  migrate status [target...]               show migration states
  migrate up [-n N | --to V] [target...]   apply pending migrations
  migrate down [-n N | --to V] [target...] roll back migrations
  migrate redo <version> [target...]       roll back and re-apply one migration
  migrate clear-dirty <version> <target>   clear a dirty flag after a manual repair
  migrate new <name>                       create a migration file
```

Without target names, `migrate` uses every enabled target. It tries all of them
and exits non-zero if any failed.

Connection strings:

```
postgres://user:pass@host:5432/db?sslmode=disable
mysql://user:pass@host:3306/db
user:pass@tcp(host:3306)/db          (mysql only)
sqlite://app.db                      (sqlite: a file path; also sqlite:///abs/app.db or just app.db)
```

## Driver plugins

godwit ships `postgres`, `mysql` and `sqlite`. For any other database, add a **driver
plugin**: a separate executable that godwit starts and talks to over stdin and
stdout (JSON lines). It can be written in any language and works the same on
Windows, macOS and Linux, with no rebuild of godwit.

```
godwit plugin install github.com/you/godwit-driver-sqlite   # go install into .godwit/plugins
godwit plugin install -g github.com/you/godwit-driver-sqlite # ...or once for every project, in ~/.godwit
godwit plugin add ./bin/godwit-driver-sqlite                # or use an executable you have
godwit auth add local sqlite 'sqlite://app.db'              # the plugin defines the syntax
```

`plugin install` builds a Go plugin with `go install` (needs the Go toolchain;
add `@version` to pin one) and `plugin add` takes any executable. Both start the
plugin once to check it, then record it in `.godwit/config.json`. The plugin's
name defaults to the driver name it reports; pass one as the last argument to
choose your own. Add `-g` to register a plugin for your user in `~/.godwit`, so
every project sees it, or `-G` to register it both globally and in the project. Plugin drivers appear in the TUI and can be used in
`-- +godwit driver: <name>` directives like the built-in ones. Plugins run with
your privileges, so only add executables you trust. To write one, see
[docs/plugins.md](docs/plugins.md). Go authors can import
[`pkg/godwit`](pkg/godwit), which provides the interfaces and runs the
protocol, so a plugin is two small interfaces and one `Serve` call. A
complete working plugin built on it is in
[examples/driver-jsonfile](examples/driver-jsonfile). To change how plugins work
inside godwit, see [docs/plugin-internals.md](docs/plugin-internals.md).

## TUI keys

Targets list: `↑/↓` select, `↵` open, `a` add, `e` edit, `x` delete,
`t` enable/disable, `p` password, `u` apply all targets, `r` refresh,
`n` new migration, `q` quit.

Migrations screen: `u` apply all, `s` apply next, `d` roll back last,
`↵` migrate up or down to the selected migration, `R` redo selected,
`c` clear dirty, `t` enable/disable, `r` refresh, `n` new, `esc` back.

## How migrations run

- **State:** each applied migration is recorded with its version, a SHA-256
  checksum and a dirty flag. Status is one of `pending`, `applied`, `modified`
  (file changed after it was applied), `dirty`, or `missing` (recorded, but the
  file is gone).
- **Locking:** PostgreSQL uses `pg_advisory_lock`, MySQL uses `GET_LOCK`. SQLite has no advisory lock; its single-writer file lock and the version primary key protect against concurrent runs.
- **PostgreSQL:** each migration, including its state-table change, runs in one
  transaction, so a failure rolls back and the migration stays pending. Use
  `NoTransaction` for statements that cannot run in one, such as
  `CREATE INDEX CONCURRENTLY`.
- **MySQL/MariaDB:** DDL commits implicitly, so there is no rollback. godwit
  records the migration as dirty, runs it, then clears the flag. If it fails, the
  earlier statements stay applied and the migration remains dirty. Runs are
  refused until you fix the database by hand and clear the flag.
- **Several migrations:** each is committed on its own, so a failure part-way
  leaves the earlier ones applied.
- **Redo:** the roll-back and re-apply are separate steps. If the re-apply fails,
  the migration stays rolled back.

## Development

```sh
go vet ./... && go test ./...

# integration tests against real databases
docker compose up -d
export GODWIT_TEST_POSTGRES="postgres://godwit:godwit@localhost:55432/godwit"
export GODWIT_TEST_MYSQL="godwit:godwit@tcp(localhost:53306)/godwit"
go test ./internal/adapters/sqldb -run Integration -v
```

The code follows a ports-and-adapters layout: `internal/domain` (no I/O),
`internal/app` (use cases and ports), and adapters for the file store, migration
files, SQL databases, TUI and CLI. `internal/architecture_test.go` enforces the
dependency direction. CI runs on GitHub Actions; pushing a `v*` tag publishes
release binaries.

## Not yet supported

Moving data between engines. godwit applies schema migrations only.
