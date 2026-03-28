CREATE TABLE tags (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name VARCHAR(50) NOT NULL,
    color VARCHAR(7),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX idx_tags_user ON tags(user_id);
-- Unique tag name per user
CREATE UNIQUE INDEX idx_tags_user_name ON tags(user_id, name);
