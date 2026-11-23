-- 0.4.3: a recurring ITEM (recurrings) as the user sees it, with any number of REMINDERS
-- (recurring_rules renamed recurring_reminders). Every existing rule becomes one item WITH THE SAME
-- ID, so every old link (/recurring/{id}, a tx's recurring link, notices) still opens the right item.
-- The item's type (5 values) is fixed once created and decides what its reminders can do.
-- The item's fields stay copied on its reminders (name, category, tags, goal, counterparty) so
-- the round engine keeps reading one reminder; the item is the source.

CREATE TABLE recurrings (
    id                          uuid PRIMARY KEY,
    user_id                     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name                        varchar(100) NOT NULL,
    description                 text,
    note                        text,
    icon_code                   jsonb,
    type                        varchar(20) NOT NULL CHECK (type IN ('expense', 'income', 'transfer', 'saving', 'debt')),
    category_id                 uuid REFERENCES categories(id) ON DELETE SET NULL,
    tag_ids                     uuid[] NOT NULL DEFAULT '{}',
    goal_id                     uuid REFERENCES saving_goals(id) ON DELETE CASCADE,
    status                      varchar(20) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused', 'archived', 'done')),
    -- type debt: who (exactly one of contact / typed name / institution; a contact keeps its name)
    counterparty_contact_id     uuid REFERENCES contacts(id) ON DELETE SET NULL,
    counterparty_person_name    varchar(100),
    counterparty_institution_id uuid REFERENCES institutions(id) ON DELETE RESTRICT,
    debt_direction              varchar(20) CHECK (debt_direction IN ('paid', 'received')),
    created_at                  timestamptz NOT NULL DEFAULT now(),
    updated_at                  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT recurrings_counterparty_check CHECK (
        (type = 'debt') = (counterparty_person_name IS NOT NULL OR counterparty_institution_id IS NOT NULL)
        AND (counterparty_contact_id IS NULL OR counterparty_institution_id IS NULL)
        AND (type = 'debt' OR (counterparty_contact_id IS NULL AND debt_direction IS NULL)))
);
CREATE INDEX idx_recurrings_user ON recurrings (user_id, status);
CREATE INDEX idx_recurrings_contact ON recurrings (counterparty_contact_id) WHERE counterparty_contact_id IS NOT NULL;
CREATE INDEX idx_recurrings_institution ON recurrings (counterparty_institution_id) WHERE counterparty_institution_id IS NOT NULL;
CREATE INDEX idx_recurrings_goal ON recurrings (goal_id) WHERE goal_id IS NOT NULL;
CREATE TRIGGER set_timestamp_recurrings BEFORE UPDATE ON recurrings FOR EACH ROW EXECUTE FUNCTION set_timestamp();

-- 1-1 from the rules, same id. Type from the old kind: subscription / installment / reminder =
-- expense (a transfer reminder: transfer), the rest by name.
INSERT INTO recurrings (id, user_id, name, description, note, icon_code, type, category_id, tag_ids, goal_id, status,
                        counterparty_contact_id, counterparty_person_name, debt_direction, created_at, updated_at)
SELECT r.id, r.user_id, r.name, r.description, r.note, r.icon_code,
       CASE r.kind
            WHEN 'subscription' THEN 'expense'
            WHEN 'installment'  THEN 'expense'
            WHEN 'income'       THEN 'income'
            WHEN 'reminder'     THEN CASE WHEN r.type = 'transfer' THEN 'transfer' ELSE 'expense' END
            WHEN 'saving'       THEN 'saving'
            WHEN 'debt'         THEN 'debt'
       END,
       r.category_id, r.tag_ids, r.goal_id, r.status,
       CASE WHEN r.kind = 'debt' THEN r.contact_id END,
       CASE WHEN r.kind = 'debt' THEN r.person_name END,
       CASE WHEN r.kind = 'debt' THEN r.debt_direction END,
       r.created_at, r.updated_at
FROM recurring_rules r;

ALTER TABLE recurring_rules ADD COLUMN recurring_id uuid REFERENCES recurrings(id) ON DELETE CASCADE;
UPDATE recurring_rules SET recurring_id = id;
ALTER TABLE recurring_rules ALTER COLUMN recurring_id SET NOT NULL;
CREATE INDEX idx_recurring_rules_recurring ON recurring_rules (recurring_id);

-- a line's source: a wallet, or a person paying for me (คนจ่ายแทน: contact, else a typed name;
-- the name is always kept). Never both; "neither" is refused by the API (a wallet's delete
-- clears account_id).
ALTER TABLE recurring_rules
    ADD COLUMN paid_by_contact_id uuid REFERENCES contacts(id) ON DELETE SET NULL,
    ADD COLUMN paid_by_person_name varchar(100);
ALTER TABLE recurring_rules ADD CONSTRAINT recurring_rules_one_source
    CHECK (paid_by_person_name IS NULL OR account_id IS NULL);
ALTER TABLE recurring_rules ADD CONSTRAINT recurring_rules_paid_by_name
    CHECK (paid_by_contact_id IS NULL OR paid_by_person_name IS NOT NULL);

-- amount_mode: fixed = the amount set on the reminder; ask = typed on each round (a bill: a round
-- is due with no amount until confirmed). Old reminder-kind rules with no amount ask.
ALTER TABLE recurring_rules ADD COLUMN amount_mode varchar(10) NOT NULL DEFAULT 'fixed'
    CHECK (amount_mode IN ('fixed', 'ask'));
UPDATE recurring_rules SET amount_mode = 'ask' WHERE kind = 'reminder' AND amount = 0;

-- repay_paid_by: the person-paid reminder of the same item this one pays back (amount = its latest
-- recorded round)
ALTER TABLE recurring_rules ADD COLUMN repays_reminder_id uuid REFERENCES recurring_rules(id) ON DELETE CASCADE;

-- rule → reminder (the UI word)
ALTER TABLE recurring_rules RENAME TO recurring_reminders;
ALTER TABLE recurring_reminders RENAME CONSTRAINT recurring_rules_pkey TO recurring_reminders_pkey;
ALTER INDEX idx_recurring_rules_recurring RENAME TO idx_recurring_reminders_recurring;

-- the rounds point at their reminder: rule_id → recurring_reminder_id
ALTER TABLE recurring_items RENAME COLUMN rule_id TO recurring_reminder_id;
ALTER INDEX idx_recurring_items_rule RENAME TO idx_recurring_items_reminder;
ALTER INDEX idx_recurring_items_rule_slot RENAME TO idx_recurring_items_reminder_slot;
ALTER TABLE recurring_items RENAME CONSTRAINT recurring_items_rule_id_fkey TO recurring_items_recurring_reminder_id_fkey;

-- a debt row of a recurring item (its opening amount and every row its rounds make); null = general.
-- Deleting the item keeps the rows as general debts of the counterparty.
ALTER TABLE personal_debts ADD COLUMN recurring_id uuid REFERENCES recurrings(id) ON DELETE SET NULL;
CREATE INDEX idx_personal_debts_recurring ON personal_debts (recurring_id) WHERE recurring_id IS NOT NULL;
UPDATE personal_debts d SET recurring_id = r.recurring_id
FROM recurring_items i JOIN recurring_reminders r ON r.id = i.recurring_reminder_id
WHERE i.debt_entry_id = d.id;

-- a pending row that records a round names the REMINDER now
UPDATE pending_transactions SET draft = (draft - 'recurring_id') || jsonb_build_object('recurring_reminder_id', draft->'recurring_id')
WHERE draft ? 'recurring_id';
