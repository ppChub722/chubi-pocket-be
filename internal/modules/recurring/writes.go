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

// ConflictError — items that can't take a batch action (409 with their ids).
type ConflictError struct{ IDs []uuid.UUID }

func (e *ConflictError) Error() string { return ErrAlreadyLinked.Error() }
func (e *ConflictError) Unwrap() error { return ErrAlreadyLinked }

// --- fields ---

func validateFields(f *Fields) error {
	if f.has("type") {
		if f.Type == nil {
			return ErrValidation
		}
		switch *f.Type {
		case "expense", "income", "transfer":
		default:
			return ErrValidation
		}
	}
	if f.has("amount") && (f.Amount == nil || *f.Amount < 0) { // 0 = no amount (a reminder); kinds that need one check it
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

// applyToDraft — the sent value fields onto d (the date is the caller's).
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

// --- the other side of a link ---

// syncLinkTx gives an item's linked draft / transaction the sent fields (and
// date). A linked transfer keeps its values (its pair is edited from the
// transaction). Returns the wallets touched.
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

// unlinkTx deletes an item's linked draft / transaction (delete_linked_tx).
// Returns the deleted transaction id and the wallets touched.
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
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	it.TransactionID = nil
	return &id, touched, nil
}

// --- settings ---

func (s *Service) begin(ctx context.Context) (pgx.Tx, error) {
	return s.db.Begin(ctx)
}

// afterWrite — the due check (a write may make items due), then the page.
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
	r := &Rule{UserID: userID, Name: name, IconCode: req.IconCode, StartDate: start, Status: "active", TagIDs: []uuid.UUID{}}
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
	if err := s.generateTx(ctx, tx, r, r.StartDate); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.afterWrite(ctx, userID, r.ID, tz, nil)
}

func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, req UpdateRequest, tz string) (*Detail, error) {
	if err := validateFields(&req.Fields); err != nil {
		return nil, err
	}
	scope := req.Scope
	if scope == "" {
		scope = "upcoming"
	}
	if scope != "upcoming" && scope != "all" {
		return nil, ErrValidation
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
	f := &req.Fields
	// Without a description of its own an item shows the setting's name:
	// a rename carries over to the items still showing the old one.
	var renamed *[2]string
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" || len([]rune(name)) > 100 {
			return nil, ErrValidation
		}
		if name != r.Name && (r.Description == nil || *r.Description == "") && !f.has("description") {
			renamed = &[2]string{r.Name, name}
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
	if rebuild {
		r.GeneratedUntil, r.EndingNotified = nil, false
		if err := r.limits(today); err != nil {
			return nil, err
		}
	}
	if err := checkRuleRefsTx(ctx, tx, userID, r); err != nil {
		return nil, err
	}
	if err := saveRuleTx(ctx, tx, r); err != nil {
		return nil, err
	}
	var touched []uuid.UUID
	if rebuild {
		if err := dropUnproducedTx(ctx, tx, r.ID); err != nil {
			return nil, err
		}
		if r.Status == "active" {
			if err := s.generateTx(ctx, tx, r, today); err != nil {
				return nil, err
			}
		}
	}
	if f.values() || renamed != nil {
		cond := `i.produced_at IS NULL AND i.pending_id IS NULL AND i.transaction_id IS NULL`
		if scope == "all" {
			cond = `TRUE`
		}
		items, err := s.lockItems(ctx, tx, `WHERE i.rule_id = $1 AND i.user_id = $2 AND `+cond, r.ID, userID)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			applyToDraft(&it.Draft, f)
			if renamed != nil && it.Draft.Description != nil && *it.Draft.Description == renamed[0] {
				it.Draft.Description = ptr(renamed[1])
			}
			if err := saveItemTx(ctx, tx, it); err != nil {
				return nil, err
			}
			if scope == "all" && req.AlsoUpdateLink && f.values() {
				t, err := s.syncLinkTx(ctx, tx, it, f, nil)
				if err != nil {
					return nil, err
				}
				touched = append(touched, t...)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.afterWrite(ctx, userID, r.ID, tz, touched)
}

// lockItems — the items matching where (alias i), locked.
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

// prefixed — "a, b" → "i.a, i.b".
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

// transition — pause / resume / archive / restore / extend.
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
	return s.transition(ctx, userID, id, tz, func(tx pgx.Tx, r *Rule, _ time.Time) error {
		if r.Status != "active" {
			return ErrInvalidTransition
		}
		r.Status = "paused"
		return dropUnproducedTx(ctx, tx, r.ID)
	})
}

func (s *Service) Resume(ctx context.Context, userID, id uuid.UUID, tz string) (*Detail, error) {
	return s.transition(ctx, userID, id, tz, func(tx pgx.Tx, r *Rule, today time.Time) error {
		if r.Status != "paused" {
			return ErrInvalidTransition
		}
		if err := s.checkWalletsActive(ctx, tx, r); err != nil {
			return err
		}
		r.Status = "active"
		if err := r.limits(today); err != nil {
			return err
		}
		return s.generateTx(ctx, tx, r, today)
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
	return s.transition(ctx, userID, id, tz, func(tx pgx.Tx, r *Rule, _ time.Time) error {
		if r.Status == "archived" {
			return ErrInvalidTransition
		}
		r.Status = "archived"
		return dropUnproducedTx(ctx, tx, r.ID)
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

func (s *Service) Extend(ctx context.Context, userID, id uuid.UUID, req ExtendRequest, tz string) (*Detail, error) {
	return s.transition(ctx, userID, id, tz, func(tx pgx.Tx, r *Rule, today time.Time) error {
		if r.Status == "archived" || r.Freq == "dates" {
			return ErrInvalidTransition
		}
		if req.End == nil {
			if r.EndKind != "none" {
				return ErrValidation
			}
			from := today
			if r.GeneratedUntil != nil && r.GeneratedUntil.After(from) {
				from = *r.GeneratedUntil
			}
			h := r.horizonSpan(from)
			r.GeneratedUntil = &h
		} else {
			if err := r.applyEnd(req.End); err != nil {
				return err
			}
			r.GeneratedUntil = nil
			if err := r.limits(today); err != nil {
				return err
			}
		}
		r.EndingNotified = false
		// ต่ออีก answers the "ending soon" notice of this setting.
		if _, err := tx.Exec(ctx, `UPDATE notifications SET actioned_at = NOW(), read_at = COALESCE(read_at, NOW())
			WHERE recipient_user_id = $1 AND type = 'recurring_ending' AND actioned_at IS NULL AND dismissed_at IS NULL
			  AND payload->>'recurring_id' = $2`, userID, r.ID.String()); err != nil {
			return err
		}
		if r.Status != "active" {
			return nil
		}
		return s.generateTx(ctx, tx, r, today)
	})
}

// --- items ---

func (s *Service) itemsResult(ctx context.Context, userID uuid.UUID, ids, touched []uuid.UUID) (*ItemsWriteResult, error) {
	today, err := s.today(ctx, userID, "")
	if err != nil {
		return nil, err
	}
	items, err := s.itemViews(ctx, s.db, userID, today, `WHERE i.id = ANY($1) AND i.user_id = $2 ORDER BY i.date, i.id`,
		uniqueIDs(ids), userID)
	if err != nil {
		return nil, err
	}
	out := &ItemsWriteResult{Items: items}
	var rs []*uuid.UUID
	for i := range items {
		rs = append(rs, ruleOf(&items[i]))
	}
	out.Setting = s.settingOf(ctx, userID, rs...)
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
	out := &ItemWriteResult{Item: *it, Transaction: txRow}
	out.Setting = s.settingOf(ctx, userID, ruleOf(it))
	if out.AccountBalances, err = shared.LoadWalletBalances(ctx, s.db, uniqueIDs(touched)); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateItem — POST /recurring-items: a manual item.
func (s *Service) CreateItem(ctx context.Context, userID uuid.UUID, req ItemCreateRequest, tz string) (*ItemWriteResult, error) {
	if req.Date == nil || req.Type == nil || req.Amount == nil {
		return nil, ErrValidation
	}
	if err := validateFields(&req.Fields); err != nil {
		return nil, err
	}
	date, _ := parseDate(*req.Date)
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	it := &itemRow{UserID: userID, Date: date, Manual: true}
	if req.RecurringID != nil {
		r, err := getRule(ctx, tx, userID, *req.RecurringID, false)
		if err != nil {
			return nil, err
		}
		it.RuleID, it.Draft = &r.ID, draftFromRule(r)
	}
	applyToDraft(&it.Draft, &req.Fields)
	if err := checkDraftRefsTx(ctx, tx, userID, it.Draft); err != nil {
		return nil, err
	}
	if !date.After(today) {
		now := time.Now()
		it.ProducedAt = &now // not_created: "สร้างใหม่" makes it real
	}
	if _, err := insertItemTx(ctx, tx, it); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.itemResult(ctx, userID, it.ID, nil, nil)
}

// UpdateItem — PUT /recurring-items/:id.
func (s *Service) UpdateItem(ctx context.Context, userID, id uuid.UUID, req ItemUpdateRequest, tz string) (*ItemsWriteResult, error) {
	if err := validateFields(&req.Fields); err != nil {
		return nil, err
	}
	scope := req.Scope
	if scope == "" {
		scope = "this"
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
	it, err := getItem(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	items := []*itemRow{it}
	if scope != "this" {
		if req.has("date") {
			return nil, ErrValidation // the date changes on one item only
		}
		if it.RuleID == nil {
			scope = "this"
		}
	}
	switch scope {
	case "this":
	case "following":
		items, err = s.lockItems(ctx, tx, `WHERE i.rule_id = $1 AND i.user_id = $2 AND (i.date >= $3 OR i.id = $4)`,
			*it.RuleID, userID, it.Date, it.ID)
	case "past":
		items, err = s.lockItems(ctx, tx, `WHERE i.rule_id = $1 AND i.user_id = $2 AND i.date <= $3`, *it.RuleID, userID, today)
	case "upcoming":
		items, err = s.lockItems(ctx, tx, `WHERE i.rule_id = $1 AND i.user_id = $2 AND i.produced_at IS NULL
			AND i.pending_id IS NULL AND i.transaction_id IS NULL`, *it.RuleID, userID)
	case "all":
		items, err = s.lockItems(ctx, tx, `WHERE i.rule_id = $1 AND i.user_id = $2`, *it.RuleID, userID)
	default:
		return nil, ErrValidation
	}
	if err != nil {
		return nil, err
	}
	var (
		ids     []uuid.UUID
		touched []uuid.UUID
	)
	for _, x := range items {
		applyToDraft(&x.Draft, &req.Fields)
		if err := checkDraftRefsTx(ctx, tx, userID, x.Draft); err != nil {
			return nil, err
		}
		var date *string
		if req.has("date") {
			d, _ := parseDate(*req.Date)
			x.Date, date = d, req.Date
		}
		if req.values() || date != nil {
			x.Edited = true
		}
		if req.AlsoUpdateLink {
			if date != nil && x.Date.After(today) && x.linked() {
				// A date after today: the real side leaves, the item is upcoming again.
				_, t, err := s.unlinkTx(ctx, tx, x)
				if err != nil {
					return nil, err
				}
				touched = append(touched, t...)
				x.ProducedAt = nil
			} else {
				t, err := s.syncLinkTx(ctx, tx, x, &req.Fields, date)
				if err != nil {
					return nil, err
				}
				touched = append(touched, t...)
			}
		}
		if err := saveItemTx(ctx, tx, x); err != nil {
			return nil, err
		}
		ids = append(ids, x.ID)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if err := s.DueCheck(ctx, userID, tz); err != nil {
		return nil, err
	}
	return s.itemsResult(ctx, userID, ids, touched)
}

// DeleteItem — DELETE /recurring-items/:id.
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
	out.Setting = s.settingOf(ctx, userID, it.RuleID)
	return out, nil
}

// CreateNow — POST /recurring-items/:id/create ("สร้างใหม่").
func (s *Service) CreateNow(ctx context.Context, userID, id uuid.UUID, req CreateNowRequest, tz string) (*ItemWriteResult, error) {
	if req.Target != "pending" && req.Target != "transaction" {
		return nil, ErrValidation
	}
	f := req.Fields
	if f == nil {
		f = &Fields{}
	}
	if err := validateFields(f); err != nil {
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
	it, err := getItem(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	if it.linked() {
		return nil, ErrAlreadyLinked
	}
	applyToDraft(&it.Draft, f)
	if f.has("date") {
		it.Date, _ = parseDate(*f.Date)
	}
	if f.values() || f.has("date") {
		it.Edited = true
	}
	if it.Date.After(today) {
		// A date after today: upcoming again, nothing made.
		it.ProducedAt, it.Archived = nil, false
		if err := saveItemTx(ctx, tx, it); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return s.itemResult(ctx, userID, id, nil, nil)
	}
	if err := s.produceTx(ctx, tx, it); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if req.Target == "pending" {
		return s.itemResult(ctx, userID, id, nil, nil)
	}
	res, err := s.pending.Submit(ctx, userID, pending.SubmitRequest{IDs: []uuid.UUID{*it.PendingID}})
	if err != nil {
		return nil, err
	}
	var row any
	if len(res.Submitted) > 0 && len(res.Submitted[0].Rows) > 0 {
		row = res.Submitted[0].Rows[0]
	}
	it2, err := s.itemView(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	return &ItemWriteResult{Item: *it2, Transaction: row, AccountBalances: res.AccountBalances, Setting: s.settingOf(ctx, userID, ruleOf(it2))}, nil
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

// Batch — POST /recurring-items/batch: one action on many items.
func (s *Service) Batch(ctx context.Context, userID uuid.UUID, req BatchRequest, tz string) (*BatchResult, error) {
	ids := uniqueIDs(req.IDs)
	if len(ids) == 0 || len(ids) > 100 {
		return nil, ErrValidation
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
	items, err := s.lockItems(ctx, tx, `WHERE i.id = ANY($1) AND i.user_id = $2`, ids, userID)
	if err != nil {
		return nil, err
	}
	if len(items) != len(ids) {
		return nil, ErrItemNotFound
	}
	out := &BatchResult{Items: []Item{}, DeletedIDs: []uuid.UUID{}}
	var (
		touched []uuid.UUID
		drafts  []uuid.UUID
	)
	switch req.Action {
	case "edit":
		if req.Fields == nil || !req.Fields.values() {
			return nil, ErrValidation
		}
		if err := validateFields(req.Fields); err != nil {
			return nil, err
		}
		for _, it := range items {
			applyToDraft(&it.Draft, req.Fields)
			if err := checkDraftRefsTx(ctx, tx, userID, it.Draft); err != nil {
				return nil, err
			}
			it.Edited = true
			if req.AlsoUpdateLink {
				t, err := s.syncLinkTx(ctx, tx, it, req.Fields, nil)
				if err != nil {
					return nil, err
				}
				touched = append(touched, t...)
			}
			if err := saveItemTx(ctx, tx, it); err != nil {
				return nil, err
			}
		}
	case "create":
		if req.Target != "pending" && req.Target != "transaction" {
			return nil, ErrValidation
		}
		var bad []uuid.UUID
		for _, it := range items {
			if it.linked() || it.Date.After(today) {
				bad = append(bad, it.ID)
			}
		}
		if len(bad) > 0 {
			return nil, &ConflictError{IDs: bad}
		}
		for _, it := range items {
			if err := s.produceTx(ctx, tx, it); err != nil {
				return nil, err
			}
			drafts = append(drafts, *it.PendingID)
		}
	case "archive", "restore":
		for _, it := range items {
			it.Archived = req.Action == "archive"
			if err := saveItemTx(ctx, tx, it); err != nil {
				return nil, err
			}
		}
	case "delete":
		for _, it := range items {
			if req.DeleteLinkedTx {
				_, t, err := s.unlinkTx(ctx, tx, it)
				if err != nil {
					return nil, err
				}
				touched = append(touched, t...)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM recurring_items WHERE id = $1`, it.ID); err != nil {
				return nil, err
			}
			out.DeletedIDs = append(out.DeletedIDs, it.ID)
		}
	default:
		return nil, ErrValidation
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if req.Action == "create" && req.Target == "transaction" && len(drafts) > 0 {
		res, err := s.pending.Submit(ctx, userID, pending.SubmitRequest{IDs: drafts})
		if err != nil {
			return nil, err
		}
		for _, b := range res.AccountBalances {
			touched = append(touched, b.AccountID)
		}
	}
	if req.Action != "delete" {
		if out.Items, err = s.itemViews(ctx, s.db, userID, today, `WHERE i.id = ANY($1) AND i.user_id = $2 ORDER BY i.date, i.id`,
			ids, userID); err != nil {
			return nil, err
		}
	}
	if out.AccountBalances, err = shared.LoadWalletBalances(ctx, s.db, uniqueIDs(touched)); err != nil {
		return nil, err
	}
	var rs []*uuid.UUID
	for _, it := range items {
		rs = append(rs, it.RuleID)
	}
	out.Setting = s.settingOf(ctx, userID, rs...)
	return out, nil
}

// --- hooks (transactions / pending) ---

func draftFromCreate(req transactions.CreateRequest) pending.Draft {
	typ := string(req.Type)
	d := pending.Draft{
		Type: &typ, Amount: ptr(req.Amount), AccountID: req.AccountID, CategoryID: req.CategoryID,
		Description: shared.CleanText(req.Description), Note: shared.CleanText(req.Note),
		TransferToAccountID: req.TransferToAccountID, TagIDs: append([]uuid.UUID(nil), req.TagIDs...), Splits: req.Splits,
	}
	return d
}

// CreateUpcomingTx — transactions.RecurringLinks: a one-off upcoming item.
func (s *Service) CreateUpcomingTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, req transactions.CreateRequest) (uuid.UUID, error) {
	date, err := parseDate(req.Date)
	if err != nil {
		return uuid.Nil, ErrValidation
	}
	d := draftFromCreate(req)
	if err := checkDraftRefsTx(ctx, tx, userID, d); err != nil {
		return uuid.Nil, err
	}
	return insertItemTx(ctx, tx, &itemRow{UserID: userID, Date: date, Draft: d})
}

// MoveToUpcomingTx — transactions.RecurringLinks: row txID moves to a
// future date; its item (if any) takes the values and is upcoming again.
func (s *Service) MoveToUpcomingTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID, req transactions.CreateRequest) (uuid.UUID, error) {
	date, err := parseDate(req.Date)
	if err != nil {
		return uuid.Nil, ErrValidation
	}
	var id uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM recurring_items WHERE transaction_id = $1 AND user_id = $2 FOR UPDATE`,
		txID, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.CreateUpcomingTx(ctx, tx, userID, req)
	}
	if err != nil {
		return uuid.Nil, err
	}
	it, err := getItem(ctx, tx, userID, id, false)
	if err != nil {
		return uuid.Nil, err
	}
	it.Draft, it.Date, it.Edited = draftFromCreate(req), date, true
	it.TransactionID, it.PendingID, it.ProducedAt, it.Archived = nil, nil, nil, false
	return id, saveItemTx(ctx, tx, it)
}

// TxEditedTx — transactions.RecurringLinks (also_update_link): the item
// linked to row takes its values.
func (s *Service) TxEditedTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, row *transactions.Transaction, tagIDs []uuid.UUID, tagsSet bool) error {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM recurring_items WHERE transaction_id = $1 AND user_id = $2 FOR UPDATE`,
		row.ID, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	it, err := getItem(ctx, tx, userID, id, false)
	if err != nil {
		return err
	}
	typ := string(row.Type)
	it.Draft.Type, it.Draft.Amount, it.Draft.AccountID, it.Draft.CategoryID = &typ, ptr(row.Amount), row.AccountID, row.CategoryID
	it.Draft.Description, it.Draft.Note = row.Description, row.Note
	if tagsSet {
		it.Draft.TagIDs = append([]uuid.UUID(nil), tagIDs...)
	}
	if d, err := parseDate(row.Date); err == nil {
		it.Date = d
	}
	it.Edited = true
	return saveItemTx(ctx, tx, it)
}

// TxDeletingTx — transactions.RecurringLinks (also_delete_link).
func (s *Service) TxDeletingTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID) (*uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `DELETE FROM recurring_items WHERE transaction_id = $1 AND user_id = $2 RETURNING id`,
		txID, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// OnDraftSubmitted — pending.SubmitHook: the item linked to the draft takes
// the new transaction (a saving draft: the saving entry).
func (s *Service) OnDraftSubmitted(ctx context.Context, tx pgx.Tx, userID, draftID, id uuid.UUID, kind string) error {
	col := "transaction_id"
	if kind == pending.KindSaving {
		col = "saving_entry_id"
	}
	_, err := tx.Exec(ctx, `UPDATE recurring_items SET `+col+` = $3, pending_id = NULL
		WHERE pending_id = $2 AND user_id = $1`, userID, draftID, id)
	return err
}
