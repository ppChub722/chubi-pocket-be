-- 0.4.1-2 S15 (BE-3 for BE-1; Q-B7 default: wipe, pre-release). The one-way split mirror is
-- retired (S16): a split with a linked contact is a shared bill now. Its old data goes:
--   1. the splitter rows of a mirrored pair (source_kind 'transaction' or 'paid_by' with a
--      counterpart_debt_id): my split / paid-by debt with a linked contact. Found first, while the
--      pair link still exists (deleting the mirror sets counterpart_debt_id NULL);
--   2. the mirrors' own expenses (paid_for_transaction_id of a 'partner' row, wallet-less), which
--      cascades their mirror rows;
--   3. any 'partner' mirror left (accepted without an expense);
--   4. the split_created / split_changed notices whose one-tap actions are gone.
-- Kept: plain splits / paid-by debts with typed names or unlinked contacts (they still count through
-- source_transaction_id / paid_for_transaction_id) and pairs already disconnected (0.4.1-1 D9).
-- A paid_by row's own expense stays (its debt goes; the expense keeps paid_by_amount, so its wallet
-- part stays as it was).
DELETE FROM personal_debts
WHERE source_kind IN ('transaction', 'paid_by') AND counterpart_debt_id IS NOT NULL;

DELETE FROM transactions
WHERE id IN (SELECT paid_for_transaction_id FROM personal_debts
             WHERE source_kind = 'partner' AND paid_for_transaction_id IS NOT NULL);

DELETE FROM personal_debts WHERE source_kind = 'partner';

DELETE FROM notifications WHERE type IN ('split_created', 'split_changed');
