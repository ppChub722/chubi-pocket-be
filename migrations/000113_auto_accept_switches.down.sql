ALTER TABLE user_notification_settings
    DROP COLUMN IF EXISTS auto_accept_events,
    DROP COLUMN IF EXISTS auto_accept_debts;
