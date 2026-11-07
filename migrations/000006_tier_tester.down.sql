UPDATE users SET tier = 'free' WHERE tier = 'tester';
ALTER TABLE users DROP CONSTRAINT users_tier_check;
ALTER TABLE users ADD CONSTRAINT users_tier_check CHECK (tier IN ('free', 'owner'));
