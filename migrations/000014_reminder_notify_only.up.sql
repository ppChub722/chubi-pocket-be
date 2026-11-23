-- 0.4.3: notify_only (แค่แจ้งเตือน) — a reminder whose rounds book nothing and count in no total;
-- a round that comes due is closed by itself (paid mark, no transaction); only its alerts go out.
ALTER TABLE recurring_reminders ADD COLUMN notify_only boolean NOT NULL DEFAULT false;
