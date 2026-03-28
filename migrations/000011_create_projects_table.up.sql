CREATE TABLE projects (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name VARCHAR(100) NOT NULL,
    type VARCHAR(50),
    budget_goal DECIMAL(15,2),
    start_date DATE,
    end_date DATE,
    status VARCHAR(20) NOT NULL DEFAULT 'active', -- active | completed | cancelled
    description TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX idx_projects_user ON projects(user_id);

-- Now add the FK from transactions to projects
ALTER TABLE transactions ADD CONSTRAINT fk_transactions_project
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE SET NULL;
