-- A starter category keeps its catalog key so its name/description can follow the user's language
-- (categories.Service.Relocalize). NULL = a category the user made, or an old row not matched yet
-- (categories.BackfillSeedKeys matches those at start-up).
ALTER TABLE categories ADD COLUMN seed_key varchar(60);
CREATE UNIQUE INDEX idx_categories_seed_key ON categories (user_id, seed_key) WHERE seed_key IS NOT NULL;
