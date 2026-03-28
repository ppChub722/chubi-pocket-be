CREATE TABLE budgets (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    category_id BIGINT NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
    amount DECIMAL(15,2) NOT NULL,
    period VARCHAR(20) NOT NULL, -- weekly | monthly | yearly
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX idx_budgets_user ON budgets(user_id);
CREATE UNIQUE INDEX idx_budgets_user_category_period ON budgets(user_id, category_id, period);
