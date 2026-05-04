-- Phase 2 redesign: project as a separate book.
-- See chubi-pocket-docs/design/plans/project-as-separate-book.md.
--
-- Bundles four schema changes:
--   1. project_transactions.parent_project_transaction_id (splits as multi-tx)
--   2. project_transactions.marks (per-row resolved markers, member ids)
--   3. drop project_transactions.category_id (user-scoped FK on shared row)
--   4. drop project_members.contact_id    (user-scoped FK on shared row)

-- 1 + 2: splits + marks on project_transactions.
ALTER TABLE project_transactions
    ADD COLUMN parent_project_transaction_id UUID
        REFERENCES project_transactions(id) ON DELETE CASCADE,
    ADD COLUMN marks UUID[] NOT NULL DEFAULT '{}';

CREATE INDEX project_transactions_parent_idx
    ON project_transactions(parent_project_transaction_id)
    WHERE parent_project_transaction_id IS NOT NULL;

-- 3: drop user-scoped FK from project_transactions.
-- Categories are user-scoped; storing one user's category id on a shared
-- project row is ambiguous. The personal-book entry created at resolve time
-- carries the category instead.
ALTER TABLE project_transactions
    DROP COLUMN category_id;

-- 4: drop user-scoped FK from project_members.
-- Contacts are user-scoped (each user has their own contact list). Existing
-- contact-kind member rows become ad-hoc — display_name is preserved; if FE
-- wants to re-associate a member with a contact later, it does so via the
-- contact's linked_user_id.
ALTER TABLE project_members
    DROP COLUMN contact_id;
