package recurring

import (
	"context"
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
		case "expense", "income", "transfer", typeSaving, typeDebt:
		default:
			return ErrValidation
		}
	}
	if f.has("debt_kind") && (f.DebtKind == nil || (*f.DebtKind != DebtKindDebt && *f.DebtKind != DebtKindRepayment)) {
		return ErrValidation
	}
	if f.has("direction") && (f.Direction == nil || (*f.Direction != "paid" && *f.Direction != "received")) {
		return ErrValidation
	}
	if f.PersonName != nil {
		if n := len([]rune(strings.TrimSpace(*f.PersonName))); n == 0 || n > 100 {
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
	if f.has("contact_id") {
		r.ContactID = f.ContactID
		if f.ContactID != nil && !f.has("person_name") {
			r.PersonName = nil // the contact's name is taken in checkRuleRefsTx
		}
	}
	if f.has("person_name") {
		r.PersonName = shared.CleanText(f.PersonName)
		if !f.has("contact_id") {
			r.ContactID = nil // a typed name replaces the contact
		}
	}
	if f.has("debt_kind") {
		r.DebtKind = f.DebtKind
	}
	if f.has("direction") {
		r.DebtDirection = f.Direction
	}
	switch r.Type {
	case "transfer":
		r.CategoryID, r.GoalID = nil, nil
	case typeSaving:
		r.CategoryID, r.ToAccountID, r.TagIDs = nil, nil, []uuid.UUID{}
	default: // a debt setting keeps a sent category / tags so finishKind rejects them
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
		if _, err := tx.Exec(ctx, `UPDATE recurring_items SET saving_entry_id = NULL WHERE id = $1`, it.ID); err != nil {
			return nil, nil, err
		}
		if err := s.savings.DeleteEntryTx(ctx, tx, it.UserID, *it.SavingEntryID); err != nil {
			return nil, nil, err
		}
		it.SavingEntryID = nil
	}
	if it.TransactionID == nil {
		return nil, nil, nil
	}
	id := *it.TransactionID
	// let go first: the delete hook would otherwise skip the occurrence (R2)
	if _, err := tx.Exec(ctx, `UPDATE recurring_items SET transaction_id = NULL WHERE id = $1`, it.ID); err != nil {
		return nil, nil, err
	}
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
// ones (edited, recorded, skipped) stay as they are, and a filled one never
// changes: it reads its transaction (0.4.1-1 R6).
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
	if (old.Type == typeSaving) != (r.Type == typeSaving) || (old.Type == typeDebt) != (r.Type == typeDebt) {
		return nil, ErrValidation // a saving / debt setting stays one (and a money one never becomes one)
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

func seqPtr(m map[string]int, slot time.Time) *int {
	if n, ok := m[slot.Format(dateLayout)]; ok {
		return &n
	}
	return nil
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
	var removed []uuid.UUID
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
				removed = append(removed, *txID)
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
	out.Effects = s.effectsOf(ctx, userID, nil, nil, removed, out.AccountBalances)
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

// Pause — manual. The due occurrences are stored so they stay due while it is
// paused (0.4.1-1 R7, the paused-vanish fix).
func (s *Service) Pause(ctx context.Context, userID, id uuid.UUID, tz string) (*Detail, error) {
	return s.transition(ctx, userID, id, tz, func(tx pgx.Tx, r *Rule, today time.Time) error {
		if r.Status != "active" {
			return ErrInvalidTransition
		}
		due, err := s.dueOccs(ctx, tx, r, today)
		if err != nil {
			return err
		}
		return s.pauseTx(ctx, tx, r, due, today, PauseManual)
	})
}

// Resume — any reason. The next occurrence is the current period's; the ones
// between the pause and it are never made (a gap, R8); an installment ending by
// count shifts instead of losing numbers (R9). What was due before the pause
// stays due. A setting that still holds 3 due turns inactive again at its next
// due. The due notice starts from today; auto-create from the current period's
// occurrence (R12: it never waits). An archived wallet no longer blocks it.
func (s *Service) Resume(ctx context.Context, userID, id uuid.UUID, tz string) (*Detail, error) {
	return s.transition(ctx, userID, id, tz, func(tx pgx.Tx, r *Rule, today time.Time) error {
		if r.Status != "paused" {
			return ErrInvalidTransition
		}
		if r.Type == typeSaving { // the goal must be active again
			if err := checkSavingTx(ctx, tx, r.UserID, r.AccountID, r.GoalID); err != nil {
				return err
			}
		}
		return r.resume(today)
	})
}

// resume — active again from the current period (the gap since the pause is skipped, R8).
func (r *Rule) resume(today time.Time) error {
	r.Status = "active"
	r.resumeGap(today)
	if err := r.limits(today); err != nil {
		return err
	}
	r.PausedFrom, r.PauseReason, r.DueNotifiedUntil, r.ZeroRounds = nil, nil, &today, 0
	autoFrom := today
	if cur := r.currentSlot(today); cur != nil && cur.Before(today) {
		autoFrom = *cur
	}
	until := autoFrom.AddDate(0, 0, -1)
	r.AutoCreatedUntil = &until
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
		if r.Status == "active" { // as a pause: its due ones are kept for a restore
			due, err := s.dueOccs(ctx, tx, r, today)
			if err != nil {
				return err
			}
			if err := s.pauseTx(ctx, tx, r, due, today, PauseManual); err != nil {
				return err
			}
		}
		r.Status = "archived"
		return nil
	})
}

// Restore — an archived setting comes back paused, with the reason it had
// (manual when it had none).
func (s *Service) Restore(ctx context.Context, userID, id uuid.UUID, tz string) (*Detail, error) {
	return s.transition(ctx, userID, id, tz, func(_ pgx.Tx, r *Rule, _ time.Time) error {
		if r.Status != "archived" {
			return ErrInvalidTransition
		}
		r.Status = "paused"
		if r.PauseReason == nil {
			r.PauseReason = ptr(PauseManual)
		}
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
	out.Effects = s.effectsOf(ctx, userID, out.Items, nil, nil, out.AccountBalances)
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
	out.Effects = s.effectsOf(ctx, userID, []Item{*it}, nil, nil, out.AccountBalances)
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
	if err := s.applyItemEdit(ctx, tx, it, &req.Fields); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.itemsResult(ctx, userID, []uuid.UUID{id}, nil)
}

// applyItemEdit puts the fields on a stored row and marks it edited. Only an
// open (or paid) occurrence takes an edit: a filled one reads its transaction /
// saving entry, which is edited instead (0.4.1-1 R1).
func (s *Service) applyItemEdit(ctx context.Context, tx pgx.Tx, it *itemRow, f *Fields) error {
	if it.filled() {
		return ErrAlreadyRecorded
	}
	before := it.Draft
	applyToDraft(&it.Draft, f)
	if switchesSaving(before, it.Draft) {
		return ErrValidation
	}
	if err := checkDraftRefsTx(ctx, tx, it.UserID, it.Draft); err != nil {
		return err
	}
	if f.has("date") {
		it.Date, _ = parseDate(*f.Date)
	}
	if f.values() || f.has("date") {
		it.Edited = true
	}
	return saveItemTx(ctx, tx, it)
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
	var slots []Item
	if it.Slot != nil { // the occurrence, computed again
		if v, err := s.slotItem(ctx, userID, it.RuleID, *it.Slot); err == nil {
			slots = append(slots, *v)
		}
	}
	var removedTx []uuid.UUID
	if out.TransactionID != nil {
		removedTx = append(removedTx, *out.TransactionID)
	}
	out.Effects = s.effectsOf(ctx, userID, slots, nil, removedTx, out.AccountBalances)
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

// TxDeletingTx — a transaction that fills an occurrence is being deleted, by
// any path (0.4.1-1 R2 / R3): the occurrence turns skipped and keeps the
// transaction's last values (amount, wallet, category, tags, description,
// note, date), so un-skipping it brings it back due with them. It returns the
// occurrence's row, nil when the transaction filled none.
func (s *Service) TxDeletingTx(ctx context.Context, tx pgx.Tx, txID uuid.UUID) (*uuid.UUID, error) {
	it, err := itemOfTx(ctx, tx, txID)
	if err != nil || it == nil {
		return nil, err
	}
	if err := keepTxValuesTx(ctx, tx, it); err != nil {
		return nil, err
	}
	it.TransactionID, it.AutoCreatedAt, it.ClearedAt = nil, nil, nil
	it.Archived, it.Edited = true, true
	id := it.ID
	return &id, saveItemTx(ctx, tx, it)
}

// keepTxValuesTx copies the values of the transaction that fills the row into
// its draft and date (a transfer: both wallets, no category).
func keepTxValuesTx(ctx context.Context, tx pgx.Tx, it *itemRow) error {
	var (
		typ                string
		amount             float64
		acc, cat, from, to *uuid.UUID
		desc, note         *string
		date               time.Time
	)
	if err := tx.QueryRow(ctx, `SELECT t.type, t.amount::float8, t.account_id, t.category_id, t.description, t.note, t.date,
			(SELECT o.account_id FROM transactions o JOIN categories c ON c.id = o.category_id
			 WHERE o.transfer_group_id = t.transfer_group_id AND c.system_kind = 'TRANSFER_OUT' LIMIT 1),
			(SELECT i.account_id FROM transactions i LEFT JOIN categories c ON c.id = i.category_id
			 WHERE i.transfer_group_id = t.transfer_group_id AND c.system_kind IS DISTINCT FROM 'TRANSFER_OUT' LIMIT 1)
		FROM transactions t WHERE t.id = $1`, *it.TransactionID).Scan(&typ, &amount, &acc, &cat, &desc, &note, &date, &from, &to); err != nil {
		return fmt.Errorf("filled tx: %w", err)
	}
	tags, err := collectUUIDs(ctx, tx, `SELECT tt.tag_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id
		WHERE tt.transaction_id = $1 AND g.user_id = $2 ORDER BY tt.tag_id`, *it.TransactionID, it.UserID)
	if err != nil {
		return err
	}
	d := &it.Draft
	d.Type, d.Amount, d.Description, d.Note, d.TagIDs = &typ, &amount, desc, note, tags
	d.AccountID, d.CategoryID, d.TransferToAccountID = acc, cat, nil
	if typ == "transfer" {
		d.AccountID, d.TransferToAccountID, d.CategoryID = from, to, nil
	}
	it.Date = date
	return nil
}

func collectUUIDs(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}

// release drops the transaction link of a row: a row with nothing else on it
// goes (the occurrence is computed again); one that was cleared, edited or
// added by hand stays without the transaction.
func (s *Service) release(ctx context.Context, tx pgx.Tx, it *itemRow) (*uuid.UUID, error) {
	it.TransactionID, it.AutoCreatedAt = nil, nil
	if it.ClearedAt == nil && !it.Edited && !it.Manual && !it.Archived && it.PendingID == nil && it.SavingEntryID == nil &&
		it.DebtEntryID == nil {
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
