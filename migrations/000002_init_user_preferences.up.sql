CREATE TABLE IF NOT EXISTS user_preferences (
    user_id             UUID         PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    preferences         JSONB        NOT NULL DEFAULT '{}'::jsonb,
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    created_by_user_id  UUID         REFERENCES users(id),
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_by_user_id  UUID         REFERENCES users(id)
);

CREATE TRIGGER set_timestamp_user_preferences
BEFORE UPDATE ON user_preferences
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();
