-- 0.4.2-4 split_transactions, phase A, step 1 of 2 = EXPAND (owner 2026-10-28 / 2026-10-30).
-- Plan: chubi-pocket-docs/now/briefs/split-transactions-plan.md.
--
-- One bill model for split bills AND events: project_transactions holds BILLS, the people of a
-- bill are rows of split_transaction_people (final name; the bill FK points at project_transactions
-- until phase B renames it). This migration only ADDS: the people table, the links from
-- transactions / personal_debts / personal_debt_bills, and triggers that keep the people rows equal
-- to the old child rows / payers / opt-outs / marks while the code moves over. It deletes nothing.
-- Migration 125 (CONTRACT) then removes the child rows, the hidden `projects.kind = 'split'`
-- projects and the old pointers, once nothing reads them. Both ship in the same release.
-- Prod data is REAL: the people rows are built from every existing bill, the asserts at the end
-- abort the whole migration (one transaction) if anything does not add up.

BEGIN;

-- ── 1. the people table ────────────────────────────────────────────────────
CREATE TABLE split_transaction_people (
    id                   UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    split_transaction_id UUID          NOT NULL REFERENCES project_transactions(id) ON DELETE CASCADE,
    -- a connected user (gets a copy); NULL = a name only
    user_id              UUID          REFERENCES users(id) ON DELETE SET NULL,
    -- the event's member this person is; NULL on a plain split bill (and after an event is deleted)
    event_member_id      UUID          REFERENCES project_members(id) ON DELETE SET NULL,
    name                 VARCHAR(100)  NOT NULL,
    share                NUMERIC(15,2) NOT NULL DEFAULT 0 CHECK (share >= 0),
    paid                 NUMERIC(15,2) NOT NULL DEFAULT 0 CHECK (paid >= 0),
    -- the "คืนแล้ว" tick on this person's part
    paid_back            BOOLEAN       NOT NULL DEFAULT FALSE,
    -- "เคลียร์แล้ว" on the event board (old project_transactions.marks)
    marked_at            TIMESTAMPTZ,
    -- this person disconnected their copy (old shared_bill_opt_outs)
    disconnected_at      TIMESTAMPTZ,
    sort                 INT           NOT NULL DEFAULT 0,
    created_at           TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX idx_split_people_bill_user ON split_transaction_people (split_transaction_id, user_id)
    WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX idx_split_people_bill_member ON split_transaction_people (split_transaction_id, event_member_id)
    WHERE event_member_id IS NOT NULL;
CREATE INDEX idx_split_people_user ON split_transaction_people (user_id) WHERE user_id IS NOT NULL;
CREATE INDEX idx_split_people_member ON split_transaction_people (event_member_id) WHERE event_member_id IS NOT NULL;
CREATE TRIGGER set_timestamp_split_transaction_people BEFORE UPDATE ON split_transaction_people
    FOR EACH ROW EXECUTE FUNCTION set_timestamp();

-- ── 2. links from the books to bill + person ───────────────────────────────
ALTER TABLE transactions
    ADD COLUMN split_transaction_id          UUID REFERENCES project_transactions(id) ON DELETE SET NULL,
    ADD COLUMN split_person_id               UUID REFERENCES split_transaction_people(id) ON DELETE SET NULL,
    ADD COLUMN unlinked_split_transaction_id UUID REFERENCES project_transactions(id) ON DELETE SET NULL;
CREATE INDEX idx_transactions_split_tx ON transactions (split_transaction_id) WHERE split_transaction_id IS NOT NULL;

ALTER TABLE personal_debts
    ADD COLUMN split_transaction_id          UUID REFERENCES project_transactions(id) ON DELETE SET NULL,
    ADD COLUMN unlinked_split_transaction_id UUID REFERENCES project_transactions(id) ON DELETE SET NULL;
CREATE INDEX idx_personal_debts_split_tx ON personal_debts (split_transaction_id) WHERE split_transaction_id IS NOT NULL;

ALTER TABLE personal_debt_bills
    ADD COLUMN split_transaction_id UUID REFERENCES project_transactions(id) ON DELETE CASCADE,
    -- SET NULL, not CASCADE: while the sync rewrites a bill's people (a member moved), the line must survive
    -- and find its new person when member_id follows
    ADD COLUMN split_person_id      UUID REFERENCES split_transaction_people(id) ON DELETE SET NULL;

-- A bill may have no event and no single main payer any more (the payers are in the people rows).
ALTER TABLE project_transactions
    ALTER COLUMN project_id DROP NOT NULL,
    ALTER COLUMN transaction_member_id DROP NOT NULL;

-- ── 3. the one function that derives a bill's people from the old tables ────
-- Involved = the main payer (parent row), every member of a child row, every row of the payers table.
-- share = the member's child rows; the main payer's share = amount - sum(children).
-- paid  = the payers table when the bill has one, else the whole amount on the main payer.
-- marked_at keeps its value once set; paid_back = a payback row with paid_back_for_id = this bill,
-- paid by that member; disconnected_at = the user's opt-out.
CREATE FUNCTION sync_split_people(p_bill UUID) RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE b project_transactions%ROWTYPE;
BEGIN
    SELECT * INTO b FROM project_transactions WHERE id = p_bill AND parent_project_transaction_id IS NULL;
    IF NOT FOUND THEN RETURN; END IF;

    -- a member no longer in the bill (first, so a member swapped for another of the same user never meets
    -- its old row in the unique index)
    DELETE FROM split_transaction_people p
     WHERE p.split_transaction_id = b.id AND p.event_member_id IS NOT NULL
       AND p.event_member_id NOT IN (
            SELECT b.transaction_member_id
            UNION SELECT transaction_member_id FROM project_transactions WHERE parent_project_transaction_id = b.id
            UNION SELECT member_id FROM project_transaction_payers WHERE project_transaction_id = b.id);

    INSERT INTO split_transaction_people
        (split_transaction_id, user_id, event_member_id, name, share, paid, paid_back, marked_at, disconnected_at, sort)
    SELECT b.id, pm.user_id, pm.id, LEFT(pm.display_name, 100),
           GREATEST(0, COALESCE(ch.amount, 0)
                     + CASE WHEN i.member_id = b.transaction_member_id THEN b.amount - COALESCE(chs.total, 0) ELSE 0 END),
           CASE WHEN EXISTS (SELECT 1 FROM project_transaction_payers x WHERE x.project_transaction_id = b.id)
                THEN COALESCE(py.amount, 0)
                WHEN i.member_id = b.transaction_member_id THEN b.amount
                ELSE 0 END,
           EXISTS (SELECT 1 FROM project_transactions pb
                    WHERE pb.paid_back_for_id = b.id AND pb.transaction_member_id = i.member_id),
           CASE WHEN i.member_id = ANY (b.marks) THEN b.updated_at END,
           oo.created_at,
           (ROW_NUMBER() OVER (ORDER BY (i.member_id = b.transaction_member_id) DESC, pm.created_at, pm.id) - 1)::INT
    FROM (
        SELECT b.transaction_member_id AS member_id
        UNION
        SELECT transaction_member_id FROM project_transactions WHERE parent_project_transaction_id = b.id
        UNION
        SELECT member_id FROM project_transaction_payers WHERE project_transaction_id = b.id
    ) i
    JOIN project_members pm ON pm.id = i.member_id
    LEFT JOIN (
        SELECT transaction_member_id AS member_id, SUM(amount) AS amount
          FROM project_transactions WHERE parent_project_transaction_id = b.id GROUP BY 1
    ) ch ON ch.member_id = i.member_id
    LEFT JOIN (
        SELECT SUM(amount) AS total FROM project_transactions WHERE parent_project_transaction_id = b.id
    ) chs ON TRUE
    LEFT JOIN project_transaction_payers py ON py.project_transaction_id = b.id AND py.member_id = i.member_id
    LEFT JOIN shared_bill_opt_outs oo ON oo.project_transaction_id = b.id AND oo.user_id = pm.user_id
    ON CONFLICT (split_transaction_id, event_member_id) WHERE event_member_id IS NOT NULL
    DO UPDATE SET user_id = EXCLUDED.user_id, name = EXCLUDED.name, share = EXCLUDED.share,
                  paid = EXCLUDED.paid, paid_back = EXCLUDED.paid_back,
                  marked_at = CASE WHEN EXCLUDED.marked_at IS NULL THEN NULL
                                   ELSE COALESCE(split_transaction_people.marked_at, EXCLUDED.marked_at) END,
                  disconnected_at = EXCLUDED.disconnected_at, sort = EXCLUDED.sort;

    -- a copy made before its person existed finds the person now
    UPDATE transactions t SET split_person_id = (SELECT p.id FROM split_transaction_people p
                                                  WHERE p.split_transaction_id = b.id AND p.user_id = t.user_id)
     WHERE t.split_transaction_id = b.id AND t.split_person_id IS NULL;
END $$;

-- While the old tables are still written, the people follow them (dropped by migration 125).
CREATE FUNCTION trg_sync_split_people_pt() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        PERFORM sync_split_people(COALESCE(OLD.parent_project_transaction_id, OLD.id));
        IF OLD.paid_back_for_id IS NOT NULL THEN PERFORM sync_split_people(OLD.paid_back_for_id); END IF;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        PERFORM sync_split_people(COALESCE(NEW.parent_project_transaction_id, NEW.id));
        IF NEW.paid_back_for_id IS NOT NULL THEN PERFORM sync_split_people(NEW.paid_back_for_id); END IF;
    END IF;
    RETURN NULL;
END $$;
CREATE TRIGGER sync_split_people_pt
    AFTER INSERT OR DELETE OR UPDATE OF amount, transaction_member_id, marks, paid_back_for_id, parent_project_transaction_id
    ON project_transactions FOR EACH ROW EXECUTE FUNCTION trg_sync_split_people_pt();

CREATE FUNCTION trg_sync_split_people_bill_child() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN PERFORM sync_split_people(OLD.project_transaction_id); END IF;
    IF TG_OP <> 'DELETE' THEN PERFORM sync_split_people(NEW.project_transaction_id); END IF;
    RETURN NULL;
END $$;
CREATE TRIGGER sync_split_people_payers
    AFTER INSERT OR UPDATE OR DELETE ON project_transaction_payers
    FOR EACH ROW EXECUTE FUNCTION trg_sync_split_people_bill_child();
CREATE TRIGGER sync_split_people_opt_outs
    AFTER INSERT OR UPDATE OR DELETE ON shared_bill_opt_outs
    FOR EACH ROW EXECUTE FUNCTION trg_sync_split_people_bill_child();

-- A member who gets an account (or is renamed) is the same person on every bill.
CREATE FUNCTION trg_sync_split_people_member() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    UPDATE split_transaction_people SET user_id = NEW.user_id, name = LEFT(NEW.display_name, 100)
     WHERE event_member_id = NEW.id;
    RETURN NULL;
END $$;
CREATE TRIGGER sync_split_people_member
    AFTER UPDATE OF user_id, display_name ON project_members
    FOR EACH ROW EXECUTE FUNCTION trg_sync_split_people_member();

-- The books' links follow the old pointers (source / unlinked project-transaction id, a lump line's
-- pt_id + member_id) so a reader can already use split_transaction_id / split_person_id.
CREATE FUNCTION split_bill_of(p_pt UUID) RETURNS UUID LANGUAGE sql STABLE AS $$
    SELECT COALESCE(parent_project_transaction_id, id) FROM project_transactions WHERE id = p_pt $$;

CREATE FUNCTION trg_links_transactions() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    -- the bill a copy belongs to, else the bill this transaction was made from
    NEW.split_transaction_id := COALESCE(split_bill_of(NEW.source_project_transaction_id),
        (SELECT id FROM project_transactions WHERE linked_transaction_id = NEW.id));
    NEW.split_person_id := (SELECT p.id FROM split_transaction_people p
                             WHERE p.split_transaction_id = NEW.split_transaction_id AND p.user_id = NEW.user_id);
    NEW.unlinked_split_transaction_id := split_bill_of(NEW.unlinked_project_transaction_id);
    RETURN NEW;
END $$;
CREATE TRIGGER links_transactions
    BEFORE INSERT OR UPDATE OF source_project_transaction_id, unlinked_project_transaction_id, user_id
    ON transactions FOR EACH ROW EXECUTE FUNCTION trg_links_transactions();

-- making a bill from a transaction (or unlinking it) sets / clears its origin link
CREATE FUNCTION trg_links_origin() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND OLD.linked_transaction_id IS NOT NULL THEN
        UPDATE transactions t SET split_transaction_id = split_bill_of(t.source_project_transaction_id)
         WHERE t.id = OLD.linked_transaction_id AND t.split_transaction_id = OLD.id AND t.source_project_transaction_id IS NULL;
    END IF;
    IF NEW.linked_transaction_id IS NOT NULL THEN
        UPDATE transactions t SET split_transaction_id = NEW.id,
               split_person_id = (SELECT p.id FROM split_transaction_people p
                                   WHERE p.split_transaction_id = NEW.id AND p.user_id = t.user_id)
         WHERE t.id = NEW.linked_transaction_id AND t.split_transaction_id IS DISTINCT FROM NEW.id;
    END IF;
    RETURN NULL;
END $$;
CREATE TRIGGER links_origin
    AFTER INSERT OR UPDATE OF linked_transaction_id ON project_transactions
    FOR EACH ROW
    EXECUTE FUNCTION trg_links_origin();

CREATE FUNCTION trg_links_debts() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.split_transaction_id := split_bill_of(NEW.source_project_transaction_id);
    NEW.unlinked_split_transaction_id := split_bill_of(NEW.unlinked_project_transaction_id);
    RETURN NEW;
END $$;
CREATE TRIGGER links_debts
    BEFORE INSERT OR UPDATE OF source_project_transaction_id, unlinked_project_transaction_id
    ON personal_debts FOR EACH ROW EXECUTE FUNCTION trg_links_debts();

CREATE FUNCTION trg_links_debt_bills() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.split_transaction_id := split_bill_of(NEW.pt_id);
    NEW.split_person_id := (SELECT p.id FROM split_transaction_people p
                             WHERE p.split_transaction_id = NEW.split_transaction_id AND p.event_member_id = NEW.member_id);
    RETURN NEW;
END $$;
CREATE TRIGGER links_debt_bills
    BEFORE INSERT OR UPDATE OF pt_id, member_id
    ON personal_debt_bills FOR EACH ROW EXECUTE FUNCTION trg_links_debt_bills();

-- ── 4. build the people of every existing bill ─────────────────────────────
SELECT sync_split_people(id) FROM project_transactions WHERE parent_project_transaction_id IS NULL;

-- ── 5. re-point the links (a child row is its bill) ────────────────────────
CREATE TEMP TABLE bill_of AS
    SELECT id AS pt_id, COALESCE(parent_project_transaction_id, id) AS bill_id FROM project_transactions;
CREATE UNIQUE INDEX ON bill_of (pt_id);

UPDATE transactions t SET
    split_transaction_id = bo.bill_id,
    split_person_id = (SELECT p.id FROM split_transaction_people p
                        WHERE p.split_transaction_id = bo.bill_id AND p.user_id = t.user_id)
FROM bill_of bo WHERE bo.pt_id = t.source_project_transaction_id;

-- the transaction a bill was made from (its origin)
UPDATE transactions t SET
    split_transaction_id = b.id,
    split_person_id = (SELECT p.id FROM split_transaction_people p
                        WHERE p.split_transaction_id = b.id AND p.user_id = t.user_id)
FROM project_transactions b
WHERE b.linked_transaction_id = t.id AND t.split_transaction_id IS NULL;

UPDATE transactions t SET unlinked_split_transaction_id = bo.bill_id
FROM bill_of bo WHERE bo.pt_id = t.unlinked_project_transaction_id;

UPDATE personal_debts d SET split_transaction_id = bo.bill_id
FROM bill_of bo WHERE bo.pt_id = d.source_project_transaction_id;
UPDATE personal_debts d SET unlinked_split_transaction_id = bo.bill_id
FROM bill_of bo WHERE bo.pt_id = d.unlinked_project_transaction_id;

UPDATE personal_debt_bills b SET
    split_transaction_id = bo.bill_id,
    split_person_id = (SELECT p.id FROM split_transaction_people p
                        WHERE p.split_transaction_id = bo.bill_id AND p.event_member_id = b.member_id)
FROM bill_of bo WHERE bo.pt_id = b.pt_id;

DROP TABLE bill_of;

-- ── 6. proof, inside the transaction: anything off aborts the whole migration ──
DO $$
DECLARE bad BIGINT;
BEGIN
    SELECT COUNT(*) INTO bad FROM project_transactions b
     WHERE b.parent_project_transaction_id IS NULL
       AND ((SELECT COALESCE(SUM(share), 0) FROM split_transaction_people WHERE split_transaction_id = b.id) <> b.amount
         OR (SELECT COALESCE(SUM(paid), 0)  FROM split_transaction_people WHERE split_transaction_id = b.id) <> b.amount);
    IF bad > 0 THEN
        RAISE EXCEPTION 'split phase A: % bill(s) whose people shares or paid do not add up to the amount', bad;
    END IF;

    SELECT COUNT(*) INTO bad FROM project_transactions b
     WHERE b.parent_project_transaction_id IS NULL
       AND NOT EXISTS (SELECT 1 FROM split_transaction_people WHERE split_transaction_id = b.id);
    IF bad > 0 THEN
        RAISE EXCEPTION 'split phase A: % bill(s) with no people', bad;
    END IF;

    SELECT COUNT(*) INTO bad FROM transactions
     WHERE source_project_transaction_id IS NOT NULL AND split_transaction_id IS NULL;
    IF bad > 0 THEN
        RAISE EXCEPTION 'split phase A: % transaction link(s) not carried over', bad;
    END IF;

    SELECT COUNT(*) INTO bad FROM personal_debts
     WHERE source_project_transaction_id IS NOT NULL AND split_transaction_id IS NULL;
    IF bad > 0 THEN
        RAISE EXCEPTION 'split phase A: % debt link(s) not carried over', bad;
    END IF;

    SELECT COUNT(*) INTO bad FROM personal_debt_bills
     WHERE split_transaction_id IS NULL OR split_person_id IS NULL;
    IF bad > 0 THEN
        RAISE EXCEPTION 'split phase A: % lump line(s) not carried over', bad;
    END IF;

    RAISE NOTICE 'split phase A (expand): % bills, % people',
        (SELECT COUNT(*) FROM project_transactions WHERE parent_project_transaction_id IS NULL),
        (SELECT COUNT(*) FROM split_transaction_people);
END $$;

COMMIT;
