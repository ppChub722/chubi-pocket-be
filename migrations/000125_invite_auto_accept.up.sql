-- 0.4.2-3 F2 (owner 2026-10-30): the three requests (event invite, contact link, wallet invite) each get
-- their own auto-accept switch, default off. A linked person added to an event is a plain member until
-- they accept; invited_user_id remembers who was invited so the same person is never invited twice
-- (also after a reject). Additive: no existing row changes.
ALTER TABLE user_notification_settings
    ADD COLUMN auto_accept_event_invites  BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN auto_accept_contact_links  BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN auto_accept_wallet_invites BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE project_members
    ADD COLUMN invited_user_id UUID REFERENCES users (id) ON DELETE SET NULL;

CREATE INDEX project_members_invited_user_idx ON project_members (project_id, invited_user_id)
    WHERE invited_user_id IS NOT NULL;
