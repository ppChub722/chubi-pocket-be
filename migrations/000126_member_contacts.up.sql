-- 0.4.2-3 F3 (owner 2026-10-30): which of MY contacts an event member is, per viewer. Two people's
-- "แม่" are different people, so a member is never matched to a contact by name any more, except the
-- members that existed before this migration (name_match): their events keep reading as they did.
-- Additive: a new table and a new column (TRUE on every existing member, FALSE for new ones).
CREATE TABLE project_member_contacts (
    project_member_id UUID        NOT NULL REFERENCES project_members (id) ON DELETE CASCADE,
    user_id           UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- deleting the contact makes the member a plain name again (for that viewer)
    contact_id        UUID        NOT NULL REFERENCES contacts (id) ON DELETE CASCADE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_member_id, user_id)
);

CREATE INDEX project_member_contacts_contact_idx ON project_member_contacts (contact_id);

ALTER TABLE project_members ADD COLUMN name_match BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE project_members ALTER COLUMN name_match SET DEFAULT FALSE;
