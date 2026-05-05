-- 033 — IconMaker: replace enum icon/color fields with unified icon_code JSONB.
-- Touches: users, accounts, categories, tags, contacts, projects,
--          project_transactions, project_members.
-- Creates: user_pack_permissions.
-- See design/database/schema.md §icon_code shape.

-- ── Step 1: Add new columns (keep old columns for data migration) ─────────────

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS icon_code JSONB;

ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS icon_code JSONB,
    ADD COLUMN IF NOT EXISTS logo_url  TEXT;

ALTER TABLE categories
    ADD COLUMN IF NOT EXISTS icon_code JSONB;

ALTER TABLE tags
    ADD COLUMN IF NOT EXISTS icon_code JSONB;

ALTER TABLE contacts
    ADD COLUMN IF NOT EXISTS icon_code JSONB;

ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS icon_code JSONB;

ALTER TABLE project_transactions
    ADD COLUMN IF NOT EXISTS category_icon_code JSONB;

ALTER TABLE project_members
    ADD COLUMN IF NOT EXISTS icon_code JSONB;

-- ── Step 2: Data migration ────────────────────────────────────────────────────
-- Old semantics:
--   accounts / categories : color = circle background → background:"solid", bgColors:[color], iconColors:["#FFFFFF"]
--   tags                  : color = icon color        → iconColors:[color],  background:null
--   contacts              : icon only                 → iconColors:[],       background:null
--   projects              : color_id same as accounts (stored as TEXT preset/hex)
--   project_transactions  : category_color_id same as accounts

UPDATE accounts
SET icon_code = jsonb_build_object(
    'icon',         icon,
    'iconColors',   jsonb_build_array('#FFFFFF'),
    'background',   CASE WHEN color IS NOT NULL THEN 'solid'::text ELSE NULL END,
    'bgColors',     CASE WHEN color IS NOT NULL THEN jsonb_build_array(color) ELSE '[]'::jsonb END,
    'border',       NULL,
    'borderColors', '[]'::jsonb
)
WHERE icon IS NOT NULL OR color IS NOT NULL;

UPDATE categories
SET icon_code = jsonb_build_object(
    'icon',         icon,
    'iconColors',   jsonb_build_array('#FFFFFF'),
    'background',   CASE WHEN color IS NOT NULL THEN 'solid'::text ELSE NULL END,
    'bgColors',     CASE WHEN color IS NOT NULL THEN jsonb_build_array(color) ELSE '[]'::jsonb END,
    'border',       NULL,
    'borderColors', '[]'::jsonb
)
WHERE icon IS NOT NULL OR color IS NOT NULL;

UPDATE tags
SET icon_code = jsonb_build_object(
    'icon',         icon,
    'iconColors',   CASE WHEN color IS NOT NULL THEN jsonb_build_array(color) ELSE '[]'::jsonb END,
    'background',   NULL,
    'bgColors',     '[]'::jsonb,
    'border',       NULL,
    'borderColors', '[]'::jsonb
)
WHERE icon IS NOT NULL OR color IS NOT NULL;

UPDATE contacts
SET icon_code = jsonb_build_object(
    'icon',         icon,
    'iconColors',   '[]'::jsonb,
    'background',   NULL,
    'bgColors',     '[]'::jsonb,
    'border',       NULL,
    'borderColors', '[]'::jsonb
)
WHERE icon IS NOT NULL;

UPDATE projects
SET icon_code = jsonb_build_object(
    'icon',         icon_id,
    'iconColors',   jsonb_build_array('#FFFFFF'),
    'background',   CASE WHEN color_id IS NOT NULL THEN 'solid'::text ELSE NULL END,
    'bgColors',     CASE WHEN color_id IS NOT NULL THEN jsonb_build_array(color_id) ELSE '[]'::jsonb END,
    'border',       NULL,
    'borderColors', '[]'::jsonb
)
WHERE icon_id IS NOT NULL OR color_id IS NOT NULL;

UPDATE project_transactions
SET category_icon_code = jsonb_build_object(
    'icon',         category_icon_id,
    'iconColors',   jsonb_build_array('#FFFFFF'),
    'background',   CASE WHEN category_color_id IS NOT NULL THEN 'solid'::text ELSE NULL END,
    'bgColors',     CASE WHEN category_color_id IS NOT NULL THEN jsonb_build_array(category_color_id) ELSE '[]'::jsonb END,
    'border',       NULL,
    'borderColors', '[]'::jsonb
)
WHERE category_icon_id IS NOT NULL OR category_color_id IS NOT NULL;

-- ── Step 3: Drop old columns ──────────────────────────────────────────────────

ALTER TABLE users
    DROP COLUMN IF EXISTS avatar_url;

ALTER TABLE accounts
    DROP COLUMN IF EXISTS icon,
    DROP COLUMN IF EXISTS color;

ALTER TABLE categories
    DROP COLUMN IF EXISTS icon,
    DROP COLUMN IF EXISTS color;

ALTER TABLE tags
    DROP COLUMN IF EXISTS icon,
    DROP COLUMN IF EXISTS color;

ALTER TABLE contacts
    DROP COLUMN IF EXISTS icon;

ALTER TABLE projects
    DROP COLUMN IF EXISTS icon_id,
    DROP COLUMN IF EXISTS color_id;

ALTER TABLE project_transactions
    DROP COLUMN IF EXISTS category_icon_id,
    DROP COLUMN IF EXISTS category_color_id;

-- ── Step 4: user_pack_permissions ─────────────────────────────────────────────
-- Tracks which DLC packs each user has access to.
-- Base pack is always available in FE — no row needed for it.
-- expires_at: NULL = permanent; non-NULL = seasonal pack expiry.

CREATE TABLE IF NOT EXISTS user_pack_permissions (
    id          UUID        PRIMARY KEY,
    user_id     UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    pack_id     TEXT        NOT NULL,
    granted_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_pack_permissions_user_pack
    ON user_pack_permissions(user_id, pack_id);

CREATE INDEX IF NOT EXISTS idx_user_pack_permissions_user
    ON user_pack_permissions(user_id);

-- Efficient sweep for expired-pack cleanup jobs.
CREATE INDEX IF NOT EXISTS idx_user_pack_permissions_expires
    ON user_pack_permissions(expires_at)
    WHERE expires_at IS NOT NULL;

CREATE TRIGGER set_timestamp_user_pack_permissions
BEFORE UPDATE ON user_pack_permissions
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();
