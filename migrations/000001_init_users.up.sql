-- Generic timestamp trigger function. Reused by every table that has updated_at.
CREATE OR REPLACE FUNCTION set_timestamp()
RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = NOW();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE IF NOT EXISTS users (
    id                  UUID         PRIMARY KEY,
    username            VARCHAR(50)  NOT NULL,
    email               VARCHAR(255),
    email_verified_at   TIMESTAMPTZ,
    display_name        VARCHAR(100) NOT NULL,
    password_hash       VARCHAR(255) NOT NULL,
    currency            VARCHAR(3)   NOT NULL DEFAULT 'THB',
    avatar_url          TEXT,
    status              VARCHAR(30)  NOT NULL DEFAULT 'active'
                          CHECK (status IN ('active', 'inactive')),
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    created_by_user_id  UUID         REFERENCES users(id),
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_by_user_id  UUID         REFERENCES users(id)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username ON users(username);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email    ON users(email);

CREATE TRIGGER set_timestamp_users
BEFORE UPDATE ON users
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();
