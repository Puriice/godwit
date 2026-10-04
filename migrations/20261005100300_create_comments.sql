-- +godwit Up
-- +godwit driver: postgres
CREATE TABLE comments (
    id         BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    post_id    BIGINT      NOT NULL REFERENCES posts (id) ON DELETE CASCADE,
    user_id    BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    body       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +godwit driver: mysql
CREATE TABLE comments (
    id         BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    post_id    BIGINT       NOT NULL,
    user_id    BIGINT       NOT NULL,
    body       TEXT         NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    CONSTRAINT fk_comments_post FOREIGN KEY (post_id) REFERENCES posts (id) ON DELETE CASCADE,
    CONSTRAINT fk_comments_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
);

-- +godwit driver: all
CREATE INDEX idx_comments_post_id ON comments (post_id);

-- +godwit Down
-- +godwit driver: postgres
DROP INDEX idx_comments_post_id;
DROP TABLE comments;

-- +godwit driver: mysql
DROP TABLE comments;
