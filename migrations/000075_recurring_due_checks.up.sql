-- 0.4.0-4: the recurring due check no longer runs on every read; it runs once per
-- local day (POST /sync) and after a recurring write. This remembers the last run.
CREATE TABLE recurring_due_checks (
    user_id    UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    checked_on DATE        NOT NULL,
    checked_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
