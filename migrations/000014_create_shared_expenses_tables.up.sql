CREATE TABLE shared_expenses (
    id BIGSERIAL PRIMARY KEY,
    transaction_id BIGINT NOT NULL REFERENCES transactions(id) ON DELETE CASCADE,
    total_amount DECIMAL(15,2) NOT NULL,
    description VARCHAR(255),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_shared_expenses_transaction ON shared_expenses(transaction_id);

CREATE TABLE shared_expense_splits (
    id BIGSERIAL PRIMARY KEY,
    shared_expense_id BIGINT NOT NULL REFERENCES shared_expenses(id) ON DELETE CASCADE,
    person_name VARCHAR(100) NOT NULL,
    owed_amount DECIMAL(15,2) NOT NULL,
    paid_amount DECIMAL(15,2) NOT NULL DEFAULT 0,
    is_settled BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX idx_shared_expense_splits_expense ON shared_expense_splits(shared_expense_id);

CREATE TABLE split_settlements (
    id BIGSERIAL PRIMARY KEY,
    split_id BIGINT NOT NULL REFERENCES shared_expense_splits(id) ON DELETE CASCADE,
    amount DECIMAL(15,2) NOT NULL,
    settled_date DATE NOT NULL,
    note TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX idx_split_settlements_split ON split_settlements(split_id);
