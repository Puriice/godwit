-- +godwit Up
-- MySQL can maintain updated_at itself; PostgreSQL needs a trigger.
-- +godwit driver: mysql
ALTER TABLE users
    ADD COLUMN updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6);

-- +godwit driver: postgres
ALTER TABLE users ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- +godwit StatementBegin
CREATE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +godwit StatementEnd

CREATE TRIGGER users_set_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +godwit Down
-- +godwit driver: postgres
DROP TRIGGER users_set_updated_at ON users;
DROP FUNCTION set_updated_at();
ALTER TABLE users DROP COLUMN updated_at;

-- +godwit driver: mysql
ALTER TABLE users DROP COLUMN updated_at;
