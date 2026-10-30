-- 0.4.2-3 split_transactions, phase A, step 2 of 2 = CONTRACT (owner 2026-10-31).
-- Plan: chubi-pocket-docs/now/briefs/split-transactions-plan.md.
--
-- From here split_transaction_people is the only record of a bill's people: the code writes it
-- directly, so the sync from the old tables goes, every pointer at a split row moves to its bill,
-- the split rows go, and so do the payers / opt-out tables and the marks array (all now in the
-- people rows). The link triggers (books -> bill + person) and the member -> person trigger stay.
-- One transaction; the asserts at the end abort it if the people or the links moved.

BEGIN;

CREATE TEMP TABLE people_before AS
    SELECT id, split_transaction_id, event_member_id, user_id, share, paid, paid_back,
           marked_at IS NOT NULL AS marked, disconnected_at IS NOT NULL AS disconnected
      FROM split_transaction_people;
CREATE TEMP TABLE links_before AS
    SELECT 't' AS k, id, split_transaction_id, split_person_id FROM transactions WHERE split_transaction_id IS NOT NULL
    UNION ALL SELECT 'd', id, split_transaction_id, NULL FROM personal_debts WHERE split_transaction_id IS NOT NULL
    UNION ALL SELECT 'l', id, split_transaction_id, split_person_id FROM personal_debt_bills;

-- ── 1. the people no longer follow the old tables ──────────────────────────
DROP TRIGGER sync_split_people_pt ON project_transactions;
DROP TRIGGER sync_split_people_payers ON project_transaction_payers;
DROP TRIGGER sync_split_people_opt_outs ON shared_bill_opt_outs;
DROP FUNCTION trg_sync_split_people_pt();
DROP FUNCTION trg_sync_split_people_bill_child();
DROP FUNCTION sync_split_people(UUID);

-- ── 2. a pointer at a split row points at its bill ─────────────────────────
CREATE TEMP TABLE child_of AS
    SELECT id AS child_id, parent_project_transaction_id AS bill_id
      FROM project_transactions WHERE parent_project_transaction_id IS NOT NULL;
CREATE UNIQUE INDEX ON child_of (child_id);

UPDATE transactions t SET source_project_transaction_id = c.bill_id
  FROM child_of c WHERE c.child_id = t.source_project_transaction_id;
UPDATE transactions t SET unlinked_project_transaction_id = c.bill_id
  FROM child_of c WHERE c.child_id = t.unlinked_project_transaction_id;
UPDATE personal_debts d SET source_project_transaction_id = c.bill_id
  FROM child_of c WHERE c.child_id = d.source_project_transaction_id;
UPDATE personal_debts d SET unlinked_project_transaction_id = c.bill_id
  FROM child_of c WHERE c.child_id = d.unlinked_project_transaction_id;
UPDATE personal_debt_bills l SET pt_id = c.bill_id
  FROM child_of c WHERE c.child_id = l.pt_id;
UPDATE project_transactions pb SET paid_back_for_id = c.bill_id
  FROM child_of c WHERE c.child_id = pb.paid_back_for_id;

-- ── 3. the split rows and the tables the people replaced ───────────────────
DELETE FROM project_transactions WHERE parent_project_transaction_id IS NOT NULL;
DROP VIEW project_bill_pairs;
DROP TABLE project_transaction_payers;
DROP TABLE shared_bill_opt_outs;
-- the completed-event lock watched marks too; it watches the same columns without it
DROP TRIGGER project_transactions_locked ON project_transactions;
ALTER TABLE project_transactions DROP COLUMN marks;
CREATE TRIGGER project_transactions_locked
    BEFORE INSERT OR UPDATE OF project_id, parent_project_transaction_id, transaction_member_id, type, amount,
        date, description, note
    ON project_transactions FOR EACH ROW EXECUTE FUNCTION project_transactions_locked_check();

-- A completed event's bills keep their people as they are (the split rows were locked the same way);
-- an opt-out and the paid-back tick may still change.
CREATE FUNCTION split_people_locked() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM project_transactions b JOIN projects p ON p.id = b.project_id
                WHERE b.id = COALESCE(NEW.split_transaction_id, OLD.split_transaction_id)
                  AND p.completed_at IS NOT NULL FOR SHARE OF p) THEN
        RAISE EXCEPTION 'EVENT_COMPLETED' USING ERRCODE = 'P0001', HINT = 'event_completed';
    END IF;
    RETURN COALESCE(NEW, OLD);
END $$;
CREATE TRIGGER split_people_locked
    BEFORE INSERT OR DELETE OR UPDATE OF share, paid, marked_at, event_member_id
    ON split_transaction_people FOR EACH ROW EXECUTE FUNCTION split_people_locked();

-- ── 4. proof, inside the transaction ───────────────────────────────────────
DO $$
DECLARE bad BIGINT;
BEGIN
    SELECT COUNT(*) INTO bad FROM (
        (SELECT id, split_transaction_id, event_member_id, user_id, share, paid, paid_back,
                marked_at IS NOT NULL, disconnected_at IS NOT NULL FROM split_transaction_people
         EXCEPT SELECT * FROM people_before)
        UNION ALL
        (SELECT * FROM people_before
         EXCEPT SELECT id, split_transaction_id, event_member_id, user_id, share, paid, paid_back,
                marked_at IS NOT NULL, disconnected_at IS NOT NULL FROM split_transaction_people)) x;
    IF bad > 0 THEN
        RAISE EXCEPTION 'split contract: % people row(s) changed', bad;
    END IF;

    SELECT COUNT(*) INTO bad FROM (
        (SELECT 't', id, split_transaction_id, split_person_id FROM transactions WHERE split_transaction_id IS NOT NULL
         UNION ALL SELECT 'd', id, split_transaction_id, NULL FROM personal_debts WHERE split_transaction_id IS NOT NULL
         UNION ALL SELECT 'l', id, split_transaction_id, split_person_id FROM personal_debt_bills
         EXCEPT ALL SELECT * FROM links_before)
        UNION ALL
        (SELECT * FROM links_before
         EXCEPT ALL (SELECT 't', id, split_transaction_id, split_person_id FROM transactions WHERE split_transaction_id IS NOT NULL
         UNION ALL SELECT 'd', id, split_transaction_id, NULL FROM personal_debts WHERE split_transaction_id IS NOT NULL
         UNION ALL SELECT 'l', id, split_transaction_id, split_person_id FROM personal_debt_bills))) x;
    IF bad > 0 THEN
        RAISE EXCEPTION 'split contract: % book link(s) changed', bad;
    END IF;

    RAISE NOTICE 'split contract: % bills, % people, % links',
        (SELECT COUNT(*) FROM project_transactions), (SELECT COUNT(*) FROM split_transaction_people),
        (SELECT COUNT(*) FROM links_before);
END $$;

DROP TABLE child_of;
DROP TABLE people_before;
DROP TABLE links_before;

COMMIT;
