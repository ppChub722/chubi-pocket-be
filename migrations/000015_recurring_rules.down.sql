DROP TRIGGER recurring_rules_drop_empty ON recurring_rules;
DROP FUNCTION recurring_drop_empty();

-- Back to the 014 shape. Item fields come back from the item's first rule; alerts, notify-only and
-- the removed notices come back empty.

ALTER TABLE recurrings DROP CONSTRAINT recurrings_status_check;
ALTER TABLE recurrings ADD CONSTRAINT recurrings_status_check CHECK (status IN ('active', 'paused', 'archived', 'done'));
ALTER TABLE recurrings
    ADD COLUMN type                        varchar(20),
    ADD COLUMN category_id                 uuid REFERENCES categories(id) ON DELETE SET NULL,
    ADD COLUMN tag_ids                     uuid[] NOT NULL DEFAULT '{}',
    ADD COLUMN goal_id                     uuid REFERENCES saving_goals(id) ON DELETE CASCADE,
    ADD COLUMN counterparty_contact_id     uuid REFERENCES contacts(id) ON DELETE SET NULL,
    ADD COLUMN counterparty_person_name    varchar(100),
    ADD COLUMN counterparty_institution_id uuid REFERENCES institutions(id) ON DELETE RESTRICT,
    ADD COLUMN counterparty_account_id     uuid REFERENCES accounts(id) ON DELETE CASCADE,
    ADD COLUMN debt_direction              varchar(20) CHECK (debt_direction IN ('paid', 'received')),
    ADD COLUMN debt_plan_id                uuid REFERENCES debt_plans(id) ON DELETE CASCADE;
UPDATE recurrings g SET type = CASE WHEN r.type = 'transfer' AND r.to_account_id IS NOT NULL AND g.tracking_type = 'credit_card'
            THEN 'debt' ELSE r.type END,
       category_id = r.category_id, tag_ids = r.tag_ids, goal_id = r.goal_id,
       counterparty_contact_id = CASE WHEN r.type = 'debt' THEN r.contact_id END,
       counterparty_person_name = CASE WHEN r.type = 'debt' AND r.institution_id IS NULL AND r.debt_plan_id IS NULL THEN r.person_name END,
       counterparty_institution_id = CASE WHEN r.debt_plan_id IS NULL THEN r.institution_id END,
       counterparty_account_id = CASE WHEN g.tracking_type = 'credit_card' AND r.type = 'transfer' THEN r.to_account_id END,
       debt_direction = CASE WHEN r.type = 'debt' OR (g.tracking_type = 'credit_card' AND r.type = 'transfer') THEN 'received' END,
       debt_plan_id = r.debt_plan_id
FROM (SELECT DISTINCT ON (recurring_id) * FROM recurring_rules ORDER BY recurring_id, created_at, id) r
WHERE r.recurring_id = g.id;
UPDATE recurrings SET type = 'expense' WHERE type IS NULL;
ALTER TABLE recurrings ALTER COLUMN type SET NOT NULL;
ALTER TABLE recurrings ADD CONSTRAINT recurrings_type_check CHECK (type IN ('expense', 'income', 'transfer', 'saving', 'debt'));
ALTER TABLE recurrings ADD CONSTRAINT recurrings_debt_target CHECK (
    ((type = 'debt') = ((debt_plan_id IS NOT NULL)::int + (counterparty_account_id IS NOT NULL)::int
        + (counterparty_institution_id IS NOT NULL)::int + (counterparty_person_name IS NOT NULL)::int = 1))
    AND (counterparty_contact_id IS NULL OR counterparty_person_name IS NOT NULL)
    AND ((type = 'debt') = (debt_direction IS NOT NULL)));
ALTER TABLE recurrings DROP COLUMN tracking_type;
CREATE INDEX idx_recurrings_contact ON recurrings (counterparty_contact_id) WHERE counterparty_contact_id IS NOT NULL;
CREATE INDEX idx_recurrings_institution ON recurrings (counterparty_institution_id) WHERE counterparty_institution_id IS NOT NULL;
CREATE INDEX idx_recurrings_goal ON recurrings (goal_id) WHERE goal_id IS NOT NULL;
CREATE INDEX idx_recurrings_plan ON recurrings (debt_plan_id) WHERE debt_plan_id IS NOT NULL;
CREATE INDEX idx_recurrings_account ON recurrings (counterparty_account_id) WHERE counterparty_account_id IS NOT NULL;

ALTER TABLE recurring_rules DROP CONSTRAINT recurring_rules_month_days_check, DROP CONSTRAINT recurring_rules_months_check;
ALTER TABLE recurring_rules ADD COLUMN month_day integer;
UPDATE recurring_rules SET month_day = CASE WHEN month_days[1] = -1 THEN 31 ELSE month_days[1] END WHERE cardinality(month_days) > 0;
ALTER TABLE recurring_rules DROP COLUMN month_days, DROP COLUMN months;

ALTER TABLE recurring_rules
    ADD COLUMN name            varchar(100),
    ADD COLUMN description     text,
    ADD COLUMN note            text,
    ADD COLUMN icon_code       jsonb,
    ADD COLUMN ending_notified boolean NOT NULL DEFAULT false,
    ADD COLUMN notify_only     boolean NOT NULL DEFAULT false;
UPDATE recurring_rules r SET name = g.name, description = g.description, note = g.note, icon_code = g.icon_code
FROM recurrings g WHERE g.id = r.recurring_id;
ALTER TABLE recurring_rules ALTER COLUMN name SET NOT NULL;

ALTER TABLE notifications DROP CONSTRAINT notifications_type_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_type_check CHECK (((type)::text = ANY (ARRAY[('contact_link_request'::character varying)::text, ('account_invite'::character varying)::text, ('project_invite'::character varying)::text, ('recurring_due'::character varying)::text, ('recurring_ending'::character varying)::text, ('recurring_auto_created'::character varying)::text, ('debt_disconnected'::character varying)::text, ('debt_repaid'::character varying)::text, ('event_completed'::character varying)::text, ('event_member_removed'::character varying)::text, ('event_deleted'::character varying)::text, ('bill_invite'::character varying)::text, ('bill_new'::character varying)::text, ('bill_changed'::character varying)::text, ('bill_removed'::character varying)::text, ('bill_owner_changed'::character varying)::text, ('bill_moved'::character varying)::text, ('recurring_due_soon'::character varying)::text])));
CREATE TABLE recurring_reminder_alerts (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    reminder_id uuid NOT NULL REFERENCES recurring_rules(id) ON DELETE CASCADE,
    anchor      varchar(10) NOT NULL CHECK (anchor IN ('due', 'start', 'end')),
    days_before integer NOT NULL DEFAULT 0 CHECK (days_before BETWEEN 0 AND 60),
    time        time NOT NULL DEFAULT '09:00',
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_recurring_reminder_alerts_reminder ON recurring_reminder_alerts (reminder_id);
CREATE INDEX idx_recurring_reminder_alerts_user ON recurring_reminder_alerts (user_id);
CREATE TABLE recurring_reminder_alert_sends (
    alert_id  uuid NOT NULL REFERENCES recurring_reminder_alerts(id) ON DELETE CASCADE,
    target_id uuid NOT NULL,
    slot      date NOT NULL,
    sent_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (alert_id, target_id, slot)
);

ALTER TABLE users DROP COLUMN recurring_notify_default, DROP COLUMN recurring_auto_accept_default;
ALTER TABLE recurring_rules RENAME COLUMN auto_accepted_until TO auto_created_until;
UPDATE recurring_rules SET auto_accept = FALSE WHERE auto_accept IS NULL;
ALTER TABLE recurring_rules ALTER COLUMN auto_accept SET NOT NULL, ALTER COLUMN auto_accept SET DEFAULT false;
ALTER TABLE recurring_rules RENAME COLUMN auto_accept TO auto_create;
ALTER TABLE recurring_rules DROP COLUMN notify;

DROP INDEX idx_recurring_rules_account;
DROP INDEX idx_recurring_rules_to_account;
DROP INDEX idx_recurring_rules_institution;
DROP INDEX idx_recurring_rules_plan;
ALTER TABLE recurring_rules DROP COLUMN institution_id, DROP COLUMN debt_plan_id;

UPDATE pending_transactions SET draft = (draft - 'recurring_rule_id') || jsonb_build_object('recurring_reminder_id', draft->'recurring_rule_id')
WHERE draft ? 'recurring_rule_id';
ALTER TABLE recurring_items RENAME CONSTRAINT recurring_items_recurring_rule_id_fkey TO recurring_items_recurring_reminder_id_fkey;
ALTER INDEX idx_recurring_items_rule_slot RENAME TO idx_recurring_items_reminder_slot;
ALTER INDEX idx_recurring_items_rule RENAME TO idx_recurring_items_reminder;
ALTER TABLE recurring_items RENAME COLUMN recurring_rule_id TO recurring_reminder_id;
ALTER TABLE recurring_rules RENAME COLUMN repays_rule_id TO repays_reminder_id;
ALTER INDEX idx_recurring_rules_recurring RENAME TO idx_recurring_reminders_recurring;
ALTER TABLE recurring_rules RENAME CONSTRAINT recurring_rules_pkey TO recurring_reminders_pkey;
ALTER TABLE recurring_rules RENAME TO recurring_reminders;
