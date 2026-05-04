CREATE TABLE IF NOT EXISTS contacts (
    id                  UUID           PRIMARY KEY,
    user_id             UUID           NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    display_name        VARCHAR(100)   NOT NULL,
    nickname            VARCHAR(100),
    email               VARCHAR(255),
    phone               VARCHAR(50),
    notes               TEXT,
    icon                VARCHAR(50),
    -- linked_user_id is dormant in 1b.1: the schema is forward-compatible with
    -- 1b.2's notification-driven link-request flow, but no endpoint sets it yet.
    -- See design/spec/07-contacts.md §2 and product/phase1b/overview.md.
    linked_user_id      UUID           REFERENCES users(id),
    status              VARCHAR(20)    NOT NULL DEFAULT 'active'
                          CHECK (status IN ('active', 'archived')),
    created_at          TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    created_by_user_id  UUID           REFERENCES users(id),
    updated_at          TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    updated_by_user_id  UUID           REFERENCES users(id),
    CONSTRAINT contacts_no_self_link CHECK (user_id <> linked_user_id)
);

CREATE INDEX IF NOT EXISTS idx_contacts_user
    ON contacts(user_id, status);

CREATE INDEX IF NOT EXISTS idx_contacts_name
    ON contacts(user_id, LOWER(display_name));

CREATE UNIQUE INDEX IF NOT EXISTS idx_contacts_owner_linked_user
    ON contacts(user_id, linked_user_id)
    WHERE linked_user_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_contacts_linked_user
    ON contacts(linked_user_id)
    WHERE linked_user_id IS NOT NULL;

CREATE TRIGGER set_timestamp_contacts
BEFORE UPDATE ON contacts
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();
