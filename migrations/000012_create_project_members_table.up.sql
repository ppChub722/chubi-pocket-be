CREATE TABLE project_members (
    id BIGSERIAL PRIMARY KEY,
    project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role VARCHAR(20) NOT NULL DEFAULT 'viewer', -- owner | contributor | viewer
    joined_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX idx_project_members_project ON project_members(project_id);
CREATE INDEX idx_project_members_user ON project_members(user_id);
CREATE UNIQUE INDEX idx_project_members_unique ON project_members(project_id, user_id);
