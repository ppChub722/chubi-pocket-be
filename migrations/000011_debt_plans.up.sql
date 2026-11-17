-- 0.4.3: a plan on a debt item (ผ่อน / loan repaid in installments) and its installments.
-- The installments are information and hand-set ticks: the balance counts only debt rows.
ALTER TABLE recurrings
    ADD COLUMN plan_principal          numeric(15,2) CHECK (plan_principal > 0),
    ADD COLUMN plan_count              integer CHECK (plan_count BETWEEN 1 AND 600),
    ADD COLUMN plan_start              date,
    ADD COLUMN plan_interest_method    varchar(10) CHECK (plan_interest_method IN ('none', 'flat', 'reducing', 'fixed', 'manual')),
    ADD COLUMN plan_interest_rate      numeric(7,4) CHECK (plan_interest_rate >= 0),
    ADD COLUMN plan_installment_amount numeric(15,2) CHECK (plan_installment_amount > 0),
    -- a card installment (ผ่อนผ่านบัตร): the card holds the debt, the item has no line and no debt row
    ADD COLUMN card_account_id         uuid REFERENCES accounts(id) ON DELETE SET NULL,
    ADD COLUMN plan_closed_at          timestamptz,
    ADD CONSTRAINT recurrings_plan_check CHECK (
        (plan_count IS NULL) = (plan_principal IS NULL)
        AND (plan_count IS NULL) = (plan_start IS NULL)
        AND (plan_count IS NULL) = (plan_interest_method IS NULL)
        AND (plan_count IS NULL OR type = 'debt'));

CREATE TABLE debt_installments (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    recurring_id     uuid NOT NULL REFERENCES recurrings(id) ON DELETE CASCADE,
    seq              integer NOT NULL CHECK (seq >= 1),
    due_date         date NOT NULL,
    amount           numeric(15,2) NOT NULL CHECK (amount >= 0),
    principal        numeric(15,2) NOT NULL CHECK (principal >= 0),
    interest         numeric(15,2) NOT NULL CHECK (interest >= 0),
    -- ticks set by hand ("จ่ายงวดแล้ว"), apart from money; skipped = closed early
    paid_at          timestamptz,
    skipped          boolean NOT NULL DEFAULT false,
    personal_debt_id uuid REFERENCES personal_debts(id) ON DELETE SET NULL,
    edited           boolean NOT NULL DEFAULT false,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (recurring_id, seq),
    CHECK (paid_at IS NULL OR NOT skipped)
);
CREATE INDEX idx_debt_installments_user_due ON debt_installments (user_id, due_date);
CREATE TRIGGER set_timestamp_debt_installments BEFORE UPDATE ON debt_installments FOR EACH ROW EXECUTE FUNCTION set_timestamp();
