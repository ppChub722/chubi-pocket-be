CREATE TABLE IF NOT EXISTS transaction_tags (
    transaction_id      UUID         NOT NULL REFERENCES transactions(id) ON DELETE CASCADE,
    tag_id              UUID         NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    created_by_user_id  UUID         REFERENCES users(id),
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_by_user_id  UUID         REFERENCES users(id),
    PRIMARY KEY (transaction_id, tag_id)
);

-- Reverse-direction lookup (find transactions for a tag) — used by usage_count
-- and tag-filter listings. Composite PK already covers (transaction_id, tag_id).
CREATE INDEX IF NOT EXISTS idx_transaction_tags_tag
    ON transaction_tags(tag_id);

CREATE TRIGGER set_timestamp_transaction_tags
BEFORE UPDATE ON transaction_tags
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();
