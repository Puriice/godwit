---
title: How migrations run
description: State, locking and failure behaviour per database.
---

# How migrations run

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

