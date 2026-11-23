-- 0.4.3: alerts (notices) on a reminder of a recurring item, sent by a daily job ("{name} จะตัด {amount} {when}").
CREATE TABLE recurring_reminder_alerts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    reminder_id  uuid NOT NULL REFERENCES recurring_reminders(id) ON DELETE CASCADE,
    anchor       varchar(10) NOT NULL CHECK (anchor IN ('due', 'start', 'end')),
    days_before  integer NOT NULL DEFAULT 0 CHECK (days_before BETWEEN 0 AND 60),
    time         time NOT NULL DEFAULT '09:00',
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_recurring_reminder_alerts_reminder ON recurring_reminder_alerts (reminder_id);
CREATE INDEX idx_recurring_reminder_alerts_user ON recurring_reminder_alerts (user_id);

-- what the job already sent: one notice per alert per round (target = the reminder).
CREATE TABLE recurring_reminder_alert_sends (
    alert_id    uuid NOT NULL REFERENCES recurring_reminder_alerts(id) ON DELETE CASCADE,
    target_id   uuid NOT NULL,
    slot        date NOT NULL,
    sent_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (alert_id, target_id, slot)
);

-- the reminder notice
ALTER TABLE notifications DROP CONSTRAINT notifications_type_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_type_check CHECK (((type)::text = ANY (ARRAY[('contact_link_request'::character varying)::text, ('account_invite'::character varying)::text, ('project_invite'::character varying)::text, ('recurring_due'::character varying)::text, ('recurring_ending'::character varying)::text, ('recurring_auto_created'::character varying)::text, ('debt_disconnected'::character varying)::text, ('debt_repaid'::character varying)::text, ('event_completed'::character varying)::text, ('event_member_removed'::character varying)::text, ('event_deleted'::character varying)::text, ('bill_invite'::character varying)::text, ('bill_new'::character varying)::text, ('bill_changed'::character varying)::text, ('bill_removed'::character varying)::text, ('bill_owner_changed'::character varying)::text, ('bill_moved'::character varying)::text, ('recurring_due_soon'::character varying)::text])));
