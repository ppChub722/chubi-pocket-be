package recurring

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/saving_goals"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

const defaultTimezone = "Asia/Bangkok"

type Service struct {
	db      *pgxpool.Pool
	txs     *transactions.Service
	pending *pending.Service
	notifs  *notifications.Service
	savings *saving_goals.Service
}

func NewService(db *pgxpool.Pool, txs *transactions.Service, p *pending.Service, n *notifications.Service) *Service {
	return &Service{db: db, txs: txs, pending: p, notifs: n}
}

// --- today ---

// today — the user's calendar date: X-Timezone header → preference →
// Asia/Bangkok.
func (s *Service) today(ctx context.Context, userID uuid.UUID, headerTZ string) (time.Time, error) {
	var stored *string
	err := s.db.QueryRow(ctx, `SELECT preferences->>'timezone' FROM user_preferences WHERE user_id = $1`, userID).Scan(&stored)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, err
	}
	names := []string{headerTZ}
	if stored != nil {
		names = append(names, *stored)
	}
	names = append(names, defaultTimezone)
	loc := time.UTC
	for _, n := range names {
		if n == "" {
			continue
		}
		if l, err := time.LoadLocation(n); err == nil {
			loc = l
			break
		}
	}
	now := time.Now().In(loc)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC), nil
}

// Today — transactions.RecurringLinks.
func (s *Service) Today(ctx context.Context, userID uuid.UUID) (string, error) {
	t, err := s.today(ctx, userID, "")
	return t.Format(dateLayout), err
}

// --- generation ---

// draftFromRule — the values an item of r starts with.
func draftFromRule(r *Rule) pending.Draft {
	d := pending.Draft{
		Type: ptr(r.Type), Amount: amountPtr(r.Amount), AccountID: r.AccountID, CategoryID: r.CategoryID,
		Description: r.Description, Note: r.Note, TagIDs: append([]uuid.UUID(nil), r.TagIDs...),
	}
	if d.Description == nil || strings.TrimSpace(*d.Description) == "" {
		d.Description = ptr(r.Name)
	}
	switch r.Type {
	case "transfer":
		d.TransferToAccountID = r.ToAccountID
		d.CategoryID = nil
	case typeSaving:
		d.GoalID, d.CategoryID, d.TagIDs = r.GoalID, nil, nil
	}
	return d
}

// generateTx writes the rule's items dated `from` or later, up to its end /
// horizon (idempotent per date).
func (s *Service) generateTx(ctx context.Context, tx pgx.Tx, r *Rule, from time.Time) error {
	if r.GeneratedUntil == nil {
		return nil
	}
	limit := 0
	if r.EndKind == "count" && r.EndCount != nil {
		limit = *r.EndCount
	}
	for i, d := range r.occurrences(*r.GeneratedUntil, limit) {
		if d.Before(from) {
			continue
		}
		seq, slot := i+1, d
		if _, err := insertItemTx(ctx, tx, &itemRow{
			UserID: r.UserID, RuleID: &r.ID, Seq: &seq, Slot: &slot, Date: d, Draft: draftFromRule(r),
		}); err != nil {
			return err
		}
	}
	return nil
}

// dropUnproducedTx removes a rule's items that were never produced (paused /
// rebuilt settings produce nothing). Archived and manual items stay.
func dropUnproducedTx(ctx context.Context, tx pgx.Tx, ruleID uuid.UUID) error {
	_, err := tx.Exec(ctx, `DELETE FROM recurring_items WHERE rule_id = $1 AND produced_at IS NULL
		AND pending_id IS NULL AND transaction_id IS NULL AND NOT archived AND NOT manual`, ruleID)
	return err
}

// --- the due check ---

// DueCheck produces the due items (dated today or earlier, of ACTIVE
// settings or one-off) into one pending draft each — exactly once — then
// one grouped recurring_due notice, and recurring_ending for settings with
// 2 or fewer upcoming items left. Safe on concurrent opens (row locks).
func (s *Service) DueCheck(ctx context.Context, userID uuid.UUID, headerTZ string) error {
	today, err := s.today(ctx, userID, headerTZ)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// An archived wallet pauses its settings.
	rows, err := tx.Query(ctx, `UPDATE recurring_rules SET status = 'paused'
		WHERE user_id = $1 AND status = 'active'
		  AND (account_id IN (SELECT id FROM accounts WHERE status = 'archived')
		    OR to_account_id IN (SELECT id FROM accounts WHERE status = 'archived'))
		RETURNING id`, userID)
	if err != nil {
		return fmt.Errorf("auto-pause: %w", err)
	}
	var paused []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		paused = append(paused, id)
	}
	rows.Close()
	for _, id := range paused {
		if err := dropUnproducedTx(ctx, tx, id); err != nil {
			return err
		}
	}

	rows, err = tx.Query(ctx, `SELECT i.id FROM recurring_items i
		LEFT JOIN recurring_rules r ON r.id = i.rule_id
		WHERE i.user_id = $1 AND i.produced_at IS NULL AND i.pending_id IS NULL AND i.transaction_id IS NULL
		  AND NOT i.archived AND i.date <= $2 AND (i.rule_id IS NULL OR r.status = 'active')
		ORDER BY i.date, i.id
		FOR UPDATE OF i SKIP LOCKED`, userID, today)
	if err != nil {
		return fmt.Errorf("due items: %w", err)
	}
	var due []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		due = append(due, id)
	}
	rows.Close()
	for _, id := range due {
		it, err := getItem(ctx, tx, userID, id, false)
		if err != nil {
			return err
		}
		if err := s.produceTx(ctx, tx, it); err != nil {
			return err
		}
	}
	if err := s.produceAutoSaves(ctx, tx, userID, today); err != nil {
		return err
	}
	if s.notifs != nil && len(due) > 0 {
		if err := s.notifs.RecurringDueTx(ctx, tx, userID, len(due)); err != nil {
			return err
		}
	}

	// Ending soon: 2 or fewer upcoming left, told once (extend re-arms it).
	rows, err = tx.Query(ctx, `SELECT r.id, r.name, to_char(r.start_date, 'YYYY-MM-DD'), r.end_kind, to_char(r.end_date, 'YYYY-MM-DD'), r.end_count, (SELECT COUNT(*) FROM recurring_items i
			WHERE i.rule_id = r.id AND i.produced_at IS NULL AND i.pending_id IS NULL
			  AND i.transaction_id IS NULL AND NOT i.archived)
		FROM recurring_rules r
		WHERE r.user_id = $1 AND r.status = 'active' AND NOT r.ending_notified`, userID)
	if err != nil {
		return fmt.Errorf("ending: %w", err)
	}
	type ending struct {
		id       uuid.UUID
		name     string
		left     int
		start    string
		endKind  string
		endDate  *string
		endCount *int
	}
	var ends []ending
	for rows.Next() {
		var e ending
		if err := rows.Scan(&e.id, &e.name, &e.start, &e.endKind, &e.endDate, &e.endCount, &e.left); err != nil {
			rows.Close()
			return err
		}
		if e.left <= 2 {
			ends = append(ends, e)
		}
	}
	rows.Close()
	for _, e := range ends {
		if s.notifs != nil {
			if err := s.notifs.RecurringEndingTx(ctx, tx, userID, notifications.RecurringEndingPayload{
				RecurringID: e.id, Name: e.name, Left: e.left, StartDate: e.start,
				End: notifications.RecurringEnd{Kind: e.endKind, Date: e.endDate, Count: e.endCount},
			}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE recurring_rules SET ending_notified = TRUE WHERE id = $1`, e.id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// produceTx — one item into a pending draft (its link), now.
func (s *Service) produceTx(ctx context.Context, tx pgx.Tx, it *itemRow) error {
	ref := sourceRef{ItemID: it.ID, Seq: it.Seq}
	source := "upcoming"
	if it.RuleID != nil {
		source = "recurring"
		var (
			name  string
			icon  []byte
			total *int32
		)
		if err := tx.QueryRow(ctx, `SELECT name, icon_code, total_count FROM recurring_rules WHERE id = $1`,
			*it.RuleID).Scan(&name, &icon, &total); err == nil {
			ref.Recurring = &shared.RecurringRef{ID: *it.RuleID, Name: name, IconCode: shared.IconFromJSON(icon)}
			if total != nil {
				v := int(*total)
				ref.Total = &v
			}
		}
	}
	raw, _ := json.Marshal(ref)
	d := it.Draft
	d.Date = ptr(it.Date.Format(dateLayout))
	kind := pending.KindCreate
	if d.Type != nil && *d.Type == typeSaving {
		kind = pending.KindSaving
	}
	p, err := s.pending.Store().InsertTx(ctx, tx, it.UserID, source, kind, d, raw)
	if err != nil {
		return fmt.Errorf("produce: %w", err)
	}
	now := time.Now()
	it.PendingID, it.ProducedAt, it.Archived = &p.ID, &now, false
	return saveItemTx(ctx, tx, it)
}

// --- views ---

// itemViews — page-ready items for userID (one query + the shared resolver).
func (s *Service) itemViews(ctx context.Context, q shared.Querier, userID uuid.UUID, today time.Time, where string, args ...any) ([]Item, error) {
	rows, err := q.Query(ctx, `SELECT i.id, i.rule_id, r.name, r.icon_code, i.seq, r.total_count, i.date,
			`+statusSQL+`, i.edited, i.manual, i.draft, i.pending_id, i.transaction_id, i.saving_entry_id, t.description, t.account_id,
			i.created_at, i.updated_at
		FROM recurring_items i
		LEFT JOIN recurring_rules r ON r.id = i.rule_id
		LEFT JOIN transactions t ON t.id = i.transaction_id
		`+where, args...)
	if err != nil {
		return nil, fmt.Errorf("items: %w", err)
	}
	var (
		out    []Item
		drafts []pending.Draft
		accIDs []uuid.UUID
		txAcc  = map[int]uuid.UUID{}
	)
	for rows.Next() {
		var (
			it     Item
			ruleID *uuid.UUID
			name   *string
			icon   []byte
			seq    *int32
			total  *int32
			date   time.Time
			draft  []byte
			txID   *uuid.UUID
			txDesc *string
			txAcct *uuid.UUID
		)
		if err := rows.Scan(&it.ID, &ruleID, &name, &icon, &seq, &total, &date, &it.Status, &it.Edited, &it.Manual,
			&draft, &it.PendingID, &txID, &it.SavingEntryID, &txDesc, &txAcct, &it.CreatedAt, &it.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		if ruleID != nil && name != nil {
			it.Recurring = &shared.RecurringRef{ID: *ruleID, Name: *name, IconCode: shared.IconFromJSON(icon)}
		}
		if seq != nil {
			v := int(*seq)
			it.Seq = &v
		}
		if total != nil && ruleID != nil {
			v := int(*total)
			it.Total = &v
		}
		it.Date = date.Format(dateLayout)
		it.DaysUntil = int(date.Sub(today).Hours() / 24)
		var d pending.Draft
		if err := json.Unmarshal(draft, &d); err != nil {
			rows.Close()
			return nil, err
		}
		if d.Type != nil {
			it.Type = *d.Type
		}
		if d.Amount != nil {
			it.Amount = d.Amount
		}
		it.Description, it.Note = d.Description, d.Note
		if txID != nil {
			it.Transaction = &LinkedTx{ID: *txID, Description: txDesc}
			if txAcct != nil {
				txAcc[len(out)] = *txAcct
				accIDs = append(accIDs, *txAcct)
			}
		}
		drafts = append(drafts, d)
		out = append(out, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		return []Item{}, nil
	}
	resolved, err := pending.ResolveDrafts(ctx, q, userID, drafts)
	if err != nil {
		return nil, err
	}
	accs, err := shared.LoadAccountRefs(ctx, q, accIDs)
	if err != nil {
		return nil, err
	}
	for i := range out {
		r := resolved[i]
		out[i].Account, out[i].ToAccount, out[i].Category, out[i].Project = r.Account, r.TransferToAccount, r.Category, r.Project
		out[i].Tags, out[i].Splits, out[i].Goal = r.Tags, r.Splits, r.Goal
		if out[i].Tags == nil {
			out[i].Tags = []shared.TagRef{}
		}
		if out[i].Splits == nil {
			out[i].Splits = []pending.ResolvedSplit{}
		}
		if id, ok := txAcc[i]; ok {
			if a, ok := accs[id]; ok {
				out[i].Transaction.Account = &a
			}
		}
	}
	return out, nil
}

// ItemView — transactions.RecurringLinks (after commit).
func (s *Service) ItemView(ctx context.Context, userID, itemID uuid.UUID) (any, error) {
	return s.itemView(ctx, userID, itemID)
}

func (s *Service) itemView(ctx context.Context, userID, itemID uuid.UUID) (*Item, error) {
	today, err := s.today(ctx, userID, "")
	if err != nil {
		return nil, err
	}
	items, err := s.itemViews(ctx, s.db, userID, today, `WHERE i.id = $1 AND i.user_id = $2`, itemID, userID)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrItemNotFound
	}
	return &items[0], nil
}

// recurringViews — page-ready settings (one query each for counts / refs).
func (s *Service) recurringViews(ctx context.Context, userID uuid.UUID, today time.Time, rules []*Rule) ([]Recurring, error) {
	out := make([]Recurring, 0, len(rules))
	if len(rules) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(rules))
	var accIDs, catIDs, tagIDs []uuid.UUID
	for i, r := range rules {
		ids[i] = r.ID
		for _, a := range []*uuid.UUID{r.AccountID, r.ToAccountID} {
			if a != nil {
				accIDs = append(accIDs, *a)
			}
		}
		if r.CategoryID != nil {
			catIDs = append(catIDs, *r.CategoryID)
		}
		tagIDs = append(tagIDs, r.TagIDs...)
	}
	type agg struct {
		counts StatusCounts
		done   int
		next   *time.Time
		edited bool
	}
	aggs := map[uuid.UUID]*agg{}
	rows, err := s.db.Query(ctx, `SELECT i.rule_id, `+statusSQL+`, COUNT(*),
			COUNT(*) FILTER (WHERE i.produced_at IS NOT NULL AND NOT i.manual),
			MIN(i.date) FILTER (WHERE `+statusSQL+` = 'upcoming'), BOOL_OR(i.edited)
		FROM recurring_items i WHERE i.rule_id = ANY($1) GROUP BY i.rule_id, 2`, ids)
	if err != nil {
		return nil, fmt.Errorf("rule counts: %w", err)
	}
	for rows.Next() {
		var (
			id     uuid.UUID
			status string
			n      int
			done   int
			next   *time.Time
			edited bool
		)
		if err := rows.Scan(&id, &status, &n, &done, &next, &edited); err != nil {
			rows.Close()
			return nil, err
		}
		a := aggs[id]
		if a == nil {
			a = &agg{}
			aggs[id] = a
		}
		switch status {
		case StatusUpcoming:
			a.counts.Upcoming = n
		case StatusNotCreated:
			a.counts.NotCreated = n
		case StatusPending:
			a.counts.Pending = n
		case StatusDone:
			a.counts.Done = n
		case StatusArchived:
			a.counts.Archived = n
		}
		a.done += done
		if next != nil && (a.next == nil || next.Before(*a.next)) {
			a.next = next
		}
		a.edited = a.edited || edited
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	occ, err := s.currentOccurrences(ctx, userID, today, rules)
	if err != nil {
		return nil, err
	}
	// The next occurrence's amount (the earliest upcoming item that has one).
	nextAmt := map[uuid.UUID]*float64{}
	nrows, err := s.db.Query(ctx, `SELECT DISTINCT ON (i.rule_id) i.rule_id, NULLIF((i.draft->>'amount')::numeric, 0)::float8
		FROM recurring_items i
		WHERE i.rule_id = ANY($1) AND `+statusSQL+` = 'upcoming'
		ORDER BY i.rule_id, i.date, i.id`, ids)
	if err != nil {
		return nil, fmt.Errorf("next amounts: %w", err)
	}
	for nrows.Next() {
		var (
			id  uuid.UUID
			amt *float64
		)
		if err := nrows.Scan(&id, &amt); err != nil {
			nrows.Close()
			return nil, err
		}
		nextAmt[id] = amt
	}
	nrows.Close()
	if err := nrows.Err(); err != nil {
		return nil, err
	}
	accs, err := shared.LoadAccountRefs(ctx, s.db, accIDs)
	if err != nil {
		return nil, err
	}
	var goalIDs []uuid.UUID
	for _, r := range rules {
		if r.GoalID != nil {
			goalIDs = append(goalIDs, *r.GoalID)
		}
	}
	goals, err := shared.LoadSavingGoalRefs(ctx, s.db, goalIDs)
	if err != nil {
		return nil, err
	}
	cats, err := shared.LoadCategoryRefs(ctx, s.db, catIDs)
	if err != nil {
		return nil, err
	}
	tags, err := shared.LoadTagRefs(ctx, s.db, userID, tagIDs)
	if err != nil {
		return nil, err
	}
	for _, r := range rules {
		a := aggs[r.ID]
		if a == nil {
			a = &agg{}
		}
		v := Recurring{
			ID: r.ID, Name: r.Name, Description: r.Description, Note: r.Note, IconCode: r.IconCode,
			Type: r.Type, Amount: amountPtr(r.Amount), Tags: []shared.TagRef{}, Rule: r.apiRule(),
			Kind: r.Kind, TrialUntil: dstr(r.TrialUntil), YearlyCost: yearlyCost(r, today),
			SavedCount: a.counts.Done, PendingCount: a.counts.Pending, NextAmount: nextAmt[r.ID], CurrentOccurrence: occ[r.ID],
			StartDate: r.StartDate.Format(dateLayout), End: r.apiEnd(), Status: r.Status,
			Progress: Progress{Done: a.done, Total: r.TotalCount}, Counts: a.counts, EditedAny: a.edited,
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		}
		if r.AccountID != nil {
			if x, ok := accs[*r.AccountID]; ok {
				v.Account = &x
			}
		}
		if r.ToAccountID != nil {
			if x, ok := accs[*r.ToAccountID]; ok {
				v.ToAccount = &x
			}
		}
		if r.CategoryID != nil {
			if x, ok := cats[*r.CategoryID]; ok {
				v.Category = &x
			}
		}
		if r.GoalID != nil {
			if x, ok := goals[*r.GoalID]; ok {
				v.Goal = &x
			}
		}
		for _, t := range r.TagIDs {
			if x, ok := tags[t]; ok {
				v.Tags = append(v.Tags, x)
			}
		}
		if a.next != nil {
			s := a.next.Format(dateLayout)
			d := int(a.next.Sub(today).Hours() / 24)
			v.NextDate, v.DaysUntil = &s, &d
		}
		v.EndingSoon = r.Status == "active" && a.counts.Upcoming <= 2
		v.Actions = Actions{
			Pause: r.Status == "active", Resume: r.Status == "paused",
			Archive: r.Status != "archived", Restore: r.Status == "archived",
			Extend: r.Status != "archived" && r.Freq != "dates",
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) detail(ctx context.Context, userID, id uuid.UUID, today time.Time, page, perPage int, touched []uuid.UUID) (*Detail, error) {
	r, err := getRule(ctx, s.db, userID, id, false)
	if err != nil {
		return nil, err
	}
	views, err := s.recurringViews(ctx, userID, today, []*Rule{r})
	if err != nil {
		return nil, err
	}
	items, err := s.ruleItems(ctx, userID, id, today, "produced", page, perPage)
	if err != nil {
		return nil, err
	}
	out := &Detail{Recurring: views[0], Items: *items}
	if out.AccountBalances, err = shared.LoadWalletBalances(ctx, s.db, uniqueIDs(touched)); err != nil {
		return nil, err
	}
	return out, nil
}

func clampPage(page, perPage int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if perPage <= 0 {
		perPage = 20
	}
	if perPage > 100 {
		perPage = 100
	}
	return page, perPage
}

func (s *Service) ruleItems(ctx context.Context, userID, ruleID uuid.UUID, today time.Time, tab string, page, perPage int) (*ItemList, error) {
	page, perPage = clampPage(page, perPage)
	cond, order := statusSQL+` <> 'upcoming'`, `i.date DESC, i.seq DESC NULLS LAST, i.id`
	if tab == "upcoming" {
		cond, order = statusSQL+` = 'upcoming'`, `i.date ASC, i.id`
	}
	var total int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM recurring_items i WHERE i.rule_id = $1 AND i.user_id = $2 AND `+cond,
		ruleID, userID).Scan(&total); err != nil {
		return nil, err
	}
	items, err := s.itemViews(ctx, s.db, userID, today, `WHERE i.rule_id = $1 AND i.user_id = $2 AND `+cond+
		` ORDER BY `+order+` LIMIT $3 OFFSET $4`, ruleID, userID, perPage, (page-1)*perPage)
	if err != nil {
		return nil, err
	}
	return &ItemList{Data: items, Pagination: shared.NewPage(page, perPage, total)}, nil
}

// --- reads ---

func (s *Service) List(ctx context.Context, userID uuid.UUID, status, q, forType string, hint PickerHint, page, perPage int, tz string) (*RecurringList, error) {
	if err := s.DueCheck(ctx, userID, tz); err != nil {
		return nil, err
	}
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	page, perPage = clampPage(page, perPage)
	cond := `status IN ('active', 'paused')`
	switch status {
	case "archived":
		cond = `status = 'archived'`
	case "all":
		cond = `TRUE`
	}
	args := []any{userID}
	if forType != "" { // the picker: ACTIVE settings that can take a transaction of this type
		args = append(args, forType)
		cond = fmt.Sprintf(`status = 'active' AND type = $%d`, len(args))
	}
	if q = strings.TrimSpace(q); q != "" {
		args = append(args, "%"+shared.EscapeLike(q)+"%")
		cond += fmt.Sprintf(` AND name ILIKE $%d`, len(args))
	}
	out := &RecurringList{Data: []Recurring{}}
	if out.Summary, err = s.summary(ctx, userID, today); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE status <> 'archived'),
		COUNT(*) FILTER (WHERE status = 'archived'), COUNT(*) FROM recurring_rules WHERE user_id = $1`, userID).Scan(
		&out.Counts.Current, &out.Counts.Archived, &out.Counts.All); err != nil {
		return nil, err
	}
	var total int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM recurring_rules WHERE user_id = $1 AND `+cond, args...).Scan(&total); err != nil {
		return nil, err
	}
	qargs, likelyOrder := append([]any{}, args...), ""
	if forType != "" {
		var ors []string
		if hint.CategoryID != nil {
			qargs = append(qargs, *hint.CategoryID)
			ors = append(ors, fmt.Sprintf(`r.category_id = $%d`, len(qargs)))
		}
		if hint.Amount != nil && *hint.Amount > 0 {
			qargs = append(qargs, *hint.Amount)
			ors = append(ors, fmt.Sprintf(`(r.amount > 0 AND ABS(r.amount - $%d::numeric) <= $%d::numeric * 0.05 + 0.004)`, len(qargs), len(qargs)))
		}
		if len(ors) > 0 {
			likelyOrder = `(` + strings.Join(ors, ` OR `) + `) DESC, `
		}
	}
	rows, err := s.db.Query(ctx, `SELECT `+ruleColumns+` FROM recurring_rules r WHERE user_id = $1 AND `+cond+`
		ORDER BY `+likelyOrder+`(SELECT MIN(i.date) FROM recurring_items i WHERE i.rule_id = r.id AND i.produced_at IS NULL
		          AND i.pending_id IS NULL AND i.transaction_id IS NULL AND NOT i.archived) ASC NULLS LAST,
		         LOWER(name), id
		LIMIT $`+fmt.Sprint(len(qargs)+1)+` OFFSET $`+fmt.Sprint(len(qargs)+2), append(qargs, perPage, (page-1)*perPage)...)
	if err != nil {
		return nil, err
	}
	var rules []*Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		rules = append(rules, r)
	}
	rows.Close()
	if out.Data, err = s.recurringViews(ctx, userID, today, rules); err != nil {
		return nil, err
	}
	if forType != "" {
		for i := range out.Data {
			amt := rules[i].Amount
			out.Data[i].Likely = hint.likely(rules[i].CategoryID, &amt)
		}
	}
	out.Pagination = shared.NewPage(page, perPage, total)
	return out, nil
}

func (s *Service) Get(ctx context.Context, userID, id uuid.UUID, page, perPage int, tz string) (*Detail, error) {
	if err := s.DueCheck(ctx, userID, tz); err != nil {
		return nil, err
	}
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	return s.detail(ctx, userID, id, today, page, perPage, nil)
}

func (s *Service) RuleItems(ctx context.Context, userID, id uuid.UUID, tab string, page, perPage int, tz string) (*ItemList, error) {
	if err := s.DueCheck(ctx, userID, tz); err != nil {
		return nil, err
	}
	if _, err := getRule(ctx, s.db, userID, id, false); err != nil {
		return nil, err
	}
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	return s.ruleItems(ctx, userID, id, today, tab, page, perPage)
}

// Items — GET /recurring-items.
func (s *Service) Items(ctx context.Context, userID uuid.UUID, status, kind, month string, page, perPage int, tz string) (*ItemList, error) {
	if err := s.DueCheck(ctx, userID, tz); err != nil {
		return nil, err
	}
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	page, perPage = clampPage(page, perPage)
	where := `WHERE i.user_id = $1`
	args := []any{userID}
	order := `i.date DESC, i.id`
	if month != "" {
		first, err := time.Parse("2006-01", month)
		if err != nil {
			return nil, ErrValidation
		}
		args = append(args, first, first.AddDate(0, 1, -1))
		where += fmt.Sprintf(` AND i.date BETWEEN $%d AND $%d AND (i.rule_id IS NULL OR r.status = 'active')`, len(args)-1, len(args))
		order = `i.date ASC, i.id`
		if perPage < 100 && perPage == 20 {
			perPage = 100
		}
	}
	switch status {
	case "", StatusUpcoming:
		where += ` AND ` + statusSQL + ` = 'upcoming' AND (i.rule_id IS NULL OR r.status = 'active')`
		order = `i.date ASC, i.id`
	case StatusNotCreated, StatusPending, StatusDone, StatusArchived:
		args = append(args, status)
		where += fmt.Sprintf(` AND `+statusSQL+` = $%d`, len(args))
	}
	switch kind {
	case "recurring":
		where += ` AND i.rule_id IS NOT NULL`
	case "one_off":
		where += ` AND i.rule_id IS NULL`
	}
	var total int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM recurring_items i LEFT JOIN recurring_rules r ON r.id = i.rule_id `+where,
		args...).Scan(&total); err != nil {
		return nil, err
	}
	args = append(args, perPage, (page-1)*perPage)
	items, err := s.itemViews(ctx, s.db, userID, today, where+fmt.Sprintf(` ORDER BY %s LIMIT $%d OFFSET $%d`,
		order, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, err
	}
	return &ItemList{Data: items, Pagination: shared.NewPage(page, perPage, total)}, nil
}

func (s *Service) GetItem(ctx context.Context, userID, id uuid.UUID) (*Item, error) {
	return s.itemView(ctx, userID, id)
}

// PickerHint — the transaction a recurring picker is for (all optional).
type PickerHint struct {
	CategoryID *uuid.UUID
	Amount     *float64
}

func (h PickerHint) likely(catID *uuid.UUID, amount *float64) bool {
	if h.CategoryID != nil && catID != nil && *h.CategoryID == *catID {
		return true
	}
	return h.Amount != nil && *h.Amount > 0 && amount != nil && math.Abs(*amount-*h.Amount) <= *h.Amount*0.05+0.004
}
