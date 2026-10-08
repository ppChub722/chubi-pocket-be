ALTER TABLE personal_debts DROP COLUMN IF EXISTS counterpart_debt_id;

ALTER TABLE user_notification_settings
    ADD COLUMN IF NOT EXISTS auto_notify_linked_split_contacts               BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS auto_add_to_personal_debt_on_split_notification BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS auto_record_received_payment                    BOOLEAN NOT NULL DEFAULT FALSE;

UPDATE user_notification_settings SET
    auto_add_to_personal_debt_on_split_notification = 'split_created' = ANY(auto_types),
    auto_record_received_payment = 'split_paid' = ANY(auto_types);

ALTER TABLE user_notification_settings
    DROP COLUMN IF EXISTS auto_types,
    DROP COLUMN IF EXISTS muted_types;
