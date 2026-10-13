---
title: Migrating from goose
description: Move a project from pressly/goose to godwit without rewriting migrations or losing history.
---

# Migrating from goose

`godwit translate goose` does two things, and neither removes anything goose
needs:

1. **Migration files.** It adds a `-- +godwit` directive next to every
   `-- +goose` directive. The goose lines stay, so both tools can read the same
   files while you switch over.
2. **History.** For each target it reads goose's version table
   (`goose_db_version`) and records the versions goose has applied in godwit's
   `godwit_migration` table. The goose table is only read, never changed.

```
godwit translate goose [--dry-run] [--table NAME] [target...]
```

Add your databases as targets first (`godwit auth add ...`), set the migrations
directory with `godwit init`, then run it with `--dry-run` to see what would
change.

## What changes in a file

```sql
-- +goose Up
-- +godwit Up
CREATE TABLE users (id BIGINT PRIMARY KEY);

-- +goose StatementBegin
-- +godwit StatementBegin
CREATE FUNCTION touch() RETURNS trigger AS $$ ... $$ LANGUAGE plpgsql;
-- +godwit StatementEnd
-- +goose StatementEnd

-- +goose Down
-- +godwit Down
DROP TABLE users;
```

| goose | added for godwit |
|---|---|
| `Up`, `Down`, `StatementBegin` | the same directive, after the goose line |
| `StatementEnd` | the same directive, **before** the goose line |
| `NO TRANSACTION` | `-- +godwit NoTransaction` |
| `ENVSUB` | nothing: godwit has no equivalent, so you get a warning |

Each converted file must still be a valid godwit migration (for example, the
last statement needs its `;`). A file that is not is left untouched and
reported, and the command exits with an error.

Files godwit cannot use are skipped with a warning: goose's Go migrations, and
names that are not `<version>_<name>.sql` with letters, digits, `_` and `-`.
Nothing is renamed.

## What changes in the database

goose adds a row for every up and down, so godwit replays them in order and the
latest row of a version decides. A version is imported when goose has it
applied **and** a usable migration file exists. Each import is recorded with the
checksum of the converted file, so `godwit migrate status` shows it as applied,
not modified.

- Versions goose has rolled back stay pending.
- Versions applied in goose but missing as files are skipped with a warning.
- Versions already in `godwit_migration` are left alone.

If the goose table has another name or lives in another schema, pass it with
`--table`, for example `--table app.goose_db_version`.

Running the command again is harmless.

## Plugin drivers

PostgreSQL, MySQL and SQLite targets work out of the box. A
[driver plugin](/reference/driver-plugins) can opt in by listing `goose` in its
handshake and implementing the two `translate_*` methods; targets on other
plugins report that they cannot import.
