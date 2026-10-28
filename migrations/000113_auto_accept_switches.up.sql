-- 0.4.1-2 (owner 2026-10-28, BE-3): two auto-accept switches next to the auto-sync ones. A NEW
-- shared bill with my share lands in my book at once only when the bill kind's auto-accept is on;
-- otherwise a shared_bill_created notice asks ("เพิ่มในสมุด"). Auto sync covers later edits.
ALTER TABLE user_notification_settings
    ADD COLUMN auto_accept_events BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN auto_accept_debts  BOOLEAN NOT NULL DEFAULT FALSE;
