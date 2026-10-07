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
  quitting (even closing the terminal) doesn't interrupt them. Progress is logged in
  `.godwit/runs/<target>.log`.
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

A migration file looks like this:

```sql
-- +godwit Up
CREATE TABLE users (id BIGINT PRIMARY KEY);

-- +godwit driver: postgres
ALTER TABLE users ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- +godwit driver: mysql
ALTER TABLE users ADD COLUMN created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP;

-- +godwit Down
DROP TABLE users;
```

## Documentation

Full documentation is at **<https://puriice.github.io/godwit/>**:

- [Getting started](https://puriice.github.io/godwit/guides/getting-started/): connection strings and project configuration
- [Migration files](https://puriice.github.io/godwit/guides/migration-files/): directives and per-driver blocks
- [Batch processing](https://puriice.github.io/godwit/guides/batch-processing/): backfill big tables in repeated passes
- [Commands](https://puriice.github.io/godwit/reference/commands/): every command and flag
- [How migrations run](https://puriice.github.io/godwit/reference/how-migrations-run/): state, locking and failure behaviour

## Driver plugins

godwit ships `postgres`, `mysql` and `sqlite`. For any other database, add a
**driver plugin**: a separate executable that godwit talks to over stdin and stdout.
To write one, see [docs/plugins.md](docs/plugins.md). Go authors can import
[`pkg/godwit`](pkg/godwit); a complete example is in
[examples/driver-jsonfile](examples/driver-jsonfile). To change how plugins work
inside godwit, see [docs/plugin-internals.md](docs/plugin-internals.md).

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
release binaries. The website in `web/` is built with Astro Starlight
(`cd web && pnpm install && pnpm dev`).

## Not yet supported

Moving data between engines. godwit applies schema migrations only.
