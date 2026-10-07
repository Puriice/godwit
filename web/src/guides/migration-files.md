---
title: Migration files
description: The file format, directives and per-driver blocks.
---

# Migration files

Migrations live in `migrations/<version>_<name>.sql`, with sections and optional
driver blocks:

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

## Directives

| Directive | Meaning |
|---|---|
| `-- +godwit Up` / `Down` | start the apply or roll-back section |
| `-- +godwit driver: <name>` | statements below run only on that driver (`postgres`, `mysql` or `sqlite`) until the next directive. No directive, or a blank name, means all drivers. Unknown names are rejected. |
| `-- +godwit StatementBegin` / `StatementEnd` | keep a body containing `;` as one statement |
| `-- +godwit NoTransaction` | do not wrap the migration in a transaction (PostgreSQL) |
| `-- +godwit RepeatStart[: <time>]` / `RepeatEnd` | run the statements in between again and again, one transaction per pass; see [Batch processing](./batch-processing) |
| `-- +godwit RepeatCondition` | inside a repeat block, a query that decides whether to run another pass |

Statements are split on `;` outside `StatementBegin`/`StatementEnd`. Aliases:
`postgresql` and `pg` mean `postgres`; `mariadb` means `mysql`; `sqlite3` means `sqlite`.
