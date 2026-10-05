-- Test fixture for batch processing: a table with a full_name column and 5000 rows.
-- +godwit Up
-- +godwit driver: postgres
CREATE TABLE batch_test_users (
    id        BIGINT       GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    full_name VARCHAR(255) NOT NULL
);

-- +godwit driver: mysql
CREATE TABLE batch_test_users (
    id        BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    full_name VARCHAR(255) NOT NULL
);

-- +godwit driver: sqlite
CREATE TABLE batch_test_users (
    id        INTEGER PRIMARY KEY,
    full_name TEXT NOT NULL
);

-- +godwit driver: postgres
INSERT INTO batch_test_users (full_name)
SELECT 'First' || n || ' Last' || n FROM generate_series(1, 5000) AS n;

-- +godwit driver: sqlite
INSERT INTO batch_test_users (full_name)
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 5000)
SELECT 'First' || i || ' Last' || i FROM n;

-- MySQL and MariaDB: build the numbers 1..5000 from four digit tables, which
-- avoids recursive CTE depth limits that differ between the two.
-- +godwit driver: mysql
INSERT INTO batch_test_users (full_name)
SELECT CONCAT('First', n, ' Last', n)
FROM (
    SELECT a.d + b.d * 10 + c.d * 100 + e.d * 1000 + 1 AS n
    FROM (SELECT 0 AS d UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4
          UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) a
    CROSS JOIN (SELECT 0 AS d UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4
          UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) b
    CROSS JOIN (SELECT 0 AS d UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4
          UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) c
    CROSS JOIN (SELECT 0 AS d UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4
          UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) e
) nums
WHERE n <= 5000;

-- +godwit Down
-- +godwit driver: all
DROP TABLE batch_test_users;
