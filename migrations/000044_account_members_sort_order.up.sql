-- Per-member wallet order (owner decision 2026-10-07, contract §3): each
-- member of a shared wallet keeps their own order; reordering never moves
-- wallets for anyone else. accounts.sort_order stays but is no longer read.
ALTER TABLE account_members
    ADD COLUMN IF NOT EXISTS sort_order INT NOT NULL DEFAULT 0;

-- Seed every membership with the wallet's current order, so nobody's
-- list moves on deploy (ties still break by accounts.created_at).
UPDATE account_members am
SET sort_order = a.sort_order
FROM accounts a
WHERE a.id = am.account_id;
