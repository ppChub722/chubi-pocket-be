DROP INDEX IF EXISTS project_members_invited_user_idx;
ALTER TABLE project_members DROP COLUMN IF EXISTS invited_user_id;
ALTER TABLE user_notification_settings
    DROP COLUMN IF EXISTS auto_accept_event_invites,
    DROP COLUMN IF EXISTS auto_accept_contact_links,
    DROP COLUMN IF EXISTS auto_accept_wallet_invites;
