ALTER TABLE personal_debts DROP COLUMN IF EXISTS counterpart_debt_id;
ALTER TABLE user_notification_settings
    ALTER COLUMN auto_add_to_personal_debt_on_split_notification SET DEFAULT FALSE;
ALTER TABLE user_notification_settings DROP COLUMN IF EXISTS muted_types;
