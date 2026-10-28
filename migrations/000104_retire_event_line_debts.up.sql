-- 0.4.1-2 V8 (Q-E4: wipe): the 0.4.0-4 "{event_id}" debts — one debt per settle line (source event,
-- an event, no bill, no bill part) — are retired; claims (personal_debt_bills) and the settle booking
-- (event_settle_bookings) replace them. A line debt with no transaction of its own is deleted; one
-- with its own wallet transaction stays as a plain debt (no wallet moves).
DELETE FROM personal_debts d
WHERE d.kind = 'debt' AND d.source_kind = 'event' AND d.project_id IS NOT NULL AND d.source_project_transaction_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM personal_debt_bills b WHERE b.debt_id = d.id)
  AND NOT EXISTS (SELECT 1 FROM transactions t WHERE t.source_personal_debt_id = d.id);

UPDATE personal_debts d SET source_kind = NULL, project_id = NULL
WHERE d.kind = 'debt' AND d.source_kind = 'event' AND d.project_id IS NOT NULL AND d.source_project_transaction_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM personal_debt_bills b WHERE b.debt_id = d.id);
