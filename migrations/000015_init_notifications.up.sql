-- 1b.2 — Notifications inbox. One row per recipient per event.
-- See design/spec/13-notifications.md §1, §2 (trigger registry).

CREATE TABLE IF NOT EXISTS notifications (
    id                  UUID           PRIMARY KEY,
    recipient_user_id   UUID           NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type                VARCHAR(50)    NOT NULL
                          CHECK (type IN (
                            'split_created',
                            'split_paid',
                            'split_received',
                            'project_tx_recorded_for_you',
                            'project_tx_changed',
                            'project_invite',
                            'contact_link_request'
                          )),
    actor_user_id       UUID           REFERENCES users(id),
    payload             JSONB          NOT NULL,
    deep_link           VARCHAR(500),
    read_at             TIMESTAMPTZ,
    actioned_at         TIMESTAMPTZ,
    dismissed_at        TIMESTAMPTZ,
    created_at          TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    created_by_user_id  UUID           REFERENCES users(id),
    updated_at          TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    updated_by_user_id  UUID           REFERENCES users(id),
    -- Mutually exclusive terminal states. read_at can coexist with either.
    CONSTRAINT notifications_terminal_xor CHECK (
        actioned_at IS NULL OR dismissed_at IS NULL
    )
);

CREATE INDEX IF NOT EXISTS idx_notifications_recipient_unread
    ON notifications(recipient_user_id, created_at DESC)
    WHERE read_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_notifications_recipient_all
    ON notifications(recipient_user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_notifications_actor
    ON notifications(actor_user_id)
    WHERE actor_user_id IS NOT NULL;

CREATE TRIGGER set_timestamp_notifications
BEFORE UPDATE ON notifications
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();
