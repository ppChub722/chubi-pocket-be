-- 0.3.1-9: my own label for a contact is back (it was merged into
-- display_name by migration 29). `nickname` is optional; `show_nickname` is
-- the per-contact switch: with it off the nickname is kept but the contact
-- shows as its linked user's profile name (or its display_name).
ALTER TABLE contacts
    ADD COLUMN nickname      VARCHAR(100),
    ADD COLUMN show_nickname BOOLEAN NOT NULL DEFAULT TRUE;
