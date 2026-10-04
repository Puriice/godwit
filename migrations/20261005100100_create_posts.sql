-- +godwit Up
-- +godwit driver: postgres
CREATE TABLE posts (
    id         BIGINT       GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    author_id  BIGINT       NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title      VARCHAR(255) NOT NULL,
    body       TEXT         NOT NULL,
    published  BOOLEAN      NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- +godwit driver: mysql
CREATE TABLE posts (
    id         BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    author_id  BIGINT       NOT NULL,
    title      VARCHAR(255) NOT NULL,
    body       TEXT         NOT NULL,
    published  BOOLEAN      NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    CONSTRAINT fk_posts_author FOREIGN KEY (author_id) REFERENCES users (id) ON DELETE CASCADE
);

-- +godwit Down
DROP TABLE posts;
