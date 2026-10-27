-- 0.4.1-1 E8 close-out: a payback row made for a bill member marked "คืนแล้ว" remembers that bill,
-- so un-ticking it on a bill edit removes the row (and my repayment of it), and the board shows the
-- tick (BoardSplit.paid_back). Paybacks entered on their own stay NULL. Deleting the bill leaves its
-- paybacks as plain ones.
ALTER TABLE project_transactions
    ADD COLUMN paid_back_for_id UUID REFERENCES project_transactions(id) ON DELETE SET NULL;
CREATE INDEX idx_project_transactions_paid_back_for ON project_transactions (paid_back_for_id)
    WHERE paid_back_for_id IS NOT NULL;
