-- Wallet identifiers (owner design 2026-10-09, spec 15 §5): the numbers a
-- wallet is known by on bank slips — bank account, PromptPay, card, other.
-- A slip's masked numbers are matched against them to pick the wallet and
-- the direction (expense / income / transfer). JSONB so new kinds need no
-- migration:
--   [{"kind":"bank_account","value":"1234527806","bank_code":"004"}, …]
-- value: digits, `x` for digits not known (a masked number saved from a slip).
ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS identifiers JSONB NOT NULL DEFAULT '[]'::jsonb;
