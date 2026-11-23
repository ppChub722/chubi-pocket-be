-- 0.4.3: a debt plan (ผ่อน / a loan repaid in installments), owned by the debts page. Its creditor
-- is exactly one of: a card / PayLater wallet (the debt is the wallet's balance: no debt row), an
-- institution, or a person (a contact, else a typed name; the name is always kept).
-- A recurring item of type debt may pay it (recurrings.debt_plan_id): its reminders then pay the
-- plan's rounds. The installments are worked out once and editable; paying one books a repayment
-- (the principal) and the interest (an expense).
CREATE TABLE debt_plans (
    id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id                 uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name                    varchar(100) NOT NULL,
    description             text,
    note                    text,
    category_id             uuid REFERENCES categories(id) ON DELETE SET NULL,
    -- paid = they owe me, received = I owe them (as personal_debts)
    direction               varchar(20) NOT NULL CHECK (direction IN ('paid', 'received')),
    account_id              uuid REFERENCES accounts(id) ON DELETE CASCADE,
    institution_id          uuid REFERENCES institutions(id) ON DELETE RESTRICT,
    contact_id              uuid REFERENCES contacts(id) ON DELETE SET NULL,
    person_name             varchar(100),
    -- purchase = bought on credit (the full price is an expense on the day); cash = a loan of money
    origin                  varchar(10) NOT NULL DEFAULT 'purchase' CHECK (origin IN ('purchase', 'cash')),
    principal               numeric(15,2) NOT NULL CHECK (principal > 0),
    count                   integer NOT NULL CHECK (count BETWEEN 1 AND 600),
    start_date              date NOT NULL,
    interest_method         varchar(10) NOT NULL CHECK (interest_method IN ('none', 'flat', 'reducing', 'fixed', 'manual')),
    interest_rate           numeric(7,4) CHECK (interest_rate >= 0),
    installment_amount      numeric(15,2) CHECK (installment_amount > 0),
    -- the purchase: on the card (a card plan) or the expense of a purchase on credit
    purchase_transaction_id uuid REFERENCES transactions(id) ON DELETE SET NULL,
    closed_at               timestamptz,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT debt_plans_one_creditor CHECK (
        (account_id IS NOT NULL)::int + (institution_id IS NOT NULL)::int + (person_name IS NOT NULL)::int = 1
        AND (contact_id IS NULL OR person_name IS NOT NULL)),
    CONSTRAINT debt_plans_card_owes CHECK (account_id IS NULL OR (direction = 'received' AND origin = 'purchase'))
);
CREATE INDEX idx_debt_plans_user ON debt_plans (user_id);
CREATE INDEX idx_debt_plans_account ON debt_plans (account_id) WHERE account_id IS NOT NULL;
CREATE INDEX idx_debt_plans_institution ON debt_plans (institution_id) WHERE institution_id IS NOT NULL;
CREATE INDEX idx_debt_plans_contact ON debt_plans (contact_id) WHERE contact_id IS NOT NULL;
CREATE TRIGGER set_timestamp_debt_plans BEFORE UPDATE ON debt_plans FOR EACH ROW EXECUTE FUNCTION set_timestamp();

CREATE TABLE debt_installments (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    debt_plan_id     uuid NOT NULL REFERENCES debt_plans(id) ON DELETE CASCADE,
    seq              integer NOT NULL CHECK (seq >= 1),
    due_date         date NOT NULL,
    amount           numeric(15,2) NOT NULL CHECK (amount >= 0),
    principal        numeric(15,2) NOT NULL CHECK (principal >= 0),
    interest         numeric(15,2) NOT NULL CHECK (interest >= 0),
    -- paid (a repayment, or a tick by hand) / skipped (closed early)
    paid_at          timestamptz,
    skipped          boolean NOT NULL DEFAULT false,
    personal_debt_id uuid REFERENCES personal_debts(id) ON DELETE SET NULL,
    edited           boolean NOT NULL DEFAULT false,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (debt_plan_id, seq),
    CHECK (paid_at IS NULL OR NOT skipped)
);
CREATE INDEX idx_debt_installments_user_due ON debt_installments (user_id, due_date);
CREATE TRIGGER set_timestamp_debt_installments BEFORE UPDATE ON debt_installments FOR EACH ROW EXECUTE FUNCTION set_timestamp();

-- a debt row of a plan (its opening row and its repayments)
ALTER TABLE personal_debts ADD COLUMN debt_plan_id uuid REFERENCES debt_plans(id) ON DELETE SET NULL;
CREATE INDEX idx_personal_debts_plan ON personal_debts (debt_plan_id) WHERE debt_plan_id IS NOT NULL;

-- a debt item pays exactly one debt: a plan, a card / PayLater wallet's bill, or a counterparty
-- (a contact / typed name / institution). A plan's item goes with its plan, a card's with its card.
ALTER TABLE recurrings
    ADD COLUMN debt_plan_id uuid REFERENCES debt_plans(id) ON DELETE CASCADE,
    ADD COLUMN counterparty_account_id uuid REFERENCES accounts(id) ON DELETE CASCADE;
CREATE INDEX idx_recurrings_plan ON recurrings (debt_plan_id) WHERE debt_plan_id IS NOT NULL;
CREATE INDEX idx_recurrings_account ON recurrings (counterparty_account_id) WHERE counterparty_account_id IS NOT NULL;
ALTER TABLE recurrings DROP CONSTRAINT recurrings_counterparty_check;
ALTER TABLE recurrings ADD CONSTRAINT recurrings_debt_target CHECK (
    ((type = 'debt') = ((debt_plan_id IS NOT NULL)::int + (counterparty_account_id IS NOT NULL)::int
        + (counterparty_institution_id IS NOT NULL)::int + (counterparty_person_name IS NOT NULL)::int = 1))
    AND (counterparty_contact_id IS NULL OR counterparty_person_name IS NOT NULL)
    AND ((type = 'debt') = (debt_direction IS NOT NULL)));
