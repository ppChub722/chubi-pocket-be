CREATE TABLE IF NOT EXISTS tags (
    id                  UUID         PRIMARY KEY,
    user_id             UUID         NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name                VARCHAR(50)  NOT NULL,
    color               VARCHAR(7),
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    created_by_user_id  UUID         REFERENCES users(id),
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_by_user_id  UUID         REFERENCES users(id)
);

CREATE INDEX IF NOT EXISTS idx_tags_user
    ON tags(user_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_tags_name
    ON tags(user_id, LOWER(name));

CREATE TRIGGER set_timestamp_tags
BEFORE UPDATE ON tags
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();
