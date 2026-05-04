-- Phase 1a additions for the account form. Mirrors the categories.{description, note}
-- pair from migration 000008. Spec: design/spec/03-accounts.md §2.1 / §2.4.

ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS description TEXT,
    ADD COLUMN IF NOT EXISTS note        TEXT;
