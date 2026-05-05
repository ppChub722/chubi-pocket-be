-- Add the column back as nullable. The previously-stored nicknames
-- are gone (the up migration overwrote display_name with them); this
-- only restores the schema shape.

ALTER TABLE contacts
    ADD COLUMN nickname VARCHAR(100);
