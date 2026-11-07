-- Chatbot tiers (0.4.2-4): a tier "tester" beside free / owner. Which tiers may use the chatbot's API and which
-- save for real are env lists (CHATBOT_API_TIERS, CHATBOT_SAVE_TIERS), not part of the schema.
ALTER TABLE users DROP CONSTRAINT users_tier_check;
ALTER TABLE users ADD CONSTRAINT users_tier_check CHECK (tier IN ('free', 'owner', 'tester'));
UPDATE users SET tier = 'tester' WHERE username IN ('tester', 'tester1');
