-- Shared wallets — membership of every account.
-- Spec:   design/spec/14-shared-wallets.md §3
-- Schema: design/database/schema.md §14 — Shared Wallets
--
-- One row per (account, user) join/leave cycle. Append-only history:
-- leaving sets left_at; rows are never deleted. Active membership =
-- joined_at IS NOT NULL AND left_at IS NULL. A pending invite is a row
-- with joined_at IS NULL (accept stamps it) — mirrors project_members'
-- pending status without a separate status column.

CREATE TABLE IF NOT EXISTS account_members (
    id                  UUID           PRIMARY KEY,
    account_id          UUID           NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    user_id             UUID           NOT NULL REFERENCES users(id),
    role                VARCHAR(10)    NOT NULL
                          CHECK (role IN ('owner', 'member')),
    -- Per-member personal-report inclusion (spec §5). Auto-reset to
    -- 'none' for everyone at conversion; ex-members capped at 'own'.
    report_scope        VARCHAR(10)    NOT NULL DEFAULT 'none'
                          CHECK (report_scope IN ('none', 'own', 'all')),
    -- NULL while the invite is pending; stamped on accept. Backfill and
    -- owner auto-rows set it immediately.
    joined_at           TIMESTAMPTZ,
    -- Set on leave/remove; never hard-deleted. Also locks the ex-member's
    -- own rows on this account to read-only for them.
    left_at             TIMESTAMPTZ,
    created_at          TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    created_by_user_id  UUID           REFERENCES users(id),
    updated_at          TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    updated_by_user_id  UUID           REFERENCES users(id)
);

-- Authz checks (is caller a member of this account?).
CREATE INDEX IF NOT EXISTS idx_account_members_account
    ON account_members(account_id)
    WHERE left_at IS NULL;

-- "My shared wallets" list.
CREATE INDEX IF NOT EXISTS idx_account_members_user
    ON account_members(user_id)
    WHERE left_at IS NULL;

-- One live (pending or active) membership per (account, user). Left rows
-- don't count, so re-invite after leaving works (multiple cycles).
CREATE UNIQUE INDEX IF NOT EXISTS idx_account_members_live_unique
    ON account_members(account_id, user_id)
    WHERE left_at IS NULL;

CREATE TRIGGER set_timestamp_account_members
BEFORE UPDATE ON account_members
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();

-- Backfill: one owner row per existing account with report_scope='all'
-- (preserves today's behavior exactly — a personal wallet fully counts
-- in its owner's reports). created_by NULL = system, same convention as
-- migration 000026's system-category backfill.
INSERT INTO account_members
    (id, account_id, user_id, role, report_scope, joined_at, created_by_user_id, updated_by_user_id)
SELECT
    gen_random_uuid(),
    a.id,
    a.user_id,
    'owner',
    'all',
    NOW(),
    NULL,
    NULL
FROM accounts a
WHERE NOT EXISTS (
    SELECT 1 FROM account_members am
    WHERE am.account_id = a.id AND am.user_id = a.user_id AND am.left_at IS NULL
);

-- Wallet invites ride the notification pattern (spec §8.1 — resolved
-- 2026-09-11, no dedicated account_invites table). Extend the type CHECK.
ALTER TABLE notifications DROP CONSTRAINT IF EXISTS notifications_type_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_type_check
    CHECK (type IN (
        'split_created',
        'split_paid',
        'split_received',
        'project_tx_recorded_for_you',
        'project_tx_changed',
        'project_invite',
        'contact_link_request',
        'account_invite'
    ));
