CREATE TABLE saving_goals (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name VARCHAR(100) NOT NULL,
    target_amount DECIMAL(15,2) NOT NULL,
    current_amount DECIMAL(15,2) NOT NULL DEFAULT 0,
    account_id BIGINT REFERENCES accounts(id) ON DELETE SET NULL,
    deadline DATE,
    is_completed BOOLEAN NOT NULL DEFAULT false,
    icon VARCHAR(50),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX idx_saving_goals_user ON saving_goals(user_id);
