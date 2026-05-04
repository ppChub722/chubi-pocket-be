-- 1b.2 — Per-user notification preferences. One row per user, auto-created
-- on registration via auth hook. Backfills any existing user (1b.1 pre-dates
-- the auth hook extension).
-- See design/spec/13-notifications.md §1.

CREATE TABLE IF NOT EXISTS user_notification_settings (
    user_id                                          UUID           PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    auto_notify_linked_split_contacts                BOOLEAN        NOT NULL DEFAULT TRUE,
    auto_add_to_personal_debt_on_split_notification  BOOLEAN        NOT NULL DEFAULT FALSE,
    auto_record_received_payment                     BOOLEAN        NOT NULL DEFAULT FALSE,
    auto_resolve_own_in_projects                     BOOLEAN        NOT NULL DEFAULT FALSE,
    default_account_id                               UUID           REFERENCES accounts(id) ON DELETE SET NULL,
    created_at                                       TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    created_by_user_id                               UUID           REFERENCES users(id),
    updated_at                                       TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    updated_by_user_id                               UUID           REFERENCES users(id)
);

CREATE TRIGGER set_timestamp_user_notification_settings
BEFORE UPDATE ON user_notification_settings
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();

-- Backfill: every existing user gets a default-valued row. Idempotent.
INSERT INTO user_notification_settings (user_id)
SELECT id FROM users
ON CONFLICT (user_id) DO NOTHING;
