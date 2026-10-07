---
title: Commands
description: Every godwit command and flag.
---

# Commands

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
  plugin list [-g|-G]                      list plugins (this project; -g = global, -G = both)
  plugin remove <name>                     remove a plugin

Migrations:
  migrate status [target...]               show migration states
  migrate up [-d | --detach] [-n N | --to V] [target...]   apply pending migrations
  migrate down [-d | --detach] [-n N | --to V | --batch] [target...] roll back migrations (--batch: the latest run)
  migrate redo [-d | --detach] <version> [target...]       roll back and re-apply one migration
                                           -d, --detach = run in the background and return
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

