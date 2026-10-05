-- Batch processing test: split full_name into first_name and last_name,
-- 500 rows per pass with a 200ms pause between passes, then drop full_name.
-- Needs 20261006100000.
-- +godwit Up
ALTER TABLE batch_test_users ADD COLUMN first_name VARCHAR(255);
ALTER TABLE batch_test_users ADD COLUMN last_name VARCHAR(255);

-- +godwit RepeatStart: 200ms
-- +godwit driver: postgres
UPDATE batch_test_users SET
    first_name = split_part(full_name, ' ', 1),
    last_name  = substr(full_name, strpos(full_name, ' ') + 1)
WHERE id IN (SELECT id FROM batch_test_users WHERE first_name IS NULL LIMIT 500);

-- +godwit driver: mysql
UPDATE batch_test_users SET
    first_name = SUBSTRING_INDEX(full_name, ' ', 1),
    last_name  = SUBSTR(full_name, LOCATE(' ', full_name) + 1)
WHERE first_name IS NULL LIMIT 500;

-- +godwit driver: sqlite
UPDATE batch_test_users SET
    first_name = substr(full_name, 1, instr(full_name, ' ') - 1),
    last_name  = substr(full_name, instr(full_name, ' ') + 1)
WHERE id IN (SELECT id FROM batch_test_users WHERE first_name IS NULL LIMIT 500);

-- +godwit driver: all
-- +godwit RepeatEnd

ALTER TABLE batch_test_users DROP COLUMN full_name;

-- +godwit Down
ALTER TABLE batch_test_users ADD COLUMN full_name VARCHAR(255);

-- +godwit RepeatStart: 200ms
-- +godwit driver: postgres
UPDATE batch_test_users SET full_name = first_name || ' ' || last_name
WHERE id IN (SELECT id FROM batch_test_users WHERE full_name IS NULL LIMIT 500);

-- +godwit driver: mysql
UPDATE batch_test_users SET full_name = CONCAT(first_name, ' ', last_name)
WHERE full_name IS NULL LIMIT 500;

-- +godwit driver: sqlite
UPDATE batch_test_users SET full_name = first_name || ' ' || last_name
WHERE id IN (SELECT id FROM batch_test_users WHERE full_name IS NULL LIMIT 500);

-- +godwit driver: all
-- +godwit RepeatEnd

ALTER TABLE batch_test_users DROP COLUMN last_name;
ALTER TABLE batch_test_users DROP COLUMN first_name;
