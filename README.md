# godwit

A terminal UI and CLI for applying versioned SQL schema migrations to several
databases at once. PostgreSQL, MySQL/MariaDB and SQLite are supported, and one set of
migration files can serve both.

- **Multiple targets:** apply the same migrations to any number of databases.
- **One file per migration**, with optional per-engine SQL blocks.
- **State lives in each database**, in a `godwit_migration` table, so every
  target tracks its own progress.
- **TUI and CLI:** every action in the TUI also exists as a command, for scripts and CI (except the debug panel).
- **Runs survive quitting:** the TUI runs migrations in a detached worker process, so
  quitting (even closing the terminal) doesn't interrupt them. Reopen the TUI to see
  the target still running. Progress is logged in `.godwit/runs/<target>.log`.
  Starting another run on a target that is already running queues it: it starts
  when the current run ends well, and is dropped if that run fails or is cancelled.
  Queued jobs show in the TUI's processes tab, under their run; select one to see
  its details and press `x` to remove it, or `X` to clear the whole queue of that
  target (the run in progress is not affected).
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
| `-- +godwit RepeatStart[: <time>]` / `RepeatEnd` | run the statements in between again and again, one transaction per pass, optionally waiting `<time>` between passes; see [Batch processing](#batch-processing) |
| `-- +godwit RepeatCondition` | inside a repeat block, a query that decides whether to run another pass |

Statements are split on `;` outside `StatementBegin`/`StatementEnd`. Aliases:
`postgresql` and `pg` mean `postgres`; `mariadb` means `mysql`; `sqlite3` means `sqlite`.

## Batch processing

A data migration over a big table should not be one giant transaction. Put the
statements between `RepeatStart` and `RepeatEnd` and godwit runs them again and
again, committing after every pass. For example, splitting `full_name`:

```sql
-- +godwit Up
ALTER TABLE users ADD COLUMN first_name TEXT;
ALTER TABLE users ADD COLUMN last_name TEXT;

-- +godwit RepeatStart
UPDATE users SET
  first_name = substr(full_name, 1, instr(full_name, ' ') - 1),
  last_name  = substr(full_name, instr(full_name, ' ') + 1)
WHERE id IN (SELECT id FROM users WHERE first_name IS NULL LIMIT 1000);
-- +godwit RepeatEnd

ALTER TABLE users DROP COLUMN full_name;
```

- A block holds one or more statements. A **pass** runs all of them in order, in
  one transaction (PostgreSQL, SQLite). MySQL has no transactional DDL here, so
  each statement commits by itself. Blocks do not nest, and work in `Up` and `Down`.
- **Without `RepeatCondition`,** the block repeats until a pass affects no rows
  in total. It always runs at least once. The `WHERE` clause must therefore
  shrink the work on its own, as `first_name IS NULL ... LIMIT 1000` does above.
- **With `RepeatCondition`,** put one query after the body, just before
  `RepeatEnd`. It runs before every pass, and the block continues while its first
  value is true (non-zero, non-empty, not `false`). An empty table runs the body
  zero times. Use it when the body always affects rows, or to stop on your own
  rule:

  ```sql
  -- +godwit RepeatStart
  INSERT INTO new_events SELECT ... FROM events WHERE id > (SELECT COALESCE(MAX(id), 0) FROM new_events) ORDER BY id LIMIT 1000;
  -- +godwit RepeatCondition
  SELECT EXISTS (SELECT 1 FROM events WHERE id > (SELECT COALESCE(MAX(id), 0) FROM new_events));
  -- +godwit RepeatEnd
  ```
- **A delay** can follow `RepeatStart` on the same line, and makes godwit wait
  between passes, so a long backfill does not keep the database busy. The wait
  happens after a pass has committed, so no transaction is held open, and Ctrl+C
  ends it at once. Units are `ms`, `s`, `m`, `hr` and `d`, with optional spaces:
  `RepeatStart: 500ms`, `RepeatStart:2s`, `RepeatStart 1.5 m`. Plain `RepeatStart`
  means no delay. A block without a `RepeatCondition` still waits once after its
  final pass that changed rows, because only the next pass shows that nothing is
  left.
- `driver:` scoping works inside a block, for `LIMIT` and other dialect
  differences. A block with no statement for the current driver is skipped.
- A block that never finishes stops with an error after 10 million passes.
- A migration with a repeat block is recorded as dirty while it runs, like
  `NoTransaction`, because the passes before a failure stay committed. Write the
  body so it can safely run again, fix the cause, then `migrate clear-dirty` and
  run it once more.
- Plugin drivers do not support repeat blocks yet; such a migration is refused.

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
  migrate up [--detach] [-n N | --to V] [target...]   apply pending migrations
  migrate down [--detach] [-n N | --to V | --batch] [target...] roll back migrations (--batch: the latest run)
  migrate redo [--detach] <version> [target...]       roll back and re-apply one migration
                                           --detach = run in the background and return
  migrate clear-dirty <version> <target>   clear a dirty flag after a manual repair
  migrate new <name>                       create a migration file
  process list [target...]                 background runs and their queues
  process show <target> [-n LINES]         a run's details, queue and log tail
  process cancel <target>                  cancel the run in progress
  process dequeue <target> <position>      remove one queued job
  process clear-queue <target>             remove every queued job of a target
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
- **Debug panel (TUI only):** on a target's migration list, `←`/`→` switch between
  the list and a debug panel. It follows the cursor and shows what the target has
  recorded for that migration. `p`, `d` and `a` force it to pending, dirty or
  applied, after a confirmation. Forcing rewrites only the `godwit_migration`
  record and never runs SQL or changes the schema, so use it to reproduce or
  repair states. Plugin drivers do not support it.

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
