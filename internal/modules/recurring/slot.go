package recurring

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Operations on ONE occurrence of a setting, addressed by rule_id + slot (its
// original date). A computed occurrence gets a stored row the first time it is
// touched.

type ConfirmRequest struct {
	Fields
}

type SkipRequest struct {
	DeleteTx bool `json:"delete_tx"`
}

type LinkSlotRequest struct {
	TransactionID uuid.UUID `json:"transaction_id"`
	// Replace — swap: the occurrence's current transaction (or the other occurrence the
	// transaction is linked to) lets go first.
	Replace bool `json:"replace"`
}

type LeftOut struct {
	Slot   string `json:"slot"`
	Reason string `json:"reason"` // needs_amount
}

type BulkResult struct {
	Items           []Item                 `json:"items"`
	LeftOut         []LeftOut              `json:"left_out"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
	Setting         *Recurring             `json:"setting"`
}

// occCtx loads the rule and the occurrence at slot for a write.
func (s *Service) occCtx(ctx context.Context, q shared.Querier, userID, ruleID uuid.UUID, slot time.Time, tz string) (time.Time, *Rule, *occ, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return time.Time{}, nil, nil, err
	}
	r, err := getRule(ctx, s.db, userID, ruleID, false)
	if err != nil {
		return time.Time{}, nil, nil, err
	}
	o, err := s.loadOcc(ctx, q, userID, r, slot, today)
	return today, r, o, err
}

// occOf — the rule and the occurrence for a write: the given stored row (a
// manual item, reached by its id) or the one at slot.
func (s *Service) occOf(ctx context.Context, q shared.Querier, userID, ruleID uuid.UUID, slot time.Time, tz string, byRow *itemRow) (*Rule, *occ, error) {
	if byRow == nil {
		_, r, o, err := s.occCtx(ctx, q, userID, ruleID, slot, tz)
		return r, o, err
	}
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, nil, err
	}
	r, err := getRule(ctx, s.db, userID, ruleID, false)
	if err != nil {
		return nil, nil, err
	}
	return r, rowOcc(r, byRow, seqIndex(r), today, s.lang(ctx, userID)), nil
}

// itemAfter — the item of the occurrence once a write is done.
func (s *Service) itemAfter(ctx context.Context, userID, ruleID uuid.UUID, slot time.Time, byRow *itemRow) (*Item, error) {
	if byRow != nil {
		return s.itemView(ctx, userID, byRow.ID)
	}
	return s.slotItem(ctx, userID, ruleID, slot)
}

// slotResult is the answer of a write on an occurrence: its item now (stored,
// or computed again when its row went), the setting, the wallets touched.
func (s *Service) slotResult(ctx context.Context, userID, ruleID uuid.UUID, slot time.Time, txRow any, touched []uuid.UUID) (*ItemWriteResult, error) {
	it, err := s.slotItem(ctx, userID, ruleID, slot)
	if err != nil {
		return nil, err
	}
	return s.resultOf(ctx, userID, it, txRow, touched)
}

func truncRunes(s *string, n int) *string {
	if s == nil {
		return nil
	}
	r := []rune(*s)
	if len(r) <= n {
		return s
	}
	t := string(r[:n])
	return &t
}

// recordOccurrenceTx makes the transaction of an occurrence (row = its stored
// row) dated its own date (or `date`), and links it. Confirm, the bulk confirm
// and auto-create all come through here.
func (s *Service) recordOccurrenceTx(ctx context.Context, tx pgx.Tx, r *Rule, row *itemRow, date *time.Time, auto bool, lang string) ([]uuid.UUID, any, error) {
	if row.Archived {
		return nil, nil, ErrInvalidTransition
	}
	if row.TransactionID != nil || row.SavingEntryID != nil {
		return nil, nil, ErrAlreadyRecorded
	}
	if row.PendingID != nil { // a legacy draft of this occurrence: the transaction replaces it
		if err := s.pending.Store().DeleteTx(ctx, tx, r.UserID, *row.PendingID); err != nil {
			return nil, nil, err
		}
		row.PendingID = nil
	}
	d := row.Draft
	if d.Type == nil || *d.Type == typeSaving {
		return nil, nil, ErrValidation
	}
	if d.Amount == nil || *d.Amount <= 0 {
		return nil, nil, ErrAmountRequired
	}
	slot := row.Date
	if row.Slot != nil {
		slot = *row.Slot
	}
	when := row.Date
	if date != nil {
		when = *date
	}
	desc := d.Description
	if desc == nil || *desc == "" {
		desc = ptr(occName(r, slot, seqPtr(seqIndex(r), slot), lang))
	}
	req := transactions.CreateRequest{
		Type: transactions.TxType(*d.Type), AccountID: d.AccountID, Amount: *d.Amount, CategoryID: d.CategoryID,
		Date: when.Format(dateLayout), Description: truncRunes(desc, 200), Note: d.Note,
		TransferToAccountID: d.TransferToAccountID, TagIDs: d.TagIDs,
	}
	res, err := s.txs.CreateInTx(ctx, tx, r.UserID, req)
	if err != nil {
		return nil, nil, err
	}
	var (
		id      uuid.UUID
		touched []uuid.UUID
	)
	switch v := res.(type) {
	case *transactions.TransactionDetail:
		id = v.ID
		if v.AccountID != nil {
			touched = append(touched, *v.AccountID)
		}
	case *transactions.TransferResponse:
		if len(v.Rows) == 0 {
			return nil, nil, ErrValidation
		}
		id = v.Rows[0].ID
		for _, rw := range v.Rows {
			if rw.AccountID != nil {
				touched = append(touched, *rw.AccountID)
			}
		}
	default:
		return nil, nil, ErrValidation
	}
	now := time.Now()
	row.TransactionID, row.ProducedAt = &id, &now
	if auto {
		row.AutoCreatedAt = &now
	}
	if err := saveItemTx(ctx, tx, row); err != nil {
		return nil, nil, err
	}
	return touched, res, nil
}

// autoCreate records, for a setting with auto-create on, the occurrences that
// came due since it last looked (auto_created_until, today]: not the ones that
// were due before it was switched on (they stay due), not skipped / paid /
// recorded ones; an edited one uses its edited values. A failing one stays due.
func (s *Service) autoCreate(ctx context.Context, tx pgx.Tx, r *Rule, today time.Time, lang string) (int, error) {
	if !r.AutoCreate || (r.AutoCreatedUntil != nil && !r.AutoCreatedUntil.Before(today)) {
		return 0, nil // off, or already handled today
	}
	from := r.TrackedFrom
	if r.AutoCreatedUntil != nil && r.AutoCreatedUntil.AddDate(0, 0, 1).After(from) {
		from = r.AutoCreatedUntil.AddDate(0, 0, 1)
	}
	made := 0
	if !from.After(today) {
		rows, err := s.storedRows(ctx, tx, r.UserID, []uuid.UUID{r.ID}, &from, &today)
		if err != nil {
			return 0, err
		}
		for _, o := range occsOf(r, rows, from, today, today, lang) {
			if o.status != StatusDue || o.draft.Amount == nil || *o.draft.Amount <= 0 {
				continue
			}
			sp, err := tx.Begin(ctx)
			if err != nil {
				return 0, err
			}
			row, err := s.store(ctx, sp, o)
			if err == nil {
				_, _, err = s.recordOccurrenceTx(ctx, sp, r, row, nil, true, lang)
			}
			if err != nil {
				sp.Rollback(ctx)
				continue // it stays due: the user finishes it by hand
			}
			if err := sp.Commit(ctx); err != nil {
				return 0, err
			}
			made++
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE recurring_rules SET auto_created_until = $2 WHERE id = $1
		AND auto_created_until IS DISTINCT FROM $2`, r.ID, today); err != nil {
		return 0, err
	}
	r.AutoCreatedUntil = &today
	return made, nil
}

// EditOcc edits one occurrence (a partial body: a key present sets it, null clears it).
func (s *Service) EditOcc(ctx context.Context, userID, ruleID uuid.UUID, slot time.Time, req ItemUpdateRequest, tz string) (*ItemWriteResult, error) {
	if err := validateFields(&req.Fields); err != nil {
		return nil, err
	}
	_, _, o, err := s.occCtx(ctx, s.db, userID, ruleID, slot, tz)
	if err != nil {
		return nil, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	row, err := s.store(ctx, tx, o)
	if err != nil {
		return nil, err
	}
	touched, err := s.applyItemEdit(ctx, tx, row, &req.Fields, req.AlsoUpdateLink)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.slotResult(ctx, userID, ruleID, slot, nil, touched)
}

// Confirm records the occurrence as a transaction now (dated the occurrence
// date unless `date` is sent; an amount may be sent or overridden).
func (s *Service) Confirm(ctx context.Context, userID, ruleID uuid.UUID, slot time.Time, req ConfirmRequest, tz string) (*ItemWriteResult, error) {
	if err := validateFields(&req.Fields); err != nil {
		return nil, err
	}
	_, r, o, err := s.occCtx(ctx, s.db, userID, ruleID, slot, tz)
	if err != nil {
		return nil, err
	}
	if o.status == StatusDone {
		return nil, ErrAlreadyRecorded
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	row, err := s.store(ctx, tx, o)
	if err != nil {
		return nil, err
	}
	applyToDraft(&row.Draft, &req.Fields)
	if req.values() {
		row.Edited = true
	}
	if err := checkDraftRefsTx(ctx, tx, userID, row.Draft); err != nil {
		return nil, err
	}
	var date *time.Time
	if req.has("date") {
		d, _ := parseDate(*req.Date)
		date = &d
	}
	touched, txRow, err := s.recordOccurrenceTx(ctx, tx, r, row, date, false, s.lang(ctx, userID))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.slotResult(ctx, userID, ruleID, slot, txRow, touched)
}

// SkipOcc skips the occurrence. A transaction recorded from it stays, unless deleteTx.
func (s *Service) SkipOcc(ctx context.Context, userID, ruleID uuid.UUID, slot time.Time, deleteTx bool, tz string) (*ItemWriteResult, error) {
	_, _, o, err := s.occCtx(ctx, s.db, userID, ruleID, slot, tz)
	if err != nil {
		return nil, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	row, err := s.store(ctx, tx, o)
	if err != nil {
		return nil, err
	}
	var touched []uuid.UUID
	if row.linked() {
		if deleteTx {
			if _, touched, err = s.unlinkTx(ctx, tx, row); err != nil {
				return nil, err
			}
		} else {
			if row.PendingID != nil { // a legacy draft must not outlive the skip
				if err := s.pending.Store().DeleteTx(ctx, tx, userID, *row.PendingID); err != nil {
					return nil, err
				}
			}
			row.TransactionID, row.PendingID, row.SavingEntryID = nil, nil, nil
		}
	}
	row.AutoCreatedAt, row.Archived, row.ClearedAt = nil, true, nil
	if err := saveItemTx(ctx, tx, row); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.slotResult(ctx, userID, ruleID, slot, nil, touched)
}

func blank(it *itemRow) bool {
	return it.ClearedAt == nil && !it.Edited && !it.Manual && !it.Archived && !it.linked()
}

// RestoreOcc undoes a skip.
func (s *Service) RestoreOcc(ctx context.Context, userID, ruleID uuid.UUID, slot time.Time, tz string) (*ItemWriteResult, error) {
	return s.flip(ctx, userID, ruleID, slot, tz, func(row *itemRow) error {
		if !row.Archived {
			return ErrInvalidTransition
		}
		row.Archived = false
		return nil
	})
}

// MarkPaid clears the occurrence as paid WITHOUT a transaction ("แม่จ่ายให้").
func (s *Service) MarkPaid(ctx context.Context, userID, ruleID uuid.UUID, slot time.Time, tz string) (*ItemWriteResult, error) {
	return s.flip(ctx, userID, ruleID, slot, tz, func(row *itemRow) error {
		if row.TransactionID != nil || row.SavingEntryID != nil {
			return ErrAlreadyRecorded
		}
		now := time.Now()
		row.Archived, row.ClearedAt = false, &now
		return nil
	})
}

// UnmarkPaid takes the paid mark off.
func (s *Service) UnmarkPaid(ctx context.Context, userID, ruleID uuid.UUID, slot time.Time, tz string) (*ItemWriteResult, error) {
	return s.flip(ctx, userID, ruleID, slot, tz, func(row *itemRow) error {
		if row.ClearedAt == nil {
			return ErrInvalidTransition
		}
		row.ClearedAt = nil
		return nil
	})
}

// flip stores the occurrence, applies fn to its row and drops the row again when
// nothing is left on it (the occurrence is computed once more).
func (s *Service) flip(ctx context.Context, userID, ruleID uuid.UUID, slot time.Time, tz string, fn func(*itemRow) error) (*ItemWriteResult, error) {
	_, _, o, err := s.occCtx(ctx, s.db, userID, ruleID, slot, tz)
	if err != nil {
		return nil, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	row, err := s.store(ctx, tx, o)
	if err != nil {
		return nil, err
	}
	if err := fn(row); err != nil {
		return nil, err
	}
	if blank(row) {
		if _, err := tx.Exec(ctx, `DELETE FROM recurring_items WHERE id = $1`, row.ID); err != nil {
			return nil, err
		}
	} else if err := saveItemTx(ctx, tx, row); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.slotResult(ctx, userID, ruleID, slot, nil, nil)
}

// dueOccs — the setting's due occurrences (computed or stored, not recorded).
func (s *Service) dueOccs(ctx context.Context, q shared.Querier, r *Rule, today time.Time) ([]*occ, error) {
	if r.TrackedFrom.After(today) {
		return nil, nil
	}
	rows, err := s.storedRows(ctx, q, r.UserID, []uuid.UUID{r.ID}, &r.TrackedFrom, &today)
	if err != nil {
		return nil, err
	}
	var out []*occ
	for _, o := range occsOf(r, rows, r.TrackedFrom, today, today, s.lang(ctx, r.UserID)) {
		if o.status == StatusDue {
			out = append(out, o)
		}
	}
	return out, nil
}

// bulk runs fn on every due occurrence of the setting in one DB transaction.
func (s *Service) bulk(ctx context.Context, userID, ruleID uuid.UUID, tz string, fn func(tx pgx.Tx, r *Rule, o *occ, out *BulkResult, touched *[]uuid.UUID) error) (*BulkResult, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	r, err := getRule(ctx, s.db, userID, ruleID, false)
	if err != nil {
		return nil, err
	}
	due, err := s.dueOccs(ctx, s.db, r, today)
	if err != nil {
		return nil, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	out := &BulkResult{Items: []Item{}, LeftOut: []LeftOut{}}
	var touched []uuid.UUID
	for _, o := range due {
		if err := fn(tx, r, o, out, &touched); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if out.AccountBalances, err = shared.LoadWalletBalances(ctx, s.db, uniqueIDs(touched)); err != nil {
		return nil, err
	}
	out.Setting = s.settingOf(ctx, userID, &ruleID)
	return out, nil
}

// ConfirmReady records every due occurrence that has an amount; the ones without are left out.
func (s *Service) ConfirmReady(ctx context.Context, userID, ruleID uuid.UUID, tz string) (*BulkResult, error) {
	lang := s.lang(ctx, userID)
	var slots []time.Time
	out, err := s.bulk(ctx, userID, ruleID, tz, func(tx pgx.Tx, r *Rule, o *occ, out *BulkResult, touched *[]uuid.UUID) error {
		if o.draft.Amount == nil || *o.draft.Amount <= 0 {
			out.LeftOut = append(out.LeftOut, LeftOut{Slot: o.slot.Format(dateLayout), Reason: "needs_amount"})
			return nil
		}
		// one occurrence at a time, each in its own savepoint: one the wallet / category
		// refuses is left out ("refused"), the rest are still recorded.
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		row, err := s.store(ctx, sp, o)
		var t []uuid.UUID
		if err == nil {
			t, _, err = s.recordOccurrenceTx(ctx, sp, r, row, nil, false, lang)
		}
		if err != nil {
			sp.Rollback(ctx)
			out.LeftOut = append(out.LeftOut, LeftOut{Slot: o.slot.Format(dateLayout), Reason: "refused"})
			return nil
		}
		if err := sp.Commit(ctx); err != nil {
			return err
		}
		*touched = append(*touched, t...)
		slots = append(slots, o.slot)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.bulkItems(ctx, userID, ruleID, slots, out)
}

// MarkPaidReady clears every due occurrence as paid.
func (s *Service) MarkPaidReady(ctx context.Context, userID, ruleID uuid.UUID, tz string) (*BulkResult, error) {
	var slots []time.Time
	out, err := s.bulk(ctx, userID, ruleID, tz, func(tx pgx.Tx, r *Rule, o *occ, out *BulkResult, touched *[]uuid.UUID) error {
		row, err := s.store(ctx, tx, o)
		if err != nil {
			return err
		}
		now := time.Now()
		row.ClearedAt = &now
		slots = append(slots, o.slot)
		return saveItemTx(ctx, tx, row)
	})
	if err != nil {
		return nil, err
	}
	return s.bulkItems(ctx, userID, ruleID, slots, out)
}

// SkipReady skips every due occurrence.
func (s *Service) SkipReady(ctx context.Context, userID, ruleID uuid.UUID, tz string) (*BulkResult, error) {
	var slots []time.Time
	out, err := s.bulk(ctx, userID, ruleID, tz, func(tx pgx.Tx, r *Rule, o *occ, out *BulkResult, touched *[]uuid.UUID) error {
		row, err := s.store(ctx, tx, o)
		if err != nil {
			return err
		}
		row.Archived = true
		slots = append(slots, o.slot)
		return saveItemTx(ctx, tx, row)
	})
	if err != nil {
		return nil, err
	}
	return s.bulkItems(ctx, userID, ruleID, slots, out)
}

func (s *Service) bulkItems(ctx context.Context, userID, ruleID uuid.UUID, slots []time.Time, out *BulkResult) (*BulkResult, error) {
	for _, sl := range slots {
		it, err := s.slotItem(ctx, userID, ruleID, sl)
		if err != nil && !errors.Is(err, ErrItemNotFound) {
			return nil, err
		}
		if it != nil {
			out.Items = append(out.Items, *it)
		}
	}
	return out, nil
}
