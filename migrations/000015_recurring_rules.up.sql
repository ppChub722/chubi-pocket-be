-- Recurring + debts rework (spec now/briefs/recurring-debts-spec.md): a recurring holds rules; a
-- rule owns everything a round books (type, category, tags, wallet / from -> to, goal, debt target,
-- schedule, switches). Nothing records where a rule was made: pages pull rules by their columns.

-- 1. reminder -> rule (the table was recurring_rules before 010; its constraints kept that name)
ALTER TABLE recurring_reminders RENAME TO recurring_rules;
ALTER TABLE recurring_rules RENAME CONSTRAINT recurring_reminders_pkey TO recurring_rules_pkey;
ALTER INDEX idx_recurring_reminders_recurring RENAME TO idx_recurring_rules_recurring;
ALTER TABLE recurring_rules RENAME COLUMN repays_reminder_id TO repays_rule_id;
ALTER TABLE recurring_items RENAME COLUMN recurring_reminder_id TO recurring_rule_id;
ALTER INDEX idx_recurring_items_reminder RENAME TO idx_recurring_items_rule;
ALTER INDEX idx_recurring_items_reminder_slot RENAME TO idx_recurring_items_rule_slot;
ALTER TABLE recurring_items RENAME CONSTRAINT recurring_items_recurring_reminder_id_fkey TO recurring_items_recurring_rule_id_fkey;
UPDATE pending_transactions SET draft = (draft - 'recurring_reminder_id') || jsonb_build_object('recurring_rule_id', draft->'recurring_reminder_id')
WHERE draft ? 'recurring_reminder_id';

-- 2. the debt target moves to the rule (contact / person / direction are already copied there)
ALTER TABLE recurring_rules
    ADD COLUMN institution_id uuid REFERENCES institutions(id) ON DELETE RESTRICT,
    ADD COLUMN debt_plan_id   uuid REFERENCES debt_plans(id) ON DELETE CASCADE;
UPDATE recurring_rules r SET institution_id = g.counterparty_institution_id, debt_plan_id = g.debt_plan_id
FROM recurrings g WHERE g.id = r.recurring_id AND r.repays_rule_id IS NULL;
-- a plan rule takes its plan's creditor
UPDATE recurring_rules r SET institution_id = p.institution_id, contact_id = p.contact_id,
       person_name = COALESCE(p.person_name, (SELECT i.name FROM institutions i WHERE i.id = p.institution_id))
FROM debt_plans p WHERE p.id = r.debt_plan_id;

CREATE INDEX idx_recurring_rules_account ON recurring_rules (account_id) WHERE account_id IS NOT NULL;
CREATE INDEX idx_recurring_rules_to_account ON recurring_rules (to_account_id) WHERE to_account_id IS NOT NULL;
CREATE INDEX idx_recurring_rules_institution ON recurring_rules (institution_id) WHERE institution_id IS NOT NULL;
CREATE INDEX idx_recurring_rules_plan ON recurring_rules (debt_plan_id) WHERE debt_plan_id IS NOT NULL;

-- 3. switches: null = the user's default (users.recurring_*_default)
ALTER TABLE recurring_rules ADD COLUMN notify boolean;
ALTER TABLE recurring_rules RENAME COLUMN auto_create TO auto_accept;
ALTER TABLE recurring_rules ALTER COLUMN auto_accept DROP NOT NULL, ALTER COLUMN auto_accept DROP DEFAULT;
UPDATE recurring_rules SET auto_accept = NULL WHERE auto_accept = FALSE;
UPDATE recurring_rules SET auto_accept = NULL WHERE amount_mode = 'ask';
ALTER TABLE recurring_rules RENAME COLUMN auto_created_until TO auto_accepted_until;
ALTER TABLE users
    ADD COLUMN recurring_notify_default      boolean NOT NULL DEFAULT true,
    ADD COLUMN recurring_auto_accept_default boolean NOT NULL DEFAULT false;

-- 4. removed: alerts, notify-only, the ending notice, a rule's own name / text / icon (it shows
-- "Rule n" and its category's icon; rounds are named from the recurring)
DROP TABLE recurring_reminder_alert_sends;
DROP TABLE recurring_reminder_alerts;
DELETE FROM recurring_rules WHERE notify_only;
ALTER TABLE recurring_rules
    DROP COLUMN notify_only,
    DROP COLUMN ending_notified,
    DROP COLUMN name,
    DROP COLUMN description,
    DROP COLUMN note,
    DROP COLUMN icon_code;
DELETE FROM notifications WHERE type IN ('recurring_due_soon', 'recurring_auto_created', 'recurring_ending');
ALTER TABLE notifications DROP CONSTRAINT notifications_type_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_type_check CHECK (((type)::text = ANY (ARRAY[('contact_link_request'::character varying)::text, ('account_invite'::character varying)::text, ('project_invite'::character varying)::text, ('recurring_due'::character varying)::text, ('debt_disconnected'::character varying)::text, ('debt_repaid'::character varying)::text, ('event_completed'::character varying)::text, ('event_member_removed'::character varying)::text, ('event_deleted'::character varying)::text, ('bill_invite'::character varying)::text, ('bill_new'::character varying)::text, ('bill_changed'::character varying)::text, ('bill_removed'::character varying)::text, ('bill_owner_changed'::character varying)::text, ('bill_moved'::character varying)::text])));
UPDATE user_notification_settings SET muted_types = array_remove(array_remove(array_remove(muted_types,
    'recurring_due_soon'), 'recurring_auto_created'), 'recurring_ending');
-- recurring_due is no longer a mute: each rule's notify switch (default users.recurring_notify_default) decides
UPDATE users u SET recurring_notify_default = FALSE
FROM user_notification_settings s WHERE s.user_id = u.id AND 'recurring_due' = ANY(s.muted_types);
UPDATE user_notification_settings SET muted_types = array_remove(muted_types, 'recurring_due');

-- 5. repeat: several days a month (-1 = the month's last day), several months a year (+ days)
ALTER TABLE recurring_rules
    ADD COLUMN month_days integer[] NOT NULL DEFAULT '{}',
    ADD COLUMN months     integer[] NOT NULL DEFAULT '{}';
UPDATE recurring_rules SET month_days = ARRAY[month_day] WHERE month_day IS NOT NULL;
ALTER TABLE recurring_rules DROP COLUMN month_day;
ALTER TABLE recurring_rules ADD CONSTRAINT recurring_rules_month_days_check
    CHECK (month_days <@ ARRAY[-1,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31]);
ALTER TABLE recurring_rules ADD CONSTRAINT recurring_rules_months_check
    CHECK (months <@ ARRAY[1,2,3,4,5,6,7,8,9,10,11,12]);

-- a recurring always has a rule: one left with none (its goal / plan / wallet deleted its rules) goes
CREATE FUNCTION recurring_drop_empty() RETURNS trigger LANGUAGE plpgsql AS $
BEGIN
    DELETE FROM recurrings g WHERE g.id = OLD.recurring_id
        AND NOT EXISTS (SELECT 1 FROM recurring_rules r WHERE r.recurring_id = OLD.recurring_id);
    RETURN OLD;
END $;
CREATE TRIGGER recurring_rules_drop_empty AFTER DELETE ON recurring_rules
    FOR EACH ROW EXECUTE FUNCTION recurring_drop_empty();

-- 6. the recurring keeps only who it is; the tracking type is a filter
ALTER TABLE recurrings ADD COLUMN tracking_type varchar(20) NOT NULL DEFAULT 'other'
    CHECK (tracking_type IN ('bill', 'subscription', 'installment', 'credit_card', 'person_debt', 'income', 'saving', 'other'));
UPDATE recurrings SET tracking_type = CASE
    WHEN type = 'income' THEN 'income'
    WHEN type = 'saving' THEN 'saving'
    WHEN type = 'debt' AND counterparty_account_id IS NOT NULL THEN 'credit_card'
    WHEN type = 'debt' AND debt_plan_id IS NOT NULL THEN 'installment'
    WHEN type = 'debt' AND counterparty_institution_id IS NOT NULL THEN 'installment'
    WHEN type = 'debt' THEN 'person_debt'
    ELSE 'other' END;
UPDATE recurrings SET status = 'archived' WHERE status = 'done';
ALTER TABLE recurrings DROP CONSTRAINT recurrings_debt_target;
DROP INDEX idx_recurrings_contact;
DROP INDEX idx_recurrings_institution;
DROP INDEX idx_recurrings_goal;
DROP INDEX idx_recurrings_plan;
DROP INDEX idx_recurrings_account;
ALTER TABLE recurrings
    DROP COLUMN type,
    DROP COLUMN category_id,
    DROP COLUMN tag_ids,
    DROP COLUMN goal_id,
    DROP COLUMN counterparty_contact_id,
    DROP COLUMN counterparty_person_name,
    DROP COLUMN counterparty_institution_id,
    DROP COLUMN counterparty_account_id,
    DROP COLUMN debt_direction,
    DROP COLUMN debt_plan_id;
ALTER TABLE recurrings DROP CONSTRAINT recurrings_status_check;
ALTER TABLE recurrings ADD CONSTRAINT recurrings_status_check CHECK (status IN ('active', 'paused', 'archived'));
