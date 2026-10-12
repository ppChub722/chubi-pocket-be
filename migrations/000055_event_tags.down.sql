-- Back to free-text bill tags and an event type (best effort; personal
-- tags on events and tag icons are lost).

DROP TABLE IF EXISTS project_my_tags;

ALTER TABLE project_transactions ADD COLUMN tags TEXT[] NOT NULL DEFAULT '{}';
UPDATE project_transactions pt SET tags = sub.names
FROM (
    SELECT ptt.project_transaction_id AS id, ARRAY_AGG(et.name ORDER BY et.name) AS names
    FROM project_transaction_tags ptt JOIN event_tags et ON et.id = ptt.event_tag_id
    GROUP BY ptt.project_transaction_id
) sub
WHERE sub.id = pt.id;

DROP TABLE IF EXISTS project_transaction_tags;
DROP TABLE IF EXISTS event_tags;

ALTER TABLE projects ADD COLUMN type VARCHAR(30);
