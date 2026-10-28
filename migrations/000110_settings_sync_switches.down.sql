ALTER TABLE user_notification_settings
    ADD COLUMN auto_types TEXT[] NOT NULL DEFAULT '{split_created}',
    ADD COLUMN auto_resolve_own_in_projects BOOLEAN NOT NULL DEFAULT FALSE;

UPDATE user_notification_settings SET
    auto_types = CASE WHEN auto_sync_debts THEN '{split_created,split_changed}'::text[] ELSE '{}'::text[] END
        || CASE WHEN auto_sync_events THEN '{project_tx_recorded_for_you,project_tx_changed}'::text[] ELSE '{}'::text[] END,
    auto_resolve_own_in_projects = auto_sync_events;

ALTER TABLE user_notification_settings
    DROP COLUMN auto_sync_events,
    DROP COLUMN auto_sync_debts;
