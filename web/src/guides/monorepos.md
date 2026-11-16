---
title: Monorepos and services
description: Keep each service's migrations and databases apart in one repository, and share a development database between them.
---

# Monorepos and services

A monorepo often holds several services, each with its own migrations and its
own production database. During development the same services usually share
one database. godwit models this as a **workspace**:

- Each **service** has its own migrations directory and its own targets.
- The workspace root has **global targets**, which every service can migrate
  on, such as the shared development database.
- Every service records its migrations in its own state table,
  `godwit_migration_<service>`. Services that share a database therefore keep
  separate histories and never block each other's runs.

A project without services works exactly as before, with its state in
`godwit_migration`.

## Layout

```
repo/
  .godwit/config.json      lists the services; its targets are global
  .godwit/.env             passwords of the global targets
  services/users/
    .godwit/config.json    the users service: its migrations directory and targets
    .godwit/.env           passwords of its own targets
    migrations/
  services/orders/
    .godwit/config.json
    migrations/
```

The root's `.godwit/config.json`:

```json
{
  "services": ["services/users", "services/orders"],
  "migrationsDir": "migrations",
  "targets": [
    { "name": "dev", "driver": "postgres", "host": "localhost", "database": "app", "user": "app" }
  ]
}
```

A service's `.godwit/config.json` has the usual format plus the service name:

```json
{
  "service": "users",
  "migrationsDir": "migrations",
  "targets": [
    { "name": "prod", "driver": "postgres", "host": "users-db.internal", "database": "users", "user": "users" }
  ]
}
```

Service names may hold lower case letters, digits and `_`, start with a
letter, and have at most 40 characters. The name becomes part of the state
table's name and of environment variable names.

## Set it up

```sh
godwit service add services/users             # or press a on the TUI's services panel
godwit service add services/orders
godwit auth add dev postgres 'postgres://app:pass@localhost:5432/app'   # a global target
godwit auth add -s users prod postgres 'postgres://users:pass@users-db.internal/users'
godwit migrate new -s users create_users
godwit migrate up                             # every service, on every target it can use
```

`godwit service add` also turns an existing plain project into a workspace.
Its targets become global targets. The workspace root has no migrations of its
own, so move any existing migration files into a service.

## Which services a command acts on

godwit finds the workspace from the directory it runs in:

| Where you run godwit | Services it works on |
|---|---|
| the workspace root | every service |
| a service directory listed by the root | that service only |
| anywhere else | a plain project, as before |

`-s NAME` (or `--service NAME`) limits `auth`, `migrate`, `process` and
`translate` to one service, and can be repeated. A target can also be named
`service/target`, as in `godwit migrate up users/dev`.

Output is grouped by service and target:

```
$ godwit migrate status
== users/prod ==
20261116070239  create_users  applied
== users/dev ==
20261116070239  create_users  applied
== orders/dev ==
20261116070239  create_orders  pending
```

`godwit migrate new` needs a service at the root when there is more than one:
use `-s`, or run it inside the service directory.

godwit always works from the workspace root, wherever it is started. Relative
paths in any configuration, such as a SQLite file, are relative to the
workspace root.

## Targets and passwords

A service uses its own targets first and then the global ones. A service
target with the same name as a global target hides the global one for that
service. `godwit auth list` marks targets as `(global)` or
`(shadows global)`.

Passwords live with the target's definition:

| Target | Saved in | Environment variable |
|---|---|---|
| global | `.godwit/.env` at the root | `GODWIT_<TARGET>_PASSWORD` |
| a service's own | the service's `.godwit/.env` | `GODWIT_<SERVICE>_<TARGET>_PASSWORD` |

At the root, `godwit auth add` without `-s` adds a global target. Inside a
service directory it adds a target to that service.

Editing, enabling or disabling a global target changes it for every service. A
global target can only be removed at the root, without `-s`. A migration
disabled on a global target is disabled for that version in every service.
Prefer the service's default (`e` in the TUI's migrations panel) when only one
service is concerned.

## Background runs

Each service has its own run and queue on a target. A run of `users` on the
shared `dev` database therefore does not queue behind one of `orders`. The
logs live in `.godwit/runs/<service>/<target>.log` at the workspace root.

## In the TUI

The **services** panel lists the workspace's services with their targets,
migration files and state table. Press `a` to add a service: give its
directory, relative to the workspace root, and optionally a name. In a plain
project this turns the project into a workspace, as `godwit service add` does.
Press `x` to take a service out of the workspace; its files and databases are
left alone. Add services at the workspace root, not inside a service directory.

The targets panel lists each service under a heading, with the global targets
it uses marked `global`. Adding a target asks for its scope: global, or one
service. The migrations panel has a section per service. Run the TUI inside a
service directory to see only that service.

## Driver plugins

Plugins listed by the root and by each service are loaded together. When two
services define the same plugin name with different commands, godwit warns and
uses the first. Plugin binaries installed with `godwit plugin install` go to
the workspace root's `.godwit/plugins`.

A plugin must speak protocol 2 to serve a service, because the state table name
is sent when a connection opens. Older plugins still work for plain projects.
See [Driver plugins](../reference/driver-plugins.md).
