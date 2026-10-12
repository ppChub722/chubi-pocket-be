-- Budgets v2 (0.3.1-10, owner mockup 2026-10-13): a NAMED personal tracker
-- with a free, overlapping scope (everything, or categories / tag names /
-- wallets) and its own time: recurring periods (each an instance with its
-- own amount / note) or one range (fixed or open until ปิดงบ).
-- Old budgets: personal ones become recurring budgets on their category
-- (+ sub-categories), periods as before; event-scope budgets are dropped.

ALTER TABLE budgets RENAME TO budgets_v1;

CREATE TABLE budgets (
    id          UUID PRIMARY KEY,
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        VARCHAR(100) NOT NULL,
    description TEXT,
    note        TEXT,
    icon_code   JSONB,
    amount      NUMERIC(15,2) NOT NULL CHECK (amount > 0),
    warn_pct    SMALLINT CHECK (warn_pct BETWEEN 1 AND 100),
    scope_all   BOOLEAN NOT NULL DEFAULT FALSE,
    tag_names   TEXT[] NOT NULL DEFAULT '{}',
    recurring   BOOLEAN NOT NULL DEFAULT FALSE,
    freq        VARCHAR(10) CHECK (freq IN ('daily', 'weekly', 'monthly', 'yearly')),
    interval_n  INT NOT NULL DEFAULT 1 CHECK (interval_n >= 1),
    start_date  DATE NOT NULL,
    -- periods are generated from anchor_date (= start_date, or the period a
    -- "this and following" rule change took effect at); earlier rows stay.
    anchor_date DATE NOT NULL,
    end_kind    VARCHAR(10) NOT NULL DEFAULT 'none' CHECK (end_kind IN ('none', 'date', 'count')),
    end_date    DATE,
    end_count   INT CHECK (end_count >= 1),
    closed_on   DATE,
    status      VARCHAR(10) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (recurring = (freq IS NOT NULL))
);
CREATE INDEX idx_budgets_v2_user ON budgets (user_id, status);
CREATE TRIGGER set_timestamp_budgets_v2 BEFORE UPDATE ON budgets
    FOR EACH ROW EXECUTE FUNCTION set_timestamp();

CREATE TABLE budget_categories (
    budget_id        UUID NOT NULL REFERENCES budgets(id) ON DELETE CASCADE,
    category_id      UUID NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
    include_children BOOLEAN NOT NULL DEFAULT TRUE,
    PRIMARY KEY (budget_id, category_id)
);

CREATE TABLE budget_wallets (
    budget_id  UUID NOT NULL REFERENCES budgets(id) ON DELETE CASCADE,
    account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    PRIMARY KEY (budget_id, account_id)
);

-- Periods that have started, each with its own amount (overridden = set
-- for that period only) and note. Later periods follow the budget.
CREATE TABLE budget_periods (
    id         UUID PRIMARY KEY,
    budget_id  UUID NOT NULL REFERENCES budgets(id) ON DELETE CASCADE,
    seq        INT NOT NULL,
    start_date DATE NOT NULL,
    end_date   DATE,
    amount     NUMERIC(15,2) NOT NULL CHECK (amount > 0),
    overridden BOOLEAN NOT NULL DEFAULT FALSE,
    note       TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (budget_id, start_date)
);

INSERT INTO budgets (id, user_id, name, description, note, icon_code, amount, warn_pct, recurring, freq,
                     start_date, anchor_date, status, created_at, updated_at)
SELECT b.id, b.user_id, b.name, b.description, b.note, c.icon_code, b.amount, 80, TRUE,
       CASE b.period WHEN 'weekly' THEN 'weekly' WHEN 'yearly' THEN 'yearly' ELSE 'monthly' END,
       CASE b.period WHEN 'weekly' THEN date_trunc('week', b.created_at)::date
                     WHEN 'yearly' THEN date_trunc('year', b.created_at)::date
                     ELSE date_trunc('month', b.created_at)::date END,
       CASE b.period WHEN 'weekly' THEN date_trunc('week', b.created_at)::date
                     WHEN 'yearly' THEN date_trunc('year', b.created_at)::date
                     ELSE date_trunc('month', b.created_at)::date END,
       b.status, b.created_at, b.updated_at
FROM budgets_v1 b JOIN categories c ON c.id = b.category_id
WHERE b.scope = 'user';

INSERT INTO budget_categories (budget_id, category_id, include_children)
SELECT b.id, b.category_id, TRUE FROM budgets_v1 b WHERE b.scope = 'user';

DROP TABLE budgets_v1;
