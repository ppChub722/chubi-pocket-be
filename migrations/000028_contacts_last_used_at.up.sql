-- Phase 2: contacts gain a `last_used_at` recency signal so the FE typeahead
-- (split debtor picker) can rank suggestions by "most recently used" before
-- alphabetical. Bumped by personal_debts.CreateAttachedTx whenever a debt
-- with counterparty_contact_id is inserted.

ALTER TABLE contacts
    ADD COLUMN last_used_at TIMESTAMPTZ;

-- Per-user list ordering: recent-first, NULLS last (never-used contacts
-- fall to the bottom). Postgres uses this index when ORDER BY matches.
CREATE INDEX IF NOT EXISTS idx_contacts_last_used
    ON contacts(user_id, last_used_at DESC NULLS LAST);
