CREATE TABLE IF NOT EXISTS categories (
    id                  UUID         PRIMARY KEY,
    user_id             UUID         NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name                VARCHAR(100) NOT NULL,
    type                VARCHAR(10)  NOT NULL
                          CHECK (type IN ('income', 'expense')),
    parent_id           UUID         REFERENCES categories(id),
    is_system           BOOLEAN      NOT NULL DEFAULT FALSE,
    -- system_kind: stable identifier for the 6 reserved system categories.
    -- NULL for user categories. Required because spec §4.14 allows renaming
    -- the display `name` of system categories, so name-based lookup is fragile.
    -- Values: OPENING_IN, OPENING_OUT, ADJUST_IN, ADJUST_OUT, TRANSFER_IN, TRANSFER_OUT.
    system_kind         VARCHAR(30),
    icon                VARCHAR(50),
    color               VARCHAR(7),
    status              VARCHAR(20)  NOT NULL DEFAULT 'active'
                          CHECK (status IN ('active', 'archived')),
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    created_by_user_id  UUID         REFERENCES users(id),
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_by_user_id  UUID         REFERENCES users(id),
    CONSTRAINT chk_categories_no_self_parent CHECK (id <> parent_id),
    CONSTRAINT chk_categories_system_kind CHECK (
        (is_system = TRUE  AND system_kind IS NOT NULL)
     OR (is_system = FALSE AND system_kind IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_categories_user
    ON categories(user_id, status);

CREATE INDEX IF NOT EXISTS idx_categories_parent
    ON categories(parent_id);

-- Sibling-name uniqueness is case-insensitive and excludes archived rows so
-- users can reuse names after archiving. NULLS NOT DISTINCT (PG 15+) so root
-- categories (parent_id IS NULL) participate in the uniqueness check.
CREATE UNIQUE INDEX IF NOT EXISTS idx_categories_name
    ON categories(user_id, type, parent_id, LOWER(name))
    NULLS NOT DISTINCT
    WHERE status = 'active';

-- One row per (user, system_kind) — guards against duplicate system seeding.
CREATE UNIQUE INDEX IF NOT EXISTS idx_categories_system_kind
    ON categories(user_id, system_kind)
    WHERE system_kind IS NOT NULL;

CREATE TRIGGER set_timestamp_categories
BEFORE UPDATE ON categories
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();
