-- Add currency and avatar_url to users table (per design spec)
ALTER TABLE users ADD COLUMN currency VARCHAR(3) NOT NULL DEFAULT 'THB';
ALTER TABLE users ADD COLUMN avatar_url TEXT;
