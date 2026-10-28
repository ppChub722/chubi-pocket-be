package recurring

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// The computed-occurrences model: a rule's occurrences are not stored. An
// occurrence is computed from the rule whenever it is asked for; a row of
// recurring_items exists only once it is recorded (a transaction, "paid",
// skipped) or the user edited it. A computed occurrence is addressed by
// rule_id + slot (its original date); a stored row keeps that slot.

// occ is one occurrence, computed (row == nil) or stored.
type occ struct {
	rule   *Rule
	row    *itemRow
	slot   time.Time
	date   time.Time // row.Date once stored (an edit may move it), else the slot
	seq    *int      // n of N, only for a setting with an end
	status string
	draft  pending.Draft
}

func (o *occ) stored() bool { return o.row != nil }

// expected: still to be recorded (counts as รอจ่าย, shows in upcoming / due).
func (o *occ) expected() bool { return o.status == StatusUpcoming || o.status == StatusDue }

func (s *Service) lang(ctx context.Context, userID uuid.UUID) string {
	prefs, err := shared.UserPrefs(ctx, s.db, userID)
	if err != nil {
		return shared.DefaultLanguage
	}
	return prefs.Language
}

// seqIndex maps a slot ("YYYY-MM-DD") to its number in the rule (1-based), only
// for a setting with an end.
func seqIndex(r *Rule) map[string]int {
	if r.TotalCount == nil || r.GeneratedUntil == nil {
		return nil
	}
	out := map[string]int{}
	for i, d := range r.Spec.Between(r.StartDate, *r.GeneratedUntil) {
		out[d.Format(dateLayout)] = i + 1
	}
	return out
}

// ruleBaseDraft is the draft a rule gives its occurrences (no description: the
// description of an occurrence is its name, set when it is recorded).
func ruleBaseDraft(r *Rule) pending.Draft {
	d := pending.Draft{
		Type: ptr(r.Type), Amount: amountPtr(r.Amount), AccountID: r.AccountID, CategoryID: r.CategoryID,
		Note: r.Note, TagIDs: append([]uuid.UUID(nil), r.TagIDs...),
	}
	switch r.Type {
	case "transfer":
		d.TransferToAccountID = r.ToAccountID
		d.CategoryID = nil
	case typeSaving:
		d.GoalID, d.CategoryID, d.TagIDs = r.GoalID, nil, nil
	case typeDebt: // the entry's transaction takes the system debt category
		d.CategoryID, d.TagIDs = nil, nil
	}
	return d
}

// occName is the name of the occurrence at slot ("Netflix ประจำเดือน ต.ค. 2026").
func occName(r *Rule, slot time.Time, seq *int, lang string) string {
	in := OccNameInput{RuleName: r.Name, Freq: r.Freq, Interval: r.Interval, Weekdays: r.Weekdays, Slot: slot, Lang: lang}
	if r.Kind == KindInstallment && r.TotalCount != nil && seq != nil {
		in.Seq, in.Total = *seq, *r.TotalCount
	}
	return OccurrenceName(in)
}

// statusOfRow is the status of a stored row as of today.
func statusOfRow(it *itemRow, today time.Time) string {
	switch {
	case it.filled():
		return StatusDone
	case it.Archived:
		return StatusSkipped
	case it.ClearedAt != nil:
		return StatusPaid
	case it.PendingID != nil:
		return StatusPending
	case !it.Date.After(today):
		return StatusDue
	}
	return StatusUpcoming
}

// computedOcc is the occurrence the rule gives at slot (nothing stored yet).
func computedOcc(r *Rule, slot time.Time, seqs map[string]int, today time.Time, lang string) *occ {
	o := &occ{rule: r, slot: slot, date: slot, draft: ruleBaseDraft(r)}
	if n, ok := seqs[slot.Format(dateLayout)]; ok {
		o.seq = &n
	}
	o.draft.Description = ptr(occName(r, slot, o.seq, lang))
	switch {
	case slot.Before(r.TrackedFrom):
		o.status = StatusPaid // before the rule existed: history, never overdue
	case !slot.After(today):
		o.status = StatusDue
	default:
		o.status = StatusUpcoming
	}
	return o
}

// rowOcc is the occurrence of a stored row. A row without a name of its own
// shows the occurrence name.
func rowOcc(r *Rule, it *itemRow, seqs map[string]int, today time.Time, lang string) *occ {
	slot := it.Date
	if it.Slot != nil {
		slot = *it.Slot
	}
	o := &occ{rule: r, row: it, slot: slot, date: it.Date, draft: it.Draft, status: statusOfRow(it, today)}
	if n, ok := seqs[slot.Format(dateLayout)]; ok {
		o.seq = &n
	}
	if o.draft.Description == nil || *o.draft.Description == "" {
		o.draft.Description = ptr(occName(r, slot, o.seq, lang))
	}
	return o
}

// storedRows loads the stored rows of the given rules; from / to (when set)
// keep the rows whose date or slot falls in [from, to].
func (s *Service) storedRows(ctx context.Context, q shared.Querier, userID uuid.UUID, ruleIDs []uuid.UUID, from, to *time.Time) ([]*itemRow, error) {
	if len(ruleIDs) == 0 {
		return nil, nil
	}
	sql := `SELECT ` + itemColumns + ` FROM recurring_items WHERE user_id = $1 AND rule_id = ANY($2)`
	args := []any{userID, ruleIDs}
	if from != nil && to != nil {
		sql += ` AND ((date BETWEEN $3 AND $4) OR (slot BETWEEN $3 AND $4))`
		args = append(args, *from, *to)
	}
	rows, err := q.Query(ctx, sql+` ORDER BY date, id`, args...)
	if err != nil {
		return nil, fmt.Errorf("stored items: %w", err)
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

// rulesByID loads rules (any status) for the given ids.
func (s *Service) rulesByID(ctx context.Context, q shared.Querier, userID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]*Rule, error) {
	out := map[uuid.UUID]*Rule{}
	ids = uniqueIDs(ids)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT `+ruleColumns+` FROM recurring_rules WHERE user_id = $1 AND id = ANY($2)`, userID, ids)
	if err != nil {
		return nil, fmt.Errorf("rules: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out[r.ID] = r
	}
	return out, rows.Err()
}

// occsOf is the occurrences of one rule whose date is in [from, to]: computed
// ones merged with the stored rows given (the rule's rows; the caller loads
// them for a range that covers [from, to]). An edited row replaces the
// computed occurrence of the same period (so a month never shows twice).
func occsOf(r *Rule, stored []*itemRow, from, to, today time.Time, lang string) []*occ {
	seqs := seqIndex(r)
	bySlot := map[string]*itemRow{}
	var shown []*itemRow
	var orphans []*itemRow
	slots := r.slotsBetween(from, to)
	inSlots := map[string]bool{}
	for _, d := range slots {
		inSlots[d.Format(dateLayout)] = true
	}
	for _, it := range stored {
		if it.Slot != nil {
			bySlot[it.Slot.Format(dateLayout)] = it
		}
		if !it.Date.Before(from) && !it.Date.After(to) {
			shown = append(shown, it)
		}
		if it.Slot != nil && !it.Manual && !inSlots[it.Slot.Format(dateLayout)] {
			orphans = append(orphans, it)
		}
	}
	replaced := map[string]bool{}
	if len(orphans) > 0 && r.Status == "active" {
		// the occurrence whose period holds the row's slot is the one it stands for
		ext := r.slotsBetween(from.AddDate(-1, 0, 0), to.AddDate(1, 0, 0))
		for _, it := range orphans {
			i := periodOf(r, ext, *it.Slot)
			if i >= 0 {
				if _, has := bySlot[ext[i].Format(dateLayout)]; !has {
					replaced[ext[i].Format(dateLayout)] = true
				}
			}
		}
	}
	var out []*occ
	for _, it := range shown {
		o := rowOcc(r, it, seqs, today, lang)
		if !listable(r, o) {
			continue
		}
		out = append(out, o)
	}
	for _, d := range slots {
		k := d.Format(dateLayout)
		if _, has := bySlot[k]; has || replaced[k] {
			continue
		}
		out = append(out, computedOcc(r, d, seqs, today, lang))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].date.Equal(out[j].date) {
			return out[i].date.Before(out[j].date)
		}
		return out[i].slot.Before(out[j].slot)
	})
	return out
}

// periodOf — the index in ext (sorted occurrence dates) of the occurrence whose
// PERIOD holds s, -1 when none. A monthly / yearly setting counts by calendar
// month / year (n months / years for an interval of n); the others by the span
// from one occurrence up to the next.
func periodOf(r *Rule, ext []time.Time, s time.Time) int {
	n := r.Interval
	if n < 1 {
		n = 1
	}
	switch r.Freq {
	case "monthly":
		ms := s.Year()*12 + int(s.Month())
		for i, c := range ext {
			mc := c.Year()*12 + int(c.Month())
			if ms >= mc && ms < mc+n {
				return i
			}
		}
		return -1
	case "yearly":
		for i, c := range ext {
			if s.Year() >= c.Year() && s.Year() < c.Year()+n {
				return i
			}
		}
		return -1
	}
	return sort.Search(len(ext), func(i int) bool { return ext[i].After(s) }) - 1
}

// slotsBetween — the computed occurrence dates of an ACTIVE rule in [from, to].
func (r *Rule) slotsBetween(from, to time.Time) []time.Time {
	if r.Status != "active" {
		return nil
	}
	return r.Spec.Between(from, to)
}

// occsInRange — the occurrences of the given rules with a date in [from, to].
// tracked clamps each rule's start to its own tracked_from (what is still to
// be recorded never goes back before it).
func (s *Service) occsInRange(ctx context.Context, q shared.Querier, userID uuid.UUID, rules []*Rule, from, to, today time.Time, tracked bool) ([]*occ, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(rules))
	for i, r := range rules {
		ids[i] = r.ID
	}
	// a stored row may sit on a slot a year either side of its date
	lo, hi := from.AddDate(-1, 0, 0), to.AddDate(1, 0, 0)
	if tracked {
		lo = to.AddDate(-1, 0, 0) // finalised below per rule; this only bounds the stored rows read
		for _, r := range rules {
			if r.TrackedFrom.AddDate(-1, 0, 0).Before(lo) {
				lo = r.TrackedFrom.AddDate(-1, 0, 0)
			}
		}
		if from.AddDate(-1, 0, 0).After(lo) {
			lo = from.AddDate(-1, 0, 0)
		}
	}
	rows, err := s.storedRows(ctx, q, userID, ids, &lo, &hi)
	if err != nil {
		return nil, err
	}
	byRule := map[uuid.UUID][]*itemRow{}
	for _, it := range rows {
		byRule[it.RuleID] = append(byRule[it.RuleID], it)
	}
	lang := s.lang(ctx, userID)
	var out []*occ
	for _, r := range rules {
		f := from
		if tracked && r.TrackedFrom.After(f) {
			f = r.TrackedFrom
		}
		if f.After(to) {
			continue
		}
		out = append(out, occsOf(r, byRule[r.ID], f, to, today, lang)...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].date.Equal(out[j].date) {
			return out[i].date.Before(out[j].date)
		}
		return out[i].rule.Name < out[j].rule.Name
	})
	return out, nil
}

// activeRules loads the user's rules that can have something to pay: the active ones and the
// paused ones (their stored due occurrences stay due, 0.4.1-1 R7).
func (s *Service) activeRules(ctx context.Context, q shared.Querier, userID uuid.UUID) ([]*Rule, error) {
	rows, err := q.Query(ctx, `SELECT `+ruleColumns+` FROM recurring_rules WHERE user_id = $1 AND status IN ('active', 'paused') ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, fmt.Errorf("active rules: %w", err)
	}
	defer rows.Close()
	var out []*Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ExpectedBetween implements shared.Expecter: the occurrences still to be
// recorded with a date in [from, to] (budgets' รอจ่าย, the dashboard, the counts).
func (s *Service) ExpectedBetween(ctx context.Context, userID uuid.UUID, from, to time.Time) ([]shared.ExpectedOccurrence, error) {
	memo := shared.ExpectedMemoOf(ctx)
	if memo != nil {
		if occs, ok := memo.Lookup(from, to); ok {
			return occs, nil
		}
	}
	out, err := s.expectedBetween(ctx, userID, from, to)
	if err == nil && memo != nil {
		memo.Remember(from, to, out)
	}
	return out, err
}

func (s *Service) expectedBetween(ctx context.Context, userID uuid.UUID, from, to time.Time) ([]shared.ExpectedOccurrence, error) {
	today, err := s.today(ctx, userID, shared.HeaderTZ(ctx))
	if err != nil {
		return nil, err
	}
	rules, err := s.activeRules(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}
	occs, err := s.occsInRange(ctx, s.db, userID, rules, from, to, today, true)
	if err != nil {
		return nil, err
	}
	out := []shared.ExpectedOccurrence{}
	for _, o := range occs {
		if !o.expected() {
			continue
		}
		e := shared.ExpectedOccurrence{
			RuleID: o.rule.ID, RuleName: o.rule.Name, RuleIcon: o.rule.IconCode, Slot: o.slot, Date: o.date,
			AccountID: o.draft.AccountID, CategoryID: o.draft.CategoryID, TagIDs: o.draft.TagIDs,
			Seq: o.seq, Total: o.rule.TotalCount, Name: derefStr(o.draft.Description), Due: !o.date.After(today),
		}
		if o.draft.Type != nil {
			e.Type = *o.draft.Type
		}
		if o.draft.Amount != nil {
			e.Amount = *o.draft.Amount
		}
		if o.row != nil {
			id := o.row.ID
			e.ItemID = &id
		}
		out = append(out, e)
	}
	return out, nil
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// loadOcc finds ONE occurrence of a rule by its slot: the stored row, else the
// computed one (an error when the rule has no occurrence at that slot).
func (s *Service) loadOcc(ctx context.Context, q shared.Querier, userID uuid.UUID, r *Rule, slot, today time.Time) (*occ, error) {
	rows, err := s.storedRows(ctx, q, userID, []uuid.UUID{r.ID}, &slot, &slot)
	if err != nil {
		return nil, err
	}
	lang := s.lang(ctx, userID)
	seqs := seqIndex(r)
	for _, it := range rows {
		if it.Slot != nil && it.Slot.Equal(slot) {
			return rowOcc(r, it, seqs, today, lang), nil
		}
	}
	if len(r.Spec.Between(slot, slot)) > 0 {
		return computedOcc(r, slot, seqs, today, lang), nil
	}
	// a manual item has no slot of its own: it answers to its date
	for _, it := range rows {
		if it.Slot == nil && it.Manual && it.Date.Equal(slot) {
			return rowOcc(r, it, seqs, today, lang), nil
		}
	}
	return nil, ErrItemNotFound
}

// store makes sure the occurrence has a row (inserting one for a computed
// occurrence) and returns it, locked. The row's draft is a full copy WITHOUT a
// name of its own, so it keeps showing the occurrence name until the user
// sets one.
func (s *Service) store(ctx context.Context, tx pgx.Tx, o *occ) (*itemRow, error) {
	if o.row != nil {
		return getItem(ctx, tx, o.rule.UserID, o.row.ID, true)
	}
	d := o.draft
	d.Description = nil
	slot := o.slot
	it := &itemRow{UserID: o.rule.UserID, RuleID: o.rule.ID, Slot: &slot, Date: o.slot, Draft: d}
	if o.seq != nil {
		it.Seq = o.seq
	}
	id, err := insertItemTx(ctx, tx, it)
	if err != nil {
		return nil, err
	}
	if id == uuid.Nil { // someone stored it in the meantime
		var existing uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM recurring_items WHERE rule_id = $1 AND slot = $2`, o.rule.ID, slot).Scan(&existing); err != nil {
			return nil, err
		}
		return getItem(ctx, tx, o.rule.UserID, existing, true)
	}
	return getItem(ctx, tx, o.rule.UserID, id, true)
}

// filled — what a filled occurrence shows: its transaction's (or saving
// entry's) values, never its own draft (0.4.1-1 R1).
type filled struct {
	draft pending.Draft
	date  time.Time
	tx    *LinkedTx
	acc   *uuid.UUID
}

// filledValues loads the values of the transactions / saving entries that fill
// occurrences (a transfer: both wallets, no category; tags: the tx owner's).
func filledValues(ctx context.Context, q shared.Querier, txIDs, entryIDs []uuid.UUID) (map[uuid.UUID]*filled, error) {
	out := map[uuid.UUID]*filled{}
	if len(txIDs) > 0 {
		rows, err := q.Query(ctx, `SELECT t.id, t.type, t.amount::float8, t.account_id, t.category_id, t.description, t.note, t.date,
				(SELECT o.account_id FROM transactions o JOIN categories c ON c.id = o.category_id
				 WHERE o.transfer_group_id = t.transfer_group_id AND c.system_kind = 'TRANSFER_OUT' LIMIT 1),
				(SELECT i.account_id FROM transactions i LEFT JOIN categories c ON c.id = i.category_id
				 WHERE i.transfer_group_id = t.transfer_group_id AND c.system_kind IS DISTINCT FROM 'TRANSFER_OUT' LIMIT 1),
				COALESCE((SELECT array_agg(tt.tag_id ORDER BY tt.tag_id) FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id
				 WHERE tt.transaction_id = t.id AND g.user_id = t.user_id), '{}')
			FROM transactions t WHERE t.id = ANY($1)`, txIDs)
		if err != nil {
			return nil, fmt.Errorf("item transactions: %w", err)
		}
		for rows.Next() {
			var (
				id, typ  = uuid.UUID{}, ""
				f        filled
				amt      float64
				from, to *uuid.UUID
			)
			d := &f.draft
			if err := rows.Scan(&id, &typ, &amt, &d.AccountID, &d.CategoryID, &d.Description, &d.Note, &f.date, &from, &to, &d.TagIDs); err != nil {
				rows.Close()
				return nil, err
			}
			d.Type, d.Amount = &typ, &amt
			f.acc = d.AccountID
			if typ == "transfer" {
				d.AccountID, d.TransferToAccountID, d.CategoryID = from, to, nil
				f.acc = from
			}
			ds := f.date.Format(dateLayout)
			f.tx = &LinkedTx{ID: id, Description: d.Description, Amount: &amt, Date: &ds}
			out[id] = &f
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if len(entryIDs) > 0 {
		rows, err := q.Query(ctx, `SELECT id, amount::float8, account_id, note, date FROM saving_entries WHERE id = ANY($1)`, entryIDs)
		if err != nil {
			return nil, fmt.Errorf("item saving entries: %w", err)
		}
		for rows.Next() {
			var (
				id  uuid.UUID
				f   filled
				amt float64
				acc uuid.UUID
			)
			if err := rows.Scan(&id, &amt, &acc, &f.draft.Note, &f.date); err != nil {
				rows.Close()
				return nil, err
			}
			f.draft.Type, f.draft.Amount, f.draft.AccountID = ptr(typeSaving), &amt, &acc
			out[id] = &f
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// viewOccs builds the Item JSON of occurrences (drafts resolved to refs). A
// filled occurrence shows its transaction's / saving entry's values.
func (s *Service) viewOccs(ctx context.Context, q shared.Querier, userID uuid.UUID, today time.Time, occs []*occ) ([]Item, error) {
	out := make([]Item, 0, len(occs))
	if len(occs) == 0 {
		return out, nil
	}
	var txIDs, entryIDs []uuid.UUID
	for _, o := range occs {
		if o.row != nil && o.row.TransactionID != nil {
			txIDs = append(txIDs, *o.row.TransactionID)
		}
		if o.row != nil && o.row.SavingEntryID != nil {
			entryIDs = append(entryIDs, *o.row.SavingEntryID)
		}
	}
	fills, err := filledValues(ctx, q, txIDs, entryIDs)
	if err != nil {
		return nil, err
	}
	fillOf := func(o *occ) *filled {
		switch {
		case o.row == nil:
			return nil
		case o.row.TransactionID != nil:
			return fills[*o.row.TransactionID]
		case o.row.SavingEntryID != nil:
			return fills[*o.row.SavingEntryID]
		}
		return nil
	}
	drafts := make([]pending.Draft, len(occs))
	var accIDs []uuid.UUID
	for i, o := range occs {
		drafts[i] = o.draft
		if f := fillOf(o); f != nil {
			d := f.draft
			d.GoalID = o.draft.GoalID // a saving entry's goal stays the occurrence's
			if d.Description == nil || *d.Description == "" {
				d.Description = o.draft.Description
			}
			drafts[i] = d
			if f.acc != nil {
				accIDs = append(accIDs, *f.acc)
			}
		}
	}
	resolved, err := pending.ResolveDrafts(ctx, q, userID, drafts)
	if err != nil {
		return nil, err
	}
	accs, err := shared.LoadAccountRefs(ctx, q, accIDs)
	if err != nil {
		return nil, err
	}
	for i, o := range occs {
		r, d, date := resolved[i], drafts[i], o.date
		f := fillOf(o)
		if f != nil {
			date = f.date
		}
		it := Item{
			RuleID: o.rule.ID, Slot: o.slot.Format(dateLayout), Stored: o.row != nil, Status: o.status,
			Recurring: &shared.RecurringRef{ID: o.rule.ID, Name: o.rule.Name, IconCode: o.rule.IconCode, Kind: o.rule.Kind,
				PauseReason: o.rule.pauseReason()},
			Seq: o.seq, Date: date.Format(dateLayout), DaysUntil: int(date.Sub(today).Hours() / 24),
			Account: r.Account, ToAccount: r.TransferToAccount, Category: r.Category, Project: r.Project,
			Tags: r.Tags, Splits: r.Splits, GoalID: d.GoalID, Goal: r.Goal, Description: d.Description, Note: d.Note,
		}
		if o.rule.TotalCount != nil {
			t := *o.rule.TotalCount
			it.Total = &t
		}
		if it.Tags == nil {
			it.Tags = []shared.TagRef{}
		}
		if it.Splits == nil {
			it.Splits = []pending.ResolvedSplit{}
		}
		if d.Type != nil {
			it.Type = *d.Type
		}
		if d.Amount != nil && *d.Amount > 0 && o.status != StatusSkipped { // a skipped one shows no amount
			a := *d.Amount
			it.Amount = &a
		}
		it.NeedsAmount = it.Amount == nil && o.expected()
		if o.row != nil {
			id := o.row.ID
			it.ID = &id
			it.Edited, it.Manual = o.row.Edited, o.row.Manual
			it.PendingID, it.SavingEntryID = o.row.PendingID, o.row.SavingEntryID
			it.AutoCreated = o.row.AutoCreatedAt != nil
			ca, ua := o.row.CreatedAt, o.row.UpdatedAt
			it.CreatedAt, it.UpdatedAt = &ca, &ua
			if o.row.TransactionID != nil {
				lt := &LinkedTx{ID: *o.row.TransactionID}
				if f != nil && f.tx != nil {
					lt = f.tx
					if f.acc != nil {
						if a, ok := accs[*f.acc]; ok {
							lt.Account = &a
						}
					}
					it.Amount = lt.Amount // a filled one shows its amount, 0 too
				}
				it.Transaction = lt
			}
		}
		out = append(out, it)
	}
	if err := debtItemViews(ctx, q, userID, occs, out); err != nil {
		return nil, err
	}
	return out, nil
}

// listable — a stored occurrence of a setting that is not active shows only as
// history, or still due while the setting is paused (0.4.1-1 R7: the 3 overdue
// ones of an inactive setting, the due ones of a manual pause); nothing of an
// archived setting is still to pay.
func listable(r *Rule, o *occ) bool {
	if r.Status == "active" || !o.expected() {
		return true
	}
	return r.Status == "paused" && o.status == StatusDue
}
