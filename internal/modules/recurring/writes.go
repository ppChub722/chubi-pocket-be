package recurring

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// ConflictError — items that can't take an action (409 with their ids).
type ConflictError struct{ IDs []uuid.UUID }

func (e *ConflictError) Error() string { return ErrAlreadyLinked.Error() }
func (e *ConflictError) Unwrap() error { return ErrAlreadyLinked }

func validateFields(f *Fields) error {
	if f.has("type") {
		if f.Type == nil {
			return ErrValidation
		}
		switch *f.Type {
		case "expense", "income", "transfer", typeSaving:
		default:
			return ErrValidation
		}
	}
	// null clears the amount of an occurrence; 0 = no amount (a reminder); a setting
	// needs one (checked on create / edit) except a reminder
	if f.has("amount") && f.Amount != nil && *f.Amount < 0 {
		return ErrValidation
	}
	if f.Description != nil && len([]rune(*f.Description)) > 200 {
		return ErrValidation
	}
	if f.Note != nil && len([]rune(*f.Note)) > 500 {
		return ErrValidation
	}
	if f.has("date") {
		if f.Date == nil {
			return ErrValidation
		}
		if _, err := parseDate(*f.Date); err != nil {
			return ErrValidation
		}
	}
	return nil
}

func applyToDraft(d *pending.Draft, f *Fields) {
	if f.has("type") {
		d.Type = f.Type
	}
	if f.has("amount") {
		d.Amount = amountPtrFrom(f.Amount)
	}
	if f.has("account_id") {
		d.AccountID = f.AccountID
	}
	if f.has("to_account_id") {
		d.TransferToAccountID = f.ToAccountID
	}
	if f.has("category_id") {
		d.CategoryID = f.CategoryID
	}
	if f.has("description") {
		d.Description = shared.CleanText(f.Description)
	}
	if f.has("note") {
		d.Note = shared.CleanText(f.Note)
	}
	if f.has("tag_ids") {
		d.TagIDs = append([]uuid.UUID(nil), f.TagIDs...)
	}
	if f.has("goal_id") {
		d.GoalID = f.GoalID
	}
	switch {
	case d.Type != nil && *d.Type == "transfer":
		d.CategoryID, d.GoalID = nil, nil
	case d.Type != nil && *d.Type == typeSaving:
		d.CategoryID, d.TransferToAccountID, d.TagIDs, d.Splits = nil, nil, nil, nil
	default:
		d.TransferToAccountID, d.GoalID = nil, nil
	}
}

func applyToRule(r *Rule, f *Fields) {
	if f.has("type") && f.Type != nil {
		r.Type = *f.Type
	}
	if f.has("amount") && f.Amount != nil {
		r.Amount = *f.Amount
	}
	if f.has("account_id") {
		r.AccountID = f.AccountID
	}
	if f.has("to_account_id") {
		r.ToAccountID = f.ToAccountID
	}
	if f.has("category_id") {
		r.CategoryID = f.CategoryID
	}
	if f.has("description") {
		r.Description = shared.CleanText(f.Description)
	}
	if f.has("note") {
		r.Note = shared.CleanText(f.Note)
	}
	if f.has("tag_ids") {
		r.TagIDs = append([]uuid.UUID{}, f.TagIDs...)
	}
	if f.has("goal_id") {
		r.GoalID = f.GoalID
	}
	switch r.Type {
	case "transfer":
		r.CategoryID, r.GoalID = nil, nil
	case typeSaving:
		r.CategoryID, r.ToAccountID, r.TagIDs = nil, nil, []uuid.UUID{}
	default:
		r.ToAccountID, r.GoalID = nil, nil
	}
}

func draftType(d pending.Draft) string {
	if d.Type == nil {
		return ""
	}
	return *d.Type
}

func checkDraftRefsTx(ctx context.Context, q rowQuerier, userID uuid.UUID, d pending.Draft) error {
	if err := checkRefsTx(ctx, q, userID, draftType(d), d.AccountID, d.TransferToAccountID, d.CategoryID, d.TagIDs); err != nil {
		return err
	}
	if draftType(d) == typeSaving {
		return checkSavingTx(ctx, q, userID, d.AccountID, d.GoalID)
	}
	return nil
}

// validateAuto — auto-create needs a fixed amount and a wallet.
func validateAuto(r *Rule) error {
	if !r.AutoCreate {
		return nil
	}
	if r.Amount <= 0 {
		return ErrAutoCreateNeedsAmount
	}
	if r.AccountID == nil {
		return ErrAutoCreateNeedsWallet
	}
	return nil
}

// syncLinkTx pushes an edit of a stored item on to what it is linked to: its
// transaction (or a legacy draft / saving entry).
func (s *Service) syncLinkTx(ctx context.Context, tx pgx.Tx, it *itemRow, f *Fields, date *string) ([]uuid.UUID, error) {
	switch {
	case it.SavingEntryID != nil:
		var amount *float64
		if f.has("amount") {
			amount = f.Amount
		}
		if s.savings == nil {
			return nil, nil
		}
		return nil, s.savings.UpdateEntryTx(ctx, tx, it.UserID, *it.SavingEntryID, amount, date, f.Note, f.has("note"))
	case it.PendingID != nil:
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT draft FROM pending_transactions WHERE id = $1 AND user_id = $2`,
			*it.PendingID, it.UserID).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		var d pending.Draft
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, err
		}
		applyToDraft(&d, f)
		if date != nil {
			d.Date = date
		}
		return nil, s.pending.Store().UpdateDraftTx(ctx, tx, it.UserID, *it.PendingID, d)
	case it.TransactionID != nil:
		var (
			typ string
			acc *uuid.UUID
		)
		err := tx.QueryRow(ctx, `SELECT type, account_id FROM transactions WHERE id = $1 AND user_id = $2`,
			*it.TransactionID, it.UserID).Scan(&typ, &acc)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if typ == "transfer" {
			return nil, nil
		}
		req := transactions.UpdateRequest{Date: date}
		var keys []string
		if f.has("amount") {
			req.Amount = f.Amount
		}
		if f.has("account_id") {
			req.AccountID, keys = f.AccountID, append(keys, "account_id")
		}
		if f.has("category_id") {
			req.CategoryID, keys = f.CategoryID, append(keys, "category_id")
		}
		if f.has("description") {
			req.Description, keys = f.Description, append(keys, "description")
		}
		if f.has("note") {
			req.Note, keys = f.Note, append(keys, "note")
		}
		if f.has("tag_ids") {
			req.TagIDs, keys = append([]uuid.UUID{}, f.TagIDs...), append(keys, "tag_ids")
		}
		req.MarkPresent(keys...)
		if err := s.txs.UpdateInTx(ctx, tx, it.UserID, *it.TransactionID, req); err != nil {
			return nil, err
		}
		touched := []uuid.UUID{}
		if acc != nil {
			touched = append(touched, *acc)
		}
		if f.has("account_id") && f.AccountID != nil {
			touched = append(touched, *f.AccountID)
		}
		return touched, nil
	}
	return nil, nil
}

// unlinkTx deletes what a stored item points at (a legacy draft, a saving
// entry, the transaction) and returns the transaction's id and the wallets
// it touched.
func (s *Service) unlinkTx(ctx context.Context, tx pgx.Tx, it *itemRow) (*uuid.UUID, []uuid.UUID, error) {
	if it.PendingID != nil {
		if err := s.pending.Store().DeleteTx(ctx, tx, it.UserID, *it.PendingID); err != nil {
			return nil, nil, err
		}
		it.PendingID = nil
	}
	if it.SavingEntryID != nil && s.savings != nil {
		if err := s.savings.DeleteEntryTx(ctx, tx, it.UserID, *it.SavingEntryID); err != nil {
			return nil, nil, err
		}
		it.SavingEntryID = nil
	}
	if it.TransactionID == nil {
		return nil, nil, nil
	}
	id := *it.TransactionID
	touched, err := s.txs.DeleteInTx(ctx, tx, it.UserID, id)
	if errors.Is(err, transactions.ErrTxNotFound) {
		it.TransactionID = nil
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	it.TransactionID = nil
	return &id, touched, nil
}

func (s *Service) begin(ctx context.Context) (pgx.Tx, error) {
	return s.db.Begin(ctx)
}

func (s *Service) afterWrite(ctx context.Context, userID, id uuid.UUID, tz string, touched []uuid.UUID) (*Detail, error) {
	if err := s.DueCheck(ctx, userID, tz); err != nil {
		return nil, err
	}
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	return s.detail(ctx, userID, id, today, 1, 20, touched)
}

func (s *Service) Create(ctx context.Context, userID uuid.UUID, req CreateRequest, tz string) (*Detail, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > 100 || req.Rule == nil {
		return nil, ErrValidation
	}
	kind := ""
	if req.Kind != nil {
		if !validKind(*req.Kind) {
			return nil, ErrValidation
		}
		kind = *req.Kind
	}
	if req.present == nil {
		req.present = map[string]bool{}
	}
	if req.Type == nil { // the kind implies the money type
		if kind == "" {
			return nil, ErrValidation
		}
		t := typeForKind(kind)
		req.Type, req.present["type"] = &t, true
	}
	if req.Amount == nil { // only a reminder may start without an amount
		if kind != KindReminder && !(kind == "" && *req.Type == "transfer") {
			return nil, ErrValidation
		}
		zero := 0.0
		req.Amount, req.present["amount"] = &zero, true
	}
	if err := validateFields(&req.Fields); err != nil {
		return nil, err
	}
	start, err := parseDate(req.StartDate)
	if err != nil {
		return nil, ErrValidation
	}
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	r := &Rule{UserID: userID, Name: name, IconCode: req.IconCode, Status: "active", TagIDs: []uuid.UUID{}}
	r.StartDate = start
	r.TrackedFrom, r.DueNotifiedUntil = today, &today
	applyToRule(r, &req.Fields)
	if err := r.applyRule(*req.Rule); err != nil {
		return nil, err
	}
	if err := r.applyEnd(req.End); err != nil {
		return nil, err
	}
	if err := r.setTrial(req.TrialUntil); err != nil {
		return nil, err
	}
	if err := r.finishKind(req.Kind); err != nil {
		return nil, err
	}
	if err := r.limits(today); err != nil {
		return nil, err
	}
	if req.AutoCreate != nil && *req.AutoCreate {
		r.AutoCreate = true
		yesterday := today.AddDate(0, 0, -1)
		r.AutoCreatedUntil = &yesterday
	}
	if err := validateAuto(r); err != nil {
		return nil, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := checkRuleRefsTx(ctx, tx, userID, r); err != nil {
		return nil, err
	}
	if err := saveRuleTx(ctx, tx, r); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.afterWrite(ctx, userID, r.ID, tz, nil)
}

// Update edits a setting. Its computed occurrences follow at once; stored
// ones (edited, recorded, skipped) stay as they are. also_update_tx pushes a
// change of name / category / wallet / tags / note to the transactions already
// recorded from it (never date or amount).
func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, req UpdateRequest, tz string) (*Detail, error) {
	if err := validateFields(&req.Fields); err != nil {
		return nil, err
	}
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	r, err := getRule(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	old := *r
	f := &req.Fields
	if f.has("amount") && f.Amount == nil {
		return nil, ErrValidation // a setting's amount can't be cleared (0 for a reminder)
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" || len([]rune(name)) > 100 {
			return nil, ErrValidation
		}
		r.Name = name
	}
	if req.has("icon_code") {
		r.IconCode = req.IconCode
	}
	applyToRule(r, f)
	rebuild := false
	if req.Rule != nil {
		if err := r.applyRule(*req.Rule); err != nil {
			return nil, err
		}
		rebuild = true
	}
	if req.StartDate != nil {
		if r.StartDate, err = parseDate(*req.StartDate); err != nil {
			return nil, ErrValidation
		}
		rebuild = true
	}
	if req.has("end") {
		if err := r.applyEnd(req.End); err != nil {
			return nil, err
		}
		rebuild = true
	}
	if req.TrialUntil != nil {
		if err := r.setTrial(req.TrialUntil); err != nil {
			return nil, err
		}
	}
	if err := r.finishKind(req.Kind); err != nil {
		return nil, err
	}
	if (old.Type == typeSaving) != (r.Type == typeSaving) {
		return nil, ErrValidation // a saving setting stays one (and a money one never becomes one)
	}
	if rebuild {
		r.GeneratedUntil, r.EndingNotified = nil, false
		if err := r.limits(today); err != nil {
			return nil, err
		}
	}
	if req.AutoCreate != nil && *req.AutoCreate != r.AutoCreate {
		r.AutoCreate = *req.AutoCreate
		yesterday := today.AddDate(0, 0, -1)
		r.AutoCreatedUntil = &yesterday // switching on / off never back-creates
	}
	if err := validateAuto(r); err != nil {
		return nil, err
	}
	if err := checkRuleRefsTx(ctx, tx, userID, r); err != nil {
		return nil, err
	}
	if err := saveRuleTx(ctx, tx, r); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	var upd *TxUpdateResult
	var touched []uuid.UUID
	if req.AlsoUpdateTx {
		if upd, touched, err = s.pushToRecorded(ctx, userID, &old, r, f, req.Name != nil, today); err != nil {
			return nil, err
		}
	}
	d, err := s.afterWrite(ctx, userID, r.ID, tz, touched)
	if err != nil {
		return nil, err
	}
	d.TxUpdate = upd
	return d, nil
}

// pushToRecorded — the descriptive fields of the setting go to the
// transactions recorded from it. A name change rewrites a transaction's
// description only while it still is the auto name from the OLD setting name;
// a transaction that cannot take the change is reported, never fails the edit.
func (s *Service) pushToRecorded(ctx context.Context, userID uuid.UUID, old, cur *Rule, f *Fields, nameSent bool, today time.Time) (*TxUpdateResult, []uuid.UUID, error) {
	nameChanged := nameSent && old.Name != cur.Name
	if !nameChanged && !f.has("category_id") && !f.has("account_id") && !f.has("tag_ids") && !f.has("note") {
		return nil, nil, nil
	}
	rows, err := s.storedRows(ctx, s.db, userID, []uuid.UUID{cur.ID}, nil, nil)
	if err != nil {
		return nil, nil, err
	}
	lang := s.lang(ctx, userID)
	res := &TxUpdateResult{Failed: []TxUpdateFailure{}}
	var touched []uuid.UUID
	oldSeqs, newSeqs := seqIndex(old), seqIndex(cur)
	for _, it := range rows {
		if it.TransactionID == nil {
			continue
		}
		slot := it.Date
		if it.Slot != nil {
			slot = *it.Slot
		}
		var (
			current *string
			prevAcc *uuid.UUID
		)
		if err := s.db.QueryRow(ctx, `SELECT description, account_id FROM transactions WHERE id = $1 AND user_id = $2`, *it.TransactionID, userID).Scan(&current, &prevAcc); err != nil {
			res.Failed = append(res.Failed, TxUpdateFailure{TransactionID: *it.TransactionID, Reason: "not_found"})
			continue
		}
		req := transactions.UpdateRequest{}
		var keys []string
		if f.has("category_id") {
			req.CategoryID, keys = f.CategoryID, append(keys, "category_id")
		}
		if f.has("account_id") {
			req.AccountID, keys = f.AccountID, append(keys, "account_id")
		}
		if f.has("tag_ids") {
			req.TagIDs, keys = append([]uuid.UUID{}, f.TagIDs...), append(keys, "tag_ids")
		}
		if f.has("note") {
			req.Note, keys = f.Note, append(keys, "note")
		}
		if nameChanged {
			oldSeq, newSeq := seqPtr(oldSeqs, slot), seqPtr(newSeqs, slot)
			if current != nil && *current == occName(old, slot, oldSeq, lang) {
				req.Description = ptr(occName(cur, slot, newSeq, lang))
				keys = append(keys, "description")
			}
		}
		if len(keys) == 0 {
			continue
		}
		req.MarkPresent(keys...)
		tx, err := s.begin(ctx)
		if err != nil {
			return nil, nil, err
		}
		if err := s.txs.UpdateInTx(ctx, tx, userID, *it.TransactionID, req); err != nil {
			tx.Rollback(ctx)
			res.Failed = append(res.Failed, TxUpdateFailure{TransactionID: *it.TransactionID, Reason: failReason(err)})
			continue
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, nil, err
		}
		res.UpdatedCount++
		if f.has("account_id") { // the wallet it left and the one it joined
			if prevAcc != nil {
				touched = append(touched, *prevAcc)
			}
			if f.AccountID != nil {
				touched = append(touched, *f.AccountID)
			}
		}
	}
	return res, touched, nil
}

func seqPtr(m map[string]int, slot time.Time) *int {
	if n, ok := m[slot.Format(dateLayout)]; ok {
		return &n
	}
	return nil
}

func failReason(err error) string {
	switch {
	case errors.Is(err, transactions.ErrAccountArchived):
		return "account_archived"
	case errors.Is(err, transactions.ErrSplitsOnTransfer):
		return "transfer"
	}
	return "refused"
}

func (s *Service) lockItems(ctx context.Context, tx pgx.Tx, where string, args ...any) ([]*itemRow, error) {
	rows, err := tx.Query(ctx, `SELECT `+prefixed(itemColumns, "i.")+` FROM recurring_items i `+where+
		` ORDER BY i.date, i.id FOR UPDATE`, args...)
	if err != nil {
		return nil, fmt.Errorf("lock items: %w", err)
	}
	defer rows.Close()
	var out []*itemRow
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func prefixed(cols, p string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = p + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID, deleteLinked bool) (*DeleteResult, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := getRule(ctx, tx, userID, id, true); err != nil {
		return nil, err
	}
	out := &DeleteResult{ID: id}
	var touched []uuid.UUID
	if deleteLinked {
		items, err := s.lockItems(ctx, tx, `WHERE i.rule_id = $1 AND i.user_id = $2
			AND (i.pending_id IS NOT NULL OR i.transaction_id IS NOT NULL OR i.saving_entry_id IS NOT NULL)`, id, userID)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			txID, t, err := s.unlinkTx(ctx, tx, it)
			if err != nil {
				return nil, err
			}
			if txID != nil {
				out.DeletedTransactionCount++
			}
			touched = append(touched, t...)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM recurring_rules WHERE id = $1 AND user_id = $2`, id, userID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if out.AccountBalances, err = shared.LoadWalletBalances(ctx, s.db, uniqueIDs(touched)); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) transition(ctx context.Context, userID, id uuid.UUID, tz string, fn func(tx pgx.Tx, r *Rule, today time.Time) error) (*Detail, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	r, err := getRule(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	if err := fn(tx, r, today); err != nil {
		return nil, err
	}
	if err := saveRuleTx(ctx, tx, r); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.afterWrite(ctx, userID, id, tz, nil)
}

func (s *Service) Pause(ctx context.Context, userID, id uuid.UUID, tz string) (*Detail, error) {
	return s.transition(ctx, userID, id, tz, func(tx pgx.Tx, r *Rule, today time.Time) error {
		if r.Status != "active" {
			return ErrInvalidTransition
		}
		r.Status, r.PausedFrom = "paused", &today
		return nil
	})
}

// Resume — what was due before the pause stays due; the occurrences inside the
// paused gap [paused_from, today) are skipped (stored as skipped rows: not due,
// not paid). Auto-create and the due notice start from today.
func (s *Service) Resume(ctx context.Context, userID, id uuid.UUID, tz string) (*Detail, error) {
	return s.transition(ctx, userID, id, tz, func(tx pgx.Tx, r *Rule, today time.Time) error {
		if r.Status != "paused" {
			return ErrInvalidTransition
		}
		if err := s.checkWalletsActive(ctx, tx, r); err != nil {
			return err
		}
		if r.Type == typeSaving { // the goal must be active again
			if err := checkSavingTx(ctx, tx, r.UserID, r.AccountID, r.GoalID); err != nil {
				return err
			}
		}
		r.Status = "active"
		if err := r.limits(today); err != nil {
			return err
		}
		gapFrom := time.Date(r.UpdatedAt.Year(), r.UpdatedAt.Month(), r.UpdatedAt.Day(), 0, 0, 0, 0, time.UTC)
		if r.PausedFrom != nil {
			gapFrom = *r.PausedFrom
		}
		if gapFrom.Before(r.TrackedFrom) {
			gapFrom = r.TrackedFrom
		}
		if gapFrom.Before(today) {
			for _, d := range r.Spec.Between(gapFrom, today.AddDate(0, 0, -1)) {
				slot := d
				if _, err := insertItemTx(ctx, tx, &itemRow{UserID: r.UserID, RuleID: r.ID, Slot: &slot, Date: d,
					Draft: ruleBaseDraft(r), Archived: true}); err != nil {
					return err
				}
			}
		}
		r.PausedFrom, r.DueNotifiedUntil = nil, &today
		yesterday := today.AddDate(0, 0, -1)
		r.AutoCreatedUntil = &yesterday
		return nil
	})
}

// checkWalletsActive — a setting on an archived wallet can't run.
func (s *Service) checkWalletsActive(ctx context.Context, tx pgx.Tx, r *Rule) error {
	var archived bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounts WHERE id = ANY($1) AND status = 'archived')`,
		uniqueIDs(append(ptrIDs(r.AccountID), ptrIDs(r.ToAccountID)...))).Scan(&archived); err != nil {
		return err
	}
	if archived {
		return transactions.ErrAccountArchived
	}
	return nil
}

func ptrIDs(p *uuid.UUID) []uuid.UUID {
	if p == nil {
		return nil
	}
	return []uuid.UUID{*p}
}

func (s *Service) Archive(ctx context.Context, userID, id uuid.UUID, tz string) (*Detail, error) {
	return s.transition(ctx, userID, id, tz, func(tx pgx.Tx, r *Rule, today time.Time) error {
		if r.Status == "archived" {
			return ErrInvalidTransition
		}
		if r.Status == "active" {
			r.PausedFrom = &today
		}
		r.Status = "archived"
		return nil
	})
}

func (s *Service) Restore(ctx context.Context, userID, id uuid.UUID, tz string) (*Detail, error) {
	return s.transition(ctx, userID, id, tz, func(_ pgx.Tx, r *Rule, _ time.Time) error {
		if r.Status != "archived" {
			return ErrInvalidTransition
		}
		r.Status = "paused"
		return nil
	})
}

// Extend pushes the END of a setting that has one further out (a setting
// without an end has nothing to extend: its occurrences are computed).
func (s *Service) Extend(ctx context.Context, userID, id uuid.UUID, req ExtendRequest, tz string) (*Detail, error) {
	return s.transition(ctx, userID, id, tz, func(tx pgx.Tx, r *Rule, today time.Time) error {
		if r.Status == "archived" || r.Freq == "dates" || r.EndKind == "none" || req.End == nil {
			return ErrInvalidTransition
		}
		if err := r.applyEnd(req.End); err != nil {
			return err
		}
		r.GeneratedUntil = nil
		if err := r.limits(today); err != nil {
			return err
		}
		r.EndingNotified = false
		_, err := tx.Exec(ctx, `UPDATE notifications SET actioned_at = NOW(), read_at = COALESCE(read_at, NOW())
			WHERE recipient_user_id = $1 AND type = 'recurring_ending' AND actioned_at IS NULL AND dismissed_at IS NULL
			  AND payload->>'recurring_id' = $2`, userID, r.ID.String())
		return err
	})
}

func (s *Service) itemsResult(ctx context.Context, userID uuid.UUID, ids, touched []uuid.UUID) (*ItemsWriteResult, error) {
	out := &ItemsWriteResult{Items: []Item{}}
	var rs []*uuid.UUID
	for _, id := range uniqueIDs(ids) {
		it, err := s.itemView(ctx, userID, id)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, *it)
		rs = append(rs, ruleOf(it))
	}
	out.Setting = s.settingOf(ctx, userID, rs...)
	var err error
	if out.AccountBalances, err = shared.LoadWalletBalances(ctx, s.db, uniqueIDs(touched)); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) itemResult(ctx context.Context, userID, id uuid.UUID, txRow any, touched []uuid.UUID) (*ItemWriteResult, error) {
	it, err := s.itemView(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	return s.resultOf(ctx, userID, it, txRow, touched)
}

// resultOf wraps an item (stored or computed) with the setting and the balances of a write.
func (s *Service) resultOf(ctx context.Context, userID uuid.UUID, it *Item, txRow any, touched []uuid.UUID) (*ItemWriteResult, error) {
	out := &ItemWriteResult{Item: *it, Transaction: txRow}
	out.Setting = s.settingOf(ctx, userID, ruleOf(it))
	var err error
	if out.AccountBalances, err = shared.LoadWalletBalances(ctx, s.db, uniqueIDs(touched)); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateItem adds an extra, manual occurrence to a setting.
func (s *Service) CreateItem(ctx context.Context, userID uuid.UUID, req ItemCreateRequest, tz string) (*ItemWriteResult, error) {
	// Only an occurrence of a rule: a one-off upcoming item no longer exists (a
	// future-dated transaction is just a transaction).
	if req.Date == nil || req.Type == nil || req.Amount == nil || req.RecurringID == nil {
		return nil, ErrValidation
	}
	if err := validateFields(&req.Fields); err != nil {
		return nil, err
	}
	date, _ := parseDate(*req.Date)
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	it := &itemRow{UserID: userID, Date: date, Manual: true, Edited: true}
	r, err := getRule(ctx, tx, userID, *req.RecurringID, false)
	if err != nil {
		return nil, err
	}
	it.RuleID, it.Draft = r.ID, ruleBaseDraft(r)
	applyToDraft(&it.Draft, &req.Fields)
	if switchesSaving(ruleBaseDraft(r), it.Draft) {
		return nil, ErrValidation
	}
	if err := checkDraftRefsTx(ctx, tx, userID, it.Draft); err != nil {
		return nil, err
	}
	if _, err := insertItemTx(ctx, tx, it); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.itemResult(ctx, userID, it.ID, nil, nil)
}

// UpdateItem edits a STORED item (scope this only; to change what a setting
// gives, edit the setting).
func (s *Service) UpdateItem(ctx context.Context, userID, id uuid.UUID, req ItemUpdateRequest, tz string) (*ItemsWriteResult, error) {
	if err := validateFields(&req.Fields); err != nil {
		return nil, err
	}
	if req.Scope != "" && req.Scope != "this" {
		return nil, ErrValidation
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	it, err := getItem(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	var touched []uuid.UUID
	t, err := s.applyItemEdit(ctx, tx, it, &req.Fields, req.AlsoUpdateLink)
	if err != nil {
		return nil, err
	}
	touched = append(touched, t...)
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.itemsResult(ctx, userID, []uuid.UUID{id}, touched)
}

// applyItemEdit puts the fields on a stored row, marks it edited and pushes the
// change to what it is linked to.
func (s *Service) applyItemEdit(ctx context.Context, tx pgx.Tx, it *itemRow, f *Fields, alsoUpdateLink bool) ([]uuid.UUID, error) {
	before := it.Draft
	applyToDraft(&it.Draft, f)
	if switchesSaving(before, it.Draft) {
		return nil, ErrValidation
	}
	if err := checkDraftRefsTx(ctx, tx, it.UserID, it.Draft); err != nil {
		return nil, err
	}
	var date *string
	if f.has("date") {
		d, _ := parseDate(*f.Date)
		it.Date, date = d, f.Date
	}
	if f.values() || date != nil {
		it.Edited = true
	}
	var touched []uuid.UUID
	if alsoUpdateLink {
		t, err := s.syncLinkTx(ctx, tx, it, f, date)
		if err != nil {
			return nil, err
		}
		touched = append(touched, t...)
	}
	if err := saveItemTx(ctx, tx, it); err != nil {
		return nil, err
	}
	return touched, nil
}

// DeleteItem removes a stored row: the occurrence goes back to its computed
// state. deleteLinked also deletes the transaction recorded from it.
func (s *Service) DeleteItem(ctx context.Context, userID, id uuid.UUID, deleteLinked bool) (*ItemDeleteResult, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	it, err := getItem(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	out := &ItemDeleteResult{ID: id}
	var touched []uuid.UUID
	if deleteLinked {
		if out.TransactionID, touched, err = s.unlinkTx(ctx, tx, it); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM recurring_items WHERE id = $1 AND user_id = $2`, id, userID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if out.AccountBalances, err = shared.LoadWalletBalances(ctx, s.db, uniqueIDs(touched)); err != nil {
		return nil, err
	}
	out.Setting = s.settingOf(ctx, userID, &it.RuleID)
	return out, nil
}

func (s *Service) setArchived(ctx context.Context, userID, id uuid.UUID, archived bool) (*ItemWriteResult, error) {
	tag, err := s.db.Exec(ctx, `UPDATE recurring_items SET archived = $3 WHERE id = $1 AND user_id = $2`, id, userID, archived)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrItemNotFound
	}
	return s.itemResult(ctx, userID, id, nil, nil)
}

func (s *Service) ArchiveItem(ctx context.Context, userID, id uuid.UUID) (*ItemWriteResult, error) {
	return s.setArchived(ctx, userID, id, true)
}

func (s *Service) RestoreItem(ctx context.Context, userID, id uuid.UUID) (*ItemWriteResult, error) {
	return s.setArchived(ctx, userID, id, false)
}

// TxEditedTx — a transaction linked to an occurrence was edited with
// also_update_link: the occurrence keeps the new values.
// itemOfTx finds the stored row that records a transaction — or its other leg
// when it is half of a transfer — whoever owns the row (the rule's owner may
// not be the member who edits / deletes the transaction of a shared wallet).
func itemOfTx(ctx context.Context, tx pgx.Tx, txID uuid.UUID) (*itemRow, error) {
	var id, owner uuid.UUID
	err := tx.QueryRow(ctx, `SELECT i.id, i.user_id FROM recurring_items i
		WHERE i.transaction_id IN (SELECT t.id FROM transactions t WHERE t.id = $1
			OR (t.transfer_group_id IS NOT NULL
			    AND t.transfer_group_id = (SELECT g.transfer_group_id FROM transactions g WHERE g.id = $1)))
		LIMIT 1 FOR UPDATE`, txID).Scan(&id, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return getItem(ctx, tx, owner, id, false)
}

func (s *Service) TxEditedTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, row *transactions.Transaction, tagIDs []uuid.UUID, tagsSet bool) error {
	it, err := itemOfTx(ctx, tx, row.ID)
	if err != nil || it == nil {
		return err
	}
	typ := string(row.Type)
	it.Draft.Type, it.Draft.Amount, it.Draft.AccountID, it.Draft.CategoryID = &typ, ptr(row.Amount), row.AccountID, row.CategoryID
	it.Draft.Description, it.Draft.Note = row.Description, row.Note
	if tagsSet {
		it.Draft.TagIDs = append([]uuid.UUID(nil), tagIDs...)
	}
	it.Edited = true
	return saveItemTx(ctx, tx, it)
}

// TxDeletingTx — a transaction linked to an occurrence is being deleted: the
// occurrence goes back to its state before (computed due / upcoming; paid when
// it was cleared first). skip stores it as skipped instead. It returns the
// item id when a row stays.
func (s *Service) TxDeletingTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID, skip bool) (*uuid.UUID, error) {
	it, err := itemOfTx(ctx, tx, txID)
	if err != nil || it == nil {
		return nil, err
	}
	if skip {
		it.TransactionID, it.AutoCreatedAt, it.Archived = nil, nil, true
		id := it.ID
		return &id, saveItemTx(ctx, tx, it)
	}
	return s.release(ctx, tx, it)
}

// release drops the transaction link of a row: a row with nothing else on it
// goes (the occurrence is computed again); one that was cleared, edited or
// added by hand stays without the transaction.
func (s *Service) release(ctx context.Context, tx pgx.Tx, it *itemRow) (*uuid.UUID, error) {
	it.TransactionID, it.AutoCreatedAt = nil, nil
	if it.ClearedAt == nil && !it.Edited && !it.Manual && !it.Archived && it.PendingID == nil && it.SavingEntryID == nil {
		_, err := tx.Exec(ctx, `DELETE FROM recurring_items WHERE id = $1`, it.ID)
		return nil, err
	}
	id := it.ID
	return &id, saveItemTx(ctx, tx, it)
}

// OnDraftSubmitted — a legacy recurring draft was submitted: its item links to
// what it became.
func (s *Service) OnDraftSubmitted(ctx context.Context, tx pgx.Tx, userID, draftID, id uuid.UUID, kind string) error {
	col := "transaction_id"
	if kind == pending.KindSaving {
		col = "saving_entry_id"
	}
	_, err := tx.Exec(ctx, `UPDATE recurring_items SET `+col+` = $3, pending_id = NULL
		WHERE pending_id = $2 AND user_id = $1`, userID, draftID, id)
	return err
}
