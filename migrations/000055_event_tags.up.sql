-- 0.3.1-10 events: the event `type` is dropped; an event carries each
-- member's own PERSONAL tags (private, many); bills use EVENT-SCOPED tags (a
-- tag set owned by the event, shared by its members, with icon / colour).

ALTER TABLE projects DROP COLUMN type;

CREATE TABLE event_tags (
    id                 UUID PRIMARY KEY,
    project_id         UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name               VARCHAR(50) NOT NULL,
    icon_code          JSONB,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX idx_event_tags_name ON event_tags (project_id, LOWER(name));
CREATE TRIGGER set_timestamp_event_tags BEFORE UPDATE ON event_tags
    FOR EACH ROW EXECUTE FUNCTION set_timestamp();

CREATE TABLE project_transaction_tags (
    project_transaction_id UUID NOT NULL REFERENCES project_transactions(id) ON DELETE CASCADE,
    event_tag_id           UUID NOT NULL REFERENCES event_tags(id) ON DELETE CASCADE,
    PRIMARY KEY (project_transaction_id, event_tag_id)
);
CREATE INDEX idx_project_transaction_tags_tag ON project_transaction_tags (event_tag_id);

-- The free-text names on bills (parents) become event tags: one per name per
-- event (case-insensitive, the first spelling wins).
INSERT INTO event_tags (id, project_id, name, created_at)
SELECT gen_random_uuid(), project_id, name, first_used
FROM (
    SELECT DISTINCT ON (pt.project_id, LOWER(TRIM(t.name)))
           pt.project_id, LEFT(TRIM(t.name), 50) AS name, pt.created_at AS first_used
    FROM project_transactions pt
    CROSS JOIN LATERAL unnest(pt.tags) AS t(name)
    WHERE pt.parent_project_transaction_id IS NULL AND TRIM(t.name) <> ''
    ORDER BY pt.project_id, LOWER(TRIM(t.name)), pt.created_at
) names;

INSERT INTO project_transaction_tags (project_transaction_id, event_tag_id)
SELECT DISTINCT pt.id, et.id
FROM project_transactions pt
CROSS JOIN LATERAL unnest(pt.tags) AS t(name)
JOIN event_tags et ON et.project_id = pt.project_id AND LOWER(et.name) = LOWER(LEFT(TRIM(t.name), 50))
WHERE pt.parent_project_transaction_id IS NULL;

ALTER TABLE project_transactions DROP COLUMN tags;

-- Each member's personal tags on an event (private to that member).
CREATE TABLE project_my_tags (
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tag_id     UUID NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project_id, tag_id)
);
CREATE INDEX idx_project_my_tags_user ON project_my_tags (user_id, tag_id);
