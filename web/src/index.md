---
layout: home
title: godwit
description: Apply versioned SQL schema migrations to several databases at once, from a terminal UI or the command line.

hero:
  name: godwit
  tagline: Apply versioned SQL schema migrations to several databases at once, from a terminal UI or the command line.
  actions:
    - theme: brand
      text: Get started
      link: /guides/getting-started
    - theme: alt
      text: View on GitHub
      link: https://github.com/Puriice/godwit

features:
  - title: Multiple targets
    details: Apply the same migrations to any number of PostgreSQL, MySQL/MariaDB and SQLite databases. One set of files can serve all of them.
  - title: One file per migration
    details: Plain SQL with optional per-engine blocks, so dialect differences stay in the same file.
  - title: TUI and CLI
    details: Every action in the TUI also exists as a command, for scripts and CI.
  - title: Runs survive quitting
    details: Migrations run in a detached worker, so closing the terminal doesn't interrupt them.
  - title: Safe by default
    details: Checksums detect edited migrations, locks stop concurrent runs, and a failed run is flagged until you resolve it.
  - title: Batch processing
    details: Backfill big tables in repeated, separately committed passes instead of one giant transaction.
---
