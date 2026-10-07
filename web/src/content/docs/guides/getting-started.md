---
title: Getting started
description: Install godwit, add a database and apply your first migration.
---

## Install

```sh
go install github.com/puriice/godwit/cmd/godwit@latest
```

Or download a binary from the [GitHub releases page](https://github.com/Puriice/godwit/releases).

## Quick start

```sh
godwit init                                   # choose a migrations directory, add targets
godwit auth add prod postgres 'postgres://user:pass@host:5432/db?sslmode=disable'
godwit migrate new create_users               # creates migrations/<timestamp>_create_users.sql
godwit migrate status
godwit migrate up
godwit                                        # or do all of this in the TUI
```

## Connection strings

```
postgres://user:pass@host:5432/db?sslmode=disable
mysql://user:pass@host:3306/db
user:pass@tcp(host:3306)/db          (mysql only)
sqlite://app.db                      (sqlite: a file path; also sqlite:///abs/app.db or just app.db)
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
