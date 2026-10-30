-- 0.4.2 1b-2 (owner 2026-10-28): sign up with Telegram, no password. Only an account CREATED
-- through an outside app may have no password; it logs in through that app until it sets one.
-- Additive: every existing row is a password account.
ALTER TABLE users
    ADD COLUMN created_via VARCHAR(20) NOT NULL DEFAULT 'password'
        CHECK (created_via IN ('password', 'telegram', 'line', 'google')),
    ALTER COLUMN password_hash DROP NOT NULL,
    ADD CONSTRAINT users_password_or_external
        CHECK (password_hash IS NOT NULL OR created_via <> 'password');
