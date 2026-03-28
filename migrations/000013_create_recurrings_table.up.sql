CREATE TABLE recurrings (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    name VARCHAR(100) NOT NULL,
    type VARCHAR(20) NOT NULL, -- expense | income
    entry_type VARCHAR(20) NOT NULL, -- recurring | installment
    amount DECIMAL(15,2) NOT NULL,
    category_id BIGINT REFERENCES categories(id) ON DELETE SET NULL,
    billing_cycle VARCHAR(20) NOT NULL, -- daily | weekly | monthly | yearly
    next_billing_date DATE NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'active', -- active | paused | cancelled | completed
    -- Installment-specific fields
    total_amount DECIMAL(15,2),
    down_payment DECIMAL(15,2),
    monthly_payment DECIMAL(15,2),
    total_installments INTEGER,
    remaining_installments INTEGER,
    note TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX idx_recurrings_user ON recurrings(user_id);
CREATE INDEX idx_recurrings_next_billing ON recurrings(status, next_billing_date);

-- Now add the FK from transactions to recurrings
ALTER TABLE transactions ADD CONSTRAINT fk_transactions_recurring
    FOREIGN KEY (recurring_id) REFERENCES recurrings(id) ON DELETE SET NULL;
