DROP TABLE llm_calls;
ALTER TABLE users DROP CONSTRAINT users_tier_check;
ALTER TABLE users DROP COLUMN tier;
