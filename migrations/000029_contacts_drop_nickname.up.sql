-- Phase 2: nickname is being unified into display_name.
--
-- Up: copy any non-empty nickname into display_name (preserves the
-- user's chosen "what I call this contact" label, since FE was using
-- nickname as the effective name when set), then drop the column.
--
-- Down (in the .down.sql) re-adds the column but cannot recover the
-- original display_name values that were overwritten — by definition
-- of dropping a column, this is one-way.

UPDATE contacts
SET display_name = nickname
WHERE nickname IS NOT NULL AND TRIM(nickname) <> '';

ALTER TABLE contacts
    DROP COLUMN nickname;
