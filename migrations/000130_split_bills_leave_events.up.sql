-- 0.4.2-3 split_transactions, phase A2 (owner 2026-10-31): a split bill is not an event.
-- Every hidden one-bill event (projects.kind = 'split') goes: its bill and the paybacks made for it get
-- project_id NULL, its members become the BILL's members (project_members.split_transaction_id = the
-- bill, project_id NULL: the people of a split bill keep one id per person, as on an event bill), and
-- the books that pointed at the hidden event (transactions / personal_debts project_id) point at no
-- event. Phase B renames project_members to fit. One transaction; the asserts abort it.

BEGIN;

ALTER TABLE project_members
    ALTER COLUMN project_id DROP NOT NULL,
    ADD COLUMN split_transaction_id UUID REFERENCES project_transactions(id) ON DELETE CASCADE
        DEFERRABLE INITIALLY DEFERRED,
    ADD CONSTRAINT project_members_one_scope CHECK ((project_id IS NULL) <> (split_transaction_id IS NULL));
CREATE INDEX idx_project_members_split_tx ON project_members (split_transaction_id) WHERE split_transaction_id IS NOT NULL;
CREATE UNIQUE INDEX idx_project_members_split_tx_user ON project_members (split_transaction_id, user_id)
    WHERE split_transaction_id IS NOT NULL AND user_id IS NOT NULL;

-- scope_id = whose a member / a bill is: its event, else (a split bill) the bill itself — a payback made
-- for a split bill belongs to that bill. The code reads by scope, so an event and a split bill are read
-- the same way; event_or_null() turns a scope back into an event id for the columns that point at one.
ALTER TABLE project_members
    ADD COLUMN scope_id UUID GENERATED ALWAYS AS (COALESCE(project_id, split_transaction_id)) STORED;
CREATE INDEX idx_project_members_scope ON project_members (scope_id, status);
ALTER TABLE project_transactions
    ADD COLUMN scope_id UUID GENERATED ALWAYS AS (COALESCE(project_id, paid_back_for_id, id)) STORED;
CREATE INDEX idx_project_transactions_scope ON project_transactions (scope_id);
CREATE FUNCTION event_or_null(p_scope UUID) RETURNS UUID LANGUAGE sql STABLE AS $$
    SELECT id FROM projects WHERE id = p_scope $$;

-- the pairs carry the scope (a split bill's pairs are its own)
CREATE OR REPLACE VIEW bill_pairs AS
SELECT b.id AS pt_id, b.scope_id AS project_id, b.type,
       CASE WHEN b.type = 'income' THEN pa.event_member_id ELSE s.event_member_id END AS debtor,
       CASE WHEN b.type = 'income' THEN s.event_member_id ELSE pa.event_member_id END AS creditor,
       CASE WHEN b.type = 'income' THEN pa.id ELSE s.id END AS debtor_person,
       CASE WHEN b.type = 'income' THEN s.id ELSE pa.id END AS creditor_person,
       ROUND(s.share * pa.paid / b.amount, 2) AS amount
FROM project_transactions b
JOIN split_transaction_people pa ON pa.split_transaction_id = b.id AND pa.paid > 0
JOIN split_transaction_people s  ON s.split_transaction_id = b.id AND s.share > 0 AND s.id <> pa.id
WHERE b.parent_project_transaction_id IS NULL
  AND ROUND(s.share * pa.paid / b.amount, 2) > 0;

-- Every scope as the readers know an event: the events, and each split bill as a "split" scope (named
-- by its description, owned by who recorded it) — what the hidden events used to answer.
CREATE VIEW scopes AS
    SELECT id, owner_user_id, name, description, start_date, end_date, status, created_at, created_by_user_id,
           updated_at, updated_by_user_id, icon_code, planned_amount, note, kind, completed_at
      FROM projects
    UNION ALL
    SELECT b.id, b.record_user_id, LEFT(COALESCE(b.description, ''), 100)::varchar, NULL, NULL, NULL,
           'active'::varchar, b.created_at, b.created_by_user_id, b.updated_at, b.updated_by_user_id,
           NULL::jsonb, NULL::numeric, NULL, 'split'::varchar, NULL::timestamptz
      FROM project_transactions b
     WHERE b.project_id IS NULL AND b.paid_back_for_id IS NULL AND b.parent_project_transaction_id IS NULL;

CREATE TEMP TABLE split_of AS
    SELECT p.id AS project_id,
           (SELECT b.id FROM project_transactions b WHERE b.project_id = p.id AND b.paid_back_for_id IS NULL) AS bill_id,
           (SELECT COUNT(*) FROM project_transactions b WHERE b.project_id = p.id AND b.paid_back_for_id IS NULL) AS bills
      FROM projects p WHERE p.kind = 'split';

DO $$
DECLARE bad BIGINT;
BEGIN
    SELECT COUNT(*) INTO bad FROM split_of WHERE bills > 1;
    IF bad > 0 THEN
        RAISE EXCEPTION 'split bills leave events: % hidden event(s) with more than one bill', bad;
    END IF;
END $$;

CREATE TEMP TABLE before_counts AS SELECT
    (SELECT COUNT(*) FROM project_transactions) AS bills,
    (SELECT COUNT(*) FROM split_transaction_people) AS people,
    (SELECT COUNT(*) FROM project_members) AS members,
    (SELECT COUNT(*) FROM personal_debt_bills) AS lumps,
    (SELECT COUNT(*) FROM transactions WHERE split_transaction_id IS NOT NULL) AS tx_links;

UPDATE project_members m SET split_transaction_id = s.bill_id, project_id = NULL
  FROM split_of s WHERE m.project_id = s.project_id AND s.bill_id IS NOT NULL;
UPDATE project_transactions b SET project_id = NULL
  FROM split_of s WHERE b.project_id = s.project_id;
UPDATE transactions t SET project_id = NULL
  FROM split_of s WHERE t.project_id = s.project_id;
UPDATE personal_debts d SET project_id = NULL
  FROM split_of s WHERE d.project_id = s.project_id;
-- an empty hidden event (no bill left) loses its members with it
DELETE FROM projects p USING split_of s WHERE p.id = s.project_id;

DO $$
DECLARE bad BIGINT; bc before_counts%ROWTYPE;
BEGIN
    SELECT * INTO bc FROM before_counts;
    IF EXISTS (SELECT 1 FROM projects WHERE kind <> 'event') THEN
        RAISE EXCEPTION 'split bills leave events: a hidden event is left';
    END IF;
    IF (SELECT COUNT(*) FROM project_transactions) <> bc.bills
       OR (SELECT COUNT(*) FROM split_transaction_people) <> bc.people
       OR (SELECT COUNT(*) FROM personal_debt_bills) <> bc.lumps
       OR (SELECT COUNT(*) FROM transactions WHERE split_transaction_id IS NOT NULL) <> bc.tx_links THEN
        RAISE EXCEPTION 'split bills leave events: bills / people / lump lines / book links changed';
    END IF;
    SELECT COUNT(*) INTO bad FROM split_transaction_people sp
      JOIN project_transactions b ON b.id = sp.split_transaction_id
     WHERE b.project_id IS NULL AND sp.event_member_id IS NULL;
    IF bad > 0 THEN
        RAISE EXCEPTION 'split bills leave events: % person(s) of a split bill lost their member', bad;
    END IF;
    RAISE NOTICE 'split bills leave events: % split bill(s), % member(s) moved',
        (SELECT COUNT(*) FROM project_transactions WHERE project_id IS NULL),
        (SELECT COUNT(*) FROM project_members WHERE split_transaction_id IS NOT NULL);
END $$;

ALTER TABLE projects ADD CONSTRAINT projects_kind_event CHECK (kind = 'event');

DROP TABLE split_of;
DROP TABLE before_counts;

COMMIT;
