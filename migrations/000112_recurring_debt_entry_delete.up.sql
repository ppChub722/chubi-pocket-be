-- 0.4.1-2 K6 (BE-3): deleting a person-book entry that fills a "หนี้" recurring occurrence SKIPS
-- that occurrence with its last values (amount, date) instead of making it due again (the FK's SET
-- NULL alone would repeat the 0.4.1-1 R3 bug). A trigger catches every delete path (the debts API,
-- split / bill follow-ups, a deleted transaction's cascade). Un-skip = due with those values.
CREATE OR REPLACE FUNCTION recurring_skip_on_debt_delete() RETURNS trigger AS $$
BEGIN
    UPDATE recurring_items
    SET archived = TRUE, edited = TRUE, debt_entry_id = NULL, produced_at = NULL, auto_created_at = NULL,
        cleared_at = NULL,
        draft = jsonb_set(draft, '{amount}', to_jsonb(OLD.amount))
    WHERE debt_entry_id = OLD.id;
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER personal_debts_recurring_skip
    BEFORE DELETE ON personal_debts
    FOR EACH ROW EXECUTE FUNCTION recurring_skip_on_debt_delete();
