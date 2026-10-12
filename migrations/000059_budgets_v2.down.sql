-- Best effort: back to the v1 table; v2 budgets on one category come back
-- as monthly personal budgets, the rest are lost.
CREATE TABLE budgets_v1 (
    id                 UUID PRIMARY KEY,
    user_id            UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    category_id        UUID NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
    scope              VARCHAR(20) NOT NULL CHECK (scope IN ('user', 'project')),
    project_id         UUID REFERENCES projects(id) ON DELETE CASCADE,
    amount             NUMERIC(15,2) NOT NULL CHECK (amount > 0),
    period             VARCHAR(20) NOT NULL CHECK (period IN ('weekly', 'monthly', 'yearly')),
    currency           VARCHAR(3) NOT NULL,
    status             VARCHAR(20) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by_user_id UUID REFERENCES users(id),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_by_user_id UUID REFERENCES users(id),
    description        TEXT,
    note               TEXT,
    name               VARCHAR(100) NOT NULL,
    CONSTRAINT chk_budgets_scope_project CHECK ((scope = 'project') = (project_id IS NOT NULL))
);

INSERT INTO budgets_v1 (id, user_id, category_id, scope, amount, period, currency, status, created_at, updated_at,
                        description, note, name)
SELECT b.id, b.user_id, bc.category_id, 'user', b.amount,
       CASE b.freq WHEN 'weekly' THEN 'weekly' WHEN 'yearly' THEN 'yearly' ELSE 'monthly' END,
       u.currency, b.status, b.created_at, b.updated_at, b.description, b.note, b.name
FROM budgets b
JOIN users u ON u.id = b.user_id
JOIN LATERAL (SELECT category_id FROM budget_categories WHERE budget_id = b.id LIMIT 1) bc ON TRUE;

DROP TABLE budget_periods;
DROP TABLE budget_wallets;
DROP TABLE budget_categories;
DROP TABLE budgets;
ALTER TABLE budgets_v1 RENAME TO budgets;
CREATE INDEX idx_budgets_user ON budgets (user_id, status);
CREATE INDEX idx_budgets_project ON budgets (project_id) WHERE project_id IS NOT NULL;
CREATE TRIGGER set_timestamp_budgets BEFORE UPDATE ON budgets
    FOR EACH ROW EXECUTE FUNCTION set_timestamp();
