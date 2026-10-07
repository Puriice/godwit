---
title: Batch processing
description: Run big data migrations in repeated, separately committed passes.
---

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

