CREATE TABLE accounts (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name VARCHAR(100) NOT NULL,
    type VARCHAR(20) NOT NULL, -- 'bank', 'cash', 'credit'
    currency VARCHAR(3) NOT NULL DEFAULT 'THB',
    initial_balance DECIMAL(19,4) NOT NULL DEFAULT 0,
    current_balance DECIMAL(19,4) NOT NULL DEFAULT 0,
    color VARCHAR(7),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);
CREATE INDEX idx_accounts_user ON accounts(user_id);
