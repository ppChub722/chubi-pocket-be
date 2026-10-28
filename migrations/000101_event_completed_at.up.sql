-- 0.4.1-2 V1 / V2: completing an event locks it for good (owner 2026-10-26). completed_at set = never
-- back to active / cancelled, no delete, no bill / member / event edits, no sync; archiving keeps it
-- and restoring an archived one returns it to completed. Events completed before this are locked too.
ALTER TABLE projects ADD COLUMN completed_at TIMESTAMPTZ;
UPDATE projects SET completed_at = updated_at WHERE status = 'completed';

-- Backstop for a bill write racing the complete call: a bill row of a completed event can't be added
-- or changed. The trigger reads the event row FOR SHARE, so it waits for a complete in flight
-- (which holds it FOR UPDATE). The API checks first (409 EVENT_COMPLETED); this only catches a race.
-- UPDATE fires only on the bill's own values, so link columns (linked_transaction_id ... SET NULL when
-- a member deletes their now-plain tx) still change.
CREATE OR REPLACE FUNCTION project_transactions_locked_check() RETURNS TRIGGER AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id AND completed_at IS NOT NULL FOR SHARE) THEN
        RAISE EXCEPTION 'EVENT_COMPLETED' USING ERRCODE = 'P0001', HINT = 'event_completed';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER project_transactions_locked
BEFORE INSERT OR UPDATE OF project_id, parent_project_transaction_id, transaction_member_id, type, amount, date,
    description, note, marks ON project_transactions
FOR EACH ROW
EXECUTE FUNCTION project_transactions_locked_check();

-- 0.4.1-2 V15 (Q-E9): the owner removing a person (≠ the person leaving) keeps the member row; their
-- spot on each bill shows as an empty person "( ) ____" to assign. An event can't be completed while a
-- bill row still points at a removed member (default 3).
ALTER TABLE project_members ADD COLUMN removed_at TIMESTAMPTZ;
