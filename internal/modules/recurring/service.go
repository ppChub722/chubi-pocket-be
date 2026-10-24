package recurring

import (
	"context"
	"fmt"
	"math"
	"sort"
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

func (s *Service) today(ctx context.Context, userID uuid.UUID, headerTZ string) (time.Time, error) {
	prefs, err := shared.UserPrefs(ctx, s.db, userID)
	if err != nil {
		return time.Time{}, err
	}
	names := []string{headerTZ, prefs.Timezone, defaultTimezone}
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

func (s *Service) Today(ctx context.Context, userID uuid.UUID) (string, error) {
	t, err := s.today(ctx, userID, shared.HeaderTZ(ctx))
	return t.Format(dateLayout), err
}

// DueCheckDaily runs DueCheck when the user's local day has not been checked
// yet (or something it reacts to changed since): POST /sync calls it, reads
// never do. One read when there is nothing to do.
func (s *Service) DueCheckDaily(ctx context.Context, userID uuid.UUID, headerTZ string) error {
	today, err := s.today(ctx, userID, headerTZ)
	if err != nil {
		return err
	}
	var skip bool
	if err := s.db.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM recurring_due_checks c WHERE c.user_id = $1 AND c.checked_on >= $2
		        AND NOT EXISTS (SELECT 1 FROM saving_auto a WHERE a.user_id = $1 AND a.start_date <= $2 AND a.updated_at > c.checked_at))
		AND NOT EXISTS (SELECT 1 FROM recurring_rules r WHERE r.user_id = $1 AND r.status = 'active'
		        AND (r.account_id IN (SELECT id FROM accounts WHERE status = 'archived')
		          OR r.to_account_id IN (SELECT id FROM accounts WHERE status = 'archived')))`,
		userID, today).Scan(&skip); err != nil {
		return fmt.Errorf("due check guard: %w", err)
	}
	if skip {
		return nil
	}
	return s.DueCheck(ctx, userID, headerTZ)
}

// DueCheck makes what came due since the last check real. Nothing is produced
// into pending any more: an occurrence that came due is just a computed `due`
// state. What it does: pause settings whose wallet was archived, record the
// transactions of the settings with auto-create on, count the newly due
// occurrences for ONE grouped notice, tell about settings that are about to
// end, and make the goal auto-save drafts. It writes, so it runs from
// DueCheckDaily and after a recurring write, never on a read.
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

	if _, err := tx.Exec(ctx, `UPDATE recurring_rules SET status = 'paused', paused_from = $2
		WHERE user_id = $1 AND status = 'active'
		  AND (account_id IN (SELECT id FROM accounts WHERE status = 'archived')
		    OR to_account_id IN (SELECT id FROM accounts WHERE status = 'archived'))`, userID, today); err != nil {
		return fmt.Errorf("auto-pause: %w", err)
	}

	rows, err := tx.Query(ctx, `SELECT `+ruleColumns+` FROM recurring_rules WHERE user_id = $1 AND status = 'active'
		ORDER BY created_at, id FOR UPDATE SKIP LOCKED`, userID)
	if err != nil {
		return fmt.Errorf("due rules: %w", err)
	}
	var rules []*Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			rows.Close()
			return err
		}
		rules = append(rules, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	lang := s.lang(ctx, userID)
	newlyDue, autoMade := 0, 0
	var ends []ending
	for _, r := range rules {
		made, err := s.autoCreate(ctx, tx, r, today, lang)
		if err != nil {
			return err
		}
		autoMade += made
		n, err := s.newlyDue(ctx, tx, r, today, lang)
		if err != nil {
			return err
		}
		newlyDue += n
		if e, ok, err := s.endingOf(ctx, tx, r, today, lang); err != nil {
			return err
		} else if ok {
			ends = append(ends, e)
		}
	}
	if err := s.produceAutoSaves(ctx, tx, userID, today); err != nil {
		return err
	}
	if s.notifs != nil {
		if newlyDue > 0 {
			if err := s.notifs.RecurringDueTx(ctx, tx, userID, newlyDue); err != nil {
				return err
			}
		}
		if autoMade > 0 {
			if err := s.notifs.RecurringAutoCreatedTx(ctx, tx, userID, autoMade, today.Format(dateLayout)); err != nil {
				return err
			}
		}
		for _, e := range ends {
			if err := s.notifs.RecurringEndingTx(ctx, tx, userID, notifications.RecurringEndingPayload{
				RecurringID: e.id, Name: e.name, Left: e.left, StartDate: e.start,
				End: notifications.RecurringEnd{Kind: e.endKind, Date: e.endDate, Count: e.endCount},
			}); err != nil {
				return err
			}
		}
	}
	for _, e := range ends {
		if _, err := tx.Exec(ctx, `UPDATE recurring_rules SET ending_notified = TRUE WHERE id = $1`, e.id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO recurring_due_checks (user_id, checked_on, checked_at) VALUES ($1, $2, NOW())
		ON CONFLICT (user_id) DO UPDATE SET checked_on = EXCLUDED.checked_on, checked_at = NOW()`, userID, today); err != nil {
		return err
	}
	return tx.Commit(ctx)
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

// newlyDue counts the occurrences of r that came due since the due notice last
// looked (due_notified_until, today], not recorded and not made by auto-create
// just now, and moves the marker to today.
func (s *Service) newlyDue(ctx context.Context, tx pgx.Tx, r *Rule, today time.Time, lang string) (int, error) {
	if r.DueNotifiedUntil != nil && !r.DueNotifiedUntil.Before(today) {
		return 0, nil // already looked today: no queries, no write
	}
	from := r.TrackedFrom
	if r.DueNotifiedUntil != nil && r.DueNotifiedUntil.AddDate(0, 0, 1).After(from) {
		from = r.DueNotifiedUntil.AddDate(0, 0, 1)
	}
	n := 0
	if !from.After(today) {
		rows, err := s.storedRows(ctx, tx, r.UserID, []uuid.UUID{r.ID}, &from, &today)
		if err != nil {
			return 0, err
		}
		for _, o := range occsOf(r, rows, from, today, today, lang) {
			if o.status == StatusDue {
				n++
			}
		}
	}
	// only when it moves: a write bumps updated_at, which the sync fingerprint reads
	if _, err := tx.Exec(ctx, `UPDATE recurring_rules SET due_notified_until = $2 WHERE id = $1
		AND due_notified_until IS DISTINCT FROM $2`, r.ID, today); err != nil {
		return 0, err
	}
	r.DueNotifiedUntil = &today
	return n, nil
}

// endingOf — a setting WITH an end that has 2 or fewer occurrences left gets
// one "about to end" notice.
func (s *Service) endingOf(ctx context.Context, tx pgx.Tx, r *Rule, today time.Time, lang string) (ending, bool, error) {
	if r.EndingNotified || r.TotalCount == nil || r.GeneratedUntil == nil {
		return ending{}, false, nil
	}
	from := today.AddDate(0, 0, 1)
	if from.After(*r.GeneratedUntil) {
		return ending{}, false, nil
	}
	rows, err := s.storedRows(ctx, tx, r.UserID, []uuid.UUID{r.ID}, &from, r.GeneratedUntil)
	if err != nil {
		return ending{}, false, err
	}
	left := 0
	for _, o := range occsOf(r, rows, from, *r.GeneratedUntil, today, lang) {
		if o.status == StatusUpcoming {
			left++
		}
	}
	if left > 2 {
		return ending{}, false, nil
	}
	return ending{id: r.ID, name: r.Name, left: left, start: r.StartDate.Format(dateLayout), endKind: r.EndKind,
		endDate: dstr(r.EndDate), endCount: r.EndCount}, true, nil
}

func (s *Service) itemView(ctx context.Context, userID, itemID uuid.UUID) (*Item, error) {
	it, err := getItem(ctx, s.db, userID, itemID, false)
	if err != nil {
		return nil, err
	}
	if it.RuleID == nil {
		return nil, ErrItemNotFound
	}
	return s.viewRow(ctx, userID, it)
}

// viewRow is the Item of a stored row.
func (s *Service) viewRow(ctx context.Context, userID uuid.UUID, it *itemRow) (*Item, error) {
	today, err := s.today(ctx, userID, shared.HeaderTZ(ctx))
	if err != nil {
		return nil, err
	}
	r, err := getRule(ctx, s.db, userID, *it.RuleID, false)
	if err != nil {
		return nil, err
	}
	o := rowOcc(r, it, seqIndex(r), today, s.lang(ctx, userID))
	items, err := s.viewOccs(ctx, s.db, userID, today, []*occ{o})
	if err != nil {
		return nil, err
	}
	return &items[0], nil
}

// slotItem is the Item of the occurrence of a rule at slot (stored, else computed).
func (s *Service) slotItem(ctx context.Context, userID, ruleID uuid.UUID, slot time.Time) (*Item, error) {
	today, err := s.today(ctx, userID, shared.HeaderTZ(ctx))
	if err != nil {
		return nil, err
	}
	r, err := getRule(ctx, s.db, userID, ruleID, false)
	if err != nil {
		return nil, err
	}
	o, err := s.loadOcc(ctx, s.db, userID, r, slot, today)
	if err != nil {
		return nil, err
	}
	items, err := s.viewOccs(ctx, s.db, userID, today, []*occ{o})
	if err != nil {
		return nil, err
	}
	return &items[0], nil
}

// ruleAgg is what a setting's row shows, worked out from its occurrences.
type ruleAgg struct {
	counts   StatusCounts
	done     int // recorded + paid + the implicit paid ones
	dueOccs  []*occ
	next     *occ
	edited   bool
	txCount  int
	chain    []ChainStep
	upcoming int
}

const horizonDays = 400

// aggregate — counts, the oldest due, the next upcoming, the chain.
func (s *Service) aggregate(r *Rule, stored []*itemRow, today time.Time, lang string) *ruleAgg {
	a := &ruleAgg{}
	for _, it := range stored {
		switch statusOfRow(it, today) {
		case StatusDone:
			a.counts.Done++
			a.done++
		case StatusPaid:
			a.counts.Paid++
			a.done++
		case StatusSkipped:
			a.counts.Skipped++
		case StatusPending:
			a.counts.Pending++
		}
		if it.Edited {
			a.edited = true
		}
		if it.TransactionID != nil {
			a.txCount++
		}
	}
	var all []*occ
	if r.TotalCount != nil && r.GeneratedUntil != nil { // an end: every occurrence
		all = occsOf(r, stored, r.StartDate, *r.GeneratedUntil, today, lang)
	} else {
		from := r.TrackedFrom
		hi := today.AddDate(0, 0, horizonDays)
		all = occsOf(r, stored, from, hi, today, lang)
	}
	implicitPaid := 0
	for _, o := range all {
		if r.Status != "active" && o.expected() {
			continue // a paused or archived setting has nothing due or coming
		}
		switch o.status {
		case StatusDue:
			a.counts.Due++
			a.dueOccs = append(a.dueOccs, o)
		case StatusUpcoming:
			a.upcoming++
			if a.next == nil {
				a.next = o
			}
		case StatusPaid:
			if !o.stored() {
				implicitPaid++
			}
		}
	}
	if r.TotalCount != nil {
		a.counts.Upcoming = a.upcoming // only a setting with an end counts them
		a.done += implicitPaid
		if len(all) <= 60 {
			for _, o := range all {
				a.chain = append(a.chain, ChainStep{Slot: o.slot.Format(dateLayout), Status: o.status})
			}
		}
	}
	a.counts.NotCreated, a.counts.Archived = a.counts.Due, a.counts.Skipped
	return a
}

func (s *Service) recurringViews(ctx context.Context, userID uuid.UUID, today time.Time, rules []*Rule) ([]Recurring, error) {
	out := make([]Recurring, 0, len(rules))
	if len(rules) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(rules))
	var accIDs, catIDs, tagIDs, goalIDs []uuid.UUID
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
		if r.GoalID != nil {
			goalIDs = append(goalIDs, *r.GoalID)
		}
		tagIDs = append(tagIDs, r.TagIDs...)
	}
	stored, err := s.storedRows(ctx, s.db, userID, ids, nil, nil)
	if err != nil {
		return nil, err
	}
	byRule := map[uuid.UUID][]*itemRow{}
	for _, it := range stored {
		if it.RuleID != nil {
			byRule[*it.RuleID] = append(byRule[*it.RuleID], it)
		}
	}
	avg := map[uuid.UUID]float64{}
	arows, err := s.db.Query(ctx, `SELECT i.rule_id, AVG(t.amount)::float8 FROM recurring_items i
		JOIN transactions t ON t.id = i.transaction_id WHERE i.user_id = $1 AND i.rule_id = ANY($2) GROUP BY i.rule_id`, userID, ids)
	if err != nil {
		return nil, fmt.Errorf("avg amounts: %w", err)
	}
	for arows.Next() {
		var (
			id uuid.UUID
			v  float64
		)
		if err := arows.Scan(&id, &v); err != nil {
			arows.Close()
			return nil, err
		}
		avg[id] = math.Round(v*100) / 100
	}
	arows.Close()
	accs, err := shared.LoadAccountRefs(ctx, s.db, accIDs)
	if err != nil {
		return nil, err
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
	lang := s.lang(ctx, userID)
	for _, r := range rules {
		a := s.aggregate(r, byRule[r.ID], today, lang)
		v := Recurring{
			ID: r.ID, Name: r.Name, Description: r.Description, Note: r.Note, IconCode: r.IconCode,
			Type: r.Type, Amount: amountPtr(r.Amount), Tags: []shared.TagRef{}, Rule: r.apiRule(),
			Kind: r.Kind, TrialUntil: dstr(r.TrialUntil), YearlyCost: yearlyCost(r, today), MonthlyAmount: monthlyAmount(r, today),
			SavedCount: a.counts.Done, PendingCount: a.counts.Pending,
			StartDate: r.StartDate.Format(dateLayout), End: r.apiEnd(), Status: r.Status,
			Progress: Progress{Done: a.done, Total: r.TotalCount}, Counts: a.counts, EditedAny: a.edited,
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
			DueCount: a.counts.Due, Chain: a.chain, AutoCreate: r.AutoCreate, RecordedTxCount: a.txCount,
		}
		if a.chain == nil {
			v.Chain = []ChainStep{}
		}
		if m, ok := avg[r.ID]; ok {
			v.AvgAmount = &m
		} else {
			v.AvgAmount = amountPtr(r.Amount)
		}
		if a.next != nil {
			v.NextAmount = amountPtrFrom(a.next.draft.Amount)
			ns := a.next.date.Format(dateLayout)
			d := int(a.next.date.Sub(today).Hours() / 24)
			v.NextDate, v.DaysUntil = &ns, &d
		}
		cur := a.next
		if len(a.dueOccs) > 0 {
			cur = a.dueOccs[0] // the oldest due one first
		}
		if cur != nil && int(cur.date.Sub(today).Hours()/24) <= windowDays(r) {
			oc := &Occurrence{RuleID: r.ID, Slot: cur.slot.Format(dateLayout), Date: cur.date.Format(dateLayout),
				DaysUntil: int(cur.date.Sub(today).Hours() / 24), Amount: amountPtrFrom(cur.draft.Amount), Status: cur.status}
			if cur.row != nil {
				id := cur.row.ID
				oc.ItemID = &id
			}
			v.CurrentOccurrence = oc
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
		v.EndingSoon = r.Status == "active" && r.TotalCount != nil && a.upcoming <= 2
		v.Actions = Actions{
			Pause: r.Status == "active", Resume: r.Status == "paused",
			Archive: r.Status != "archived", Restore: r.Status == "archived",
			Extend: r.Status != "archived" && r.Freq != "dates" && r.TotalCount != nil,
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
	stored, err := s.storedRows(ctx, s.db, userID, []uuid.UUID{r.ID}, nil, nil)
	if err != nil {
		return nil, err
	}
	items, err := s.ruleItemsOf(ctx, userID, r, stored, today, "produced", page, perPage, nil, 0)
	if err != nil {
		return nil, err
	}
	up, err := s.ruleItemsOf(ctx, userID, r, stored, today, "upcoming", 1, perPage, nil, 0)
	if err != nil {
		return nil, err
	}
	out := &Detail{Recurring: views[0], Items: *items, Upcoming: *up}
	if out.Stats, err = s.stats(ctx, userID, r, stored, today); err != nil {
		return nil, err
	}
	if out.AccountBalances, err = shared.LoadWalletBalances(ctx, s.db, uniqueIDs(touched)); err != nil {
		return nil, err
	}
	return out, nil
}

// stats — the detail page's numbers, from the recorded occurrences.
func (s *Service) stats(ctx context.Context, userID uuid.UUID, r *Rule, stored []*itemRow, today time.Time) (Stats, error) {
	st := Stats{Series: []Point{}}
	rows, err := s.db.Query(ctx, `SELECT COALESCE(i.slot, i.date), i.date, t.amount::float8 FROM recurring_items i
		JOIN transactions t ON t.id = i.transaction_id
		WHERE i.user_id = $1 AND i.rule_id = $2 ORDER BY COALESCE(i.slot, i.date)`, userID, r.ID)
	if err != nil {
		return st, fmt.Errorf("stats: %w", err)
	}
	var pts []Point
	for rows.Next() {
		var (
			slot, date time.Time
			amt        float64
		)
		if err := rows.Scan(&slot, &date, &amt); err != nil {
			rows.Close()
			return st, err
		}
		pts = append(pts, Point{Slot: slot.Format(dateLayout), Date: date.Format(dateLayout), Amount: amt})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}
	st.PaidCount = len(pts)
	var max float64
	for _, p := range pts {
		st.PaidTotal += p.Amount
		if p.Amount > max {
			max = p.Amount
		}
	}
	st.PaidTotal = math.Round(st.PaidTotal*100) / 100
	if n := len(pts); n > 0 {
		avg := math.Round(st.PaidTotal/float64(n)*100) / 100
		last, mx := pts[n-1].Amount, max
		st.AvgAmount, st.LastAmount, st.MaxAmount = &avg, &last, &mx
		lp := pts[n-1].Slot
		st.LastPeriod = &lp
	}
	if len(pts) > 24 {
		pts = pts[len(pts)-24:]
	}
	st.Series = append(st.Series, pts...)
	if r.TotalCount != nil && r.GeneratedUntil != nil {
		left := 0.0
		for _, o := range occsOf(r, stored, r.StartDate, *r.GeneratedUntil, today, s.lang(ctx, userID)) {
			if o.expected() && o.draft.Amount != nil {
				left += *o.draft.Amount
			}
		}
		left = math.Round(left*100) / 100
		st.RemainingTotal = &left
	}
	return st, nil
}

const unlimitedPerPage = 100000

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

// pageOccs slices occurrences into a page and builds their views.
func (s *Service) pageOccs(ctx context.Context, userID uuid.UUID, today time.Time, occs []*occ, page, perPage int) (*ItemList, error) {
	total := len(occs)
	lo, hi := (page-1)*perPage, page*perPage
	if lo > total {
		lo = total
	}
	if hi > total {
		hi = total
	}
	items, err := s.viewOccs(ctx, s.db, userID, today, occs[lo:hi])
	if err != nil {
		return nil, err
	}
	return &ItemList{Data: items, Pagination: shared.NewPage(page, perPage, total)}, nil
}

// ruleItems — one setting's items. tab produced = the history (recorded, paid,
// skipped, legacy pending, and the ones due, newest first); tab upcoming = what
// is still to come (oldest first): a setting WITHOUT an end shows `months`
// months from `from` (default today, 12), one with an end shows all of it.
func (s *Service) ruleItems(ctx context.Context, userID uuid.UUID, r *Rule, today time.Time, tab string, page, perPage int, from *time.Time, months int) (*ItemList, error) {
	stored, err := s.storedRows(ctx, s.db, userID, []uuid.UUID{r.ID}, nil, nil)
	if err != nil {
		return nil, err
	}
	return s.ruleItemsOf(ctx, userID, r, stored, today, tab, page, perPage, from, months)
}

// ruleItemsOf is ruleItems over the rule's stored rows, already loaded.
func (s *Service) ruleItemsOf(ctx context.Context, userID uuid.UUID, r *Rule, stored []*itemRow, today time.Time, tab string, page, perPage int, from *time.Time, months int) (*ItemList, error) {
	page, perPage = clampPage(page, perPage)
	lang := s.lang(ctx, userID)
	if tab != "upcoming" {
		var occs []*occ
		seqs := seqIndex(r)
		for _, it := range stored {
			if st := statusOfRow(it, today); st != StatusUpcoming {
				occs = append(occs, rowOcc(r, it, seqs, today, lang))
			}
		}
		if r.Status == "active" && !r.TrackedFrom.After(today) {
			for _, o := range occsOf(r, stored, r.TrackedFrom, today, today, lang) {
				if !o.stored() && o.status == StatusDue {
					occs = append(occs, o)
				}
			}
		}
		sort.SliceStable(occs, func(i, j int) bool { return occs[i].date.After(occs[j].date) })
		return s.pageOccs(ctx, userID, today, occs, page, perPage)
	}
	if r.TotalCount != nil && r.GeneratedUntil != nil { // an end: all of it
		var occs []*occ
		for _, o := range occsOf(r, stored, r.StartDate, *r.GeneratedUntil, today, lang) {
			if o.status == StatusUpcoming {
				occs = append(occs, o)
			}
		}
		return s.pageOccs(ctx, userID, today, occs, page, perPage)
	}
	start := today.AddDate(0, 0, 1)
	if from != nil {
		start = *from
	}
	if months <= 0 {
		months = 12
	}
	if months > 24 {
		months = 24
	}
	end := start.AddDate(0, months, -1)
	var occs []*occ
	for _, o := range occsOf(r, stored, start, end, today, lang) {
		if o.status == StatusUpcoming {
			occs = append(occs, o)
		}
	}
	items, err := s.viewOccs(ctx, s.db, userID, today, occs)
	if err != nil {
		return nil, err
	}
	out := &ItemList{Data: items, Pagination: shared.NewPage(1, len(items)+1, len(items))}
	w := &Window{From: start.Format(dateLayout), To: end.Format(dateLayout)}
	if r.Status == "active" && (r.TotalCount == nil) {
		nf := end.AddDate(0, 0, 1).Format(dateLayout)
		w.NextFrom = &nf
	}
	out.Window = w
	return out, nil
}

func (s *Service) List(ctx context.Context, userID uuid.UUID, status, q, forType string, hint PickerHint, kind string, page, perPage int, tz string) (*RecurringList, error) {
	return s.list(ctx, userID, status, q, forType, hint, kind, page, perPage, tz, false)
}

// ListAll — every rule in one page (the sync dataset); no 100-row cap.
func (s *Service) ListAll(ctx context.Context, userID uuid.UUID, tz string) (*RecurringList, error) {
	return s.list(ctx, userID, "all", "", "", PickerHint{}, "", 1, unlimitedPerPage, tz, true)
}

func (s *Service) list(ctx context.Context, userID uuid.UUID, status, q, forType string, hint PickerHint, kind string, page, perPage int, tz string, unlimited bool) (*RecurringList, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	if !unlimited {
		page, perPage = clampPage(page, perPage)
	}
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
	if kind != "" {
		args = append(args, kind)
		cond += fmt.Sprintf(` AND kind = $%d`, len(args))
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
	rows, err := s.db.Query(ctx, `SELECT `+ruleColumns+` FROM recurring_rules WHERE user_id = $1 AND `+cond, args...)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	views, err := s.recurringViews(ctx, userID, today, rules)
	if err != nil {
		return nil, err
	}
	likely := map[uuid.UUID]bool{}
	if forType != "" {
		for i := range views {
			amt := rules[i].Amount
			views[i].Likely = hint.likely(rules[i].CategoryID, &amt)
			likely[views[i].ID] = views[i].Likely
		}
	}
	// the picker: likely first; then the nearest due first (overdue, then the next
	// one coming, none last), then by name.
	sort.SliceStable(views, func(i, j int) bool {
		if forType != "" && likely[views[i].ID] != likely[views[j].ID] {
			return likely[views[i].ID]
		}
		ki, kj := dueKey(&views[i]), dueKey(&views[j])
		if ki != kj {
			return ki < kj
		}
		return strings.ToLower(views[i].Name) < strings.ToLower(views[j].Name)
	})
	total := len(views)
	if !unlimited {
		lo, hi := (page-1)*perPage, page*perPage
		if lo > total {
			lo = total
		}
		if hi > total {
			hi = total
		}
		views = views[lo:hi]
	}
	out.Data = views
	out.Pagination = shared.NewPage(page, perPage, total)
	return out, nil
}

// dueKey orders settings: overdue first (oldest first), then by the next date,
// settings with nothing coming last.
func dueKey(v *Recurring) string {
	if v.CurrentOccurrence != nil {
		if v.CurrentOccurrence.Status == StatusDue {
			return "0" + v.CurrentOccurrence.Date
		}
		return "1" + v.CurrentOccurrence.Date
	}
	if v.NextDate != nil {
		return "1" + *v.NextDate
	}
	return "2"
}

func (s *Service) Get(ctx context.Context, userID, id uuid.UUID, page, perPage int, tz string, withDeletePreview bool) (*Detail, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	d, err := s.detail(ctx, userID, id, today, page, perPage, nil)
	if err != nil || !withDeletePreview {
		return d, err
	}
	p := &DeletePreview{}
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE transaction_id IS NOT NULL), COUNT(*) FILTER (WHERE pending_id IS NOT NULL)
		FROM recurring_items WHERE rule_id = $1 AND user_id = $2`, id, userID).Scan(&p.Items, &p.LinkedTransactions, &p.PendingDrafts); err != nil {
		return nil, err
	}
	d.DeletePreview = p
	return d, nil
}

func (s *Service) RuleItems(ctx context.Context, userID, id uuid.UUID, tab string, page, perPage int, from *time.Time, months int, tz string) (*ItemList, error) {
	r, err := getRule(ctx, s.db, userID, id, false)
	if err != nil {
		return nil, err
	}
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	return s.ruleItems(ctx, userID, r, today, tab, page, perPage, from, months)
}

// Items lists occurrences across the settings. status: upcoming (default, the
// next 120 days) | due (overdue + due today + this month's upcoming ones with
// no amount yet, flagged needs_amount) | done | paid | skipped | pending
// (stored rows, newest first) | all (needs a month). month = every occurrence
// dated in it, oldest first.
func (s *Service) Items(ctx context.Context, userID uuid.UUID, status, kind, month string, ruleID *uuid.UUID, page, perPage int, tz string) (*ItemList, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	page, perPage = clampPage(page, perPage)
	switch status {
	case StatusNotCreated:
		status = StatusDue
	case StatusArchived:
		status = StatusSkipped
	}
	rows, err := s.db.Query(ctx, `SELECT `+ruleColumns+` FROM recurring_rules WHERE user_id = $1
		AND ($2::uuid IS NULL OR id = $2)`, userID, ruleID)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if ruleID != nil && len(rules) == 0 {
		return nil, ErrNotFound
	}
	var occs []*occ
	if month != "" {
		first, err := time.Parse("2006-01", month)
		if err != nil {
			return nil, ErrValidation
		}
		if occs, err = s.occsInRange(ctx, s.db, userID, rules, first, first.AddDate(0, 1, -1), today, false); err != nil {
			return nil, err
		}
		if perPage == 20 {
			perPage = 100
		}
		occs = filterStatus(occs, status)
		return s.pageOccs(ctx, userID, today, occs, page, perPage)
	}
	switch status {
	case StatusDone, StatusPaid, StatusSkipped, StatusPending:
		ids := make([]uuid.UUID, len(rules))
		byID := map[uuid.UUID]*Rule{}
		for i, r := range rules {
			ids[i], byID[r.ID] = r.ID, r
		}
		stored, err := s.storedRows(ctx, s.db, userID, ids, nil, nil)
		if err != nil {
			return nil, err
		}
		lang := s.lang(ctx, userID)
		for _, it := range stored {
			if statusOfRow(it, today) == status && it.RuleID != nil {
				r := byID[*it.RuleID]
				occs = append(occs, rowOcc(r, it, seqIndex(r), today, lang))
			}
		}
		sort.SliceStable(occs, func(i, j int) bool { return occs[i].date.After(occs[j].date) })
	case StatusDue:
		rules = activeOnly(rules) // the badge counts active settings only
		lo := today
		for _, r := range rules {
			if r.TrackedFrom.Before(lo) {
				lo = r.TrackedFrom
			}
		}
		due, err := s.occsInRange(ctx, s.db, userID, rules, lo, today, today, true)
		if err != nil {
			return nil, err
		}
		occs = filterStatus(due, StatusDue)
		// this month's upcoming ones that still have no amount
		first, last := shared.MonthBounds(today)
		ahead := first
		if today.AddDate(0, 0, 1).After(ahead) {
			ahead = today.AddDate(0, 0, 1)
		}
		if !ahead.After(last) {
			up, err := s.occsInRange(ctx, s.db, userID, rules, ahead, last, today, true)
			if err != nil {
				return nil, err
			}
			for _, o := range up {
				if o.status == StatusUpcoming && (o.draft.Amount == nil || *o.draft.Amount <= 0) {
					occs = append(occs, o)
				}
			}
		}
	case "all":
		return nil, ErrValidation
	default: // upcoming
		rules = activeOnly(rules)
		up, err := s.occsInRange(ctx, s.db, userID, rules, today.AddDate(0, 0, 1), today.AddDate(0, 0, 120), today, true)
		if err != nil {
			return nil, err
		}
		occs = filterStatus(up, StatusUpcoming)
	}
	return s.pageOccs(ctx, userID, today, occs, page, perPage)
}

func activeOnly(rules []*Rule) []*Rule {
	out := make([]*Rule, 0, len(rules))
	for _, r := range rules {
		if r.Status == "active" {
			out = append(out, r)
		}
	}
	return out
}

func filterStatus(occs []*occ, status string) []*occ {
	if status == "" || status == "all" {
		return occs
	}
	out := occs[:0:0]
	for _, o := range occs {
		if o.status == status {
			out = append(out, o)
		}
	}
	return out
}

func (s *Service) GetItem(ctx context.Context, userID, id uuid.UUID) (*Item, error) {
	return s.itemView(ctx, userID, id)
}

// Occurrences is the quick-create picker's step 2: one setting's overdue
// occurrences (the 24 newest), the `past` most recent earlier ones and the next
// `upcoming` ones, each with its state and linked transaction; suggested_slot is
// the oldest overdue one, else the free one nearest txDate.
func (s *Service) Occurrences(ctx context.Context, userID, ruleID uuid.UUID, txDate *time.Time, past, upcoming int, tz string) (*OccurrencesResult, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	r, err := getRule(ctx, s.db, userID, ruleID, false)
	if err != nil {
		return nil, err
	}
	if past < 0 {
		past = 3
	}
	if upcoming < 0 {
		upcoming = 3
	}
	stored, err := s.storedRows(ctx, s.db, userID, []uuid.UUID{r.ID}, nil, nil)
	if err != nil {
		return nil, err
	}
	lang := s.lang(ctx, userID)
	var due, earlier, ahead []*occ
	lo := r.TrackedFrom
	for _, o := range occsOf(r, stored, lo, today, today, lang) {
		if o.status == StatusDue {
			due = append(due, o)
		} else {
			earlier = append(earlier, o)
		}
	}
	var oldestDue *occ
	if len(due) > 0 {
		oldestDue = due[0] // the oldest overdue one of all, before the list is cut
	}
	if len(due) > 24 {
		due = due[len(due)-24:]
	}
	if len(earlier) > past {
		earlier = earlier[len(earlier)-past:]
	}
	for _, o := range occsOf(r, stored, today.AddDate(0, 0, 1), today.AddDate(0, 0, horizonDays), today, lang) {
		if len(ahead) < upcoming {
			ahead = append(ahead, o)
		}
	}
	all := append(append(append([]*occ{}, earlier...), due...), ahead...)
	sort.SliceStable(all, func(i, j int) bool { return all[i].date.Before(all[j].date) }) // oldest first
	items, err := s.viewOccs(ctx, s.db, userID, today, all)
	if err != nil {
		return nil, err
	}
	out := &OccurrencesResult{Data: items}
	switch {
	case oldestDue != nil:
		sl := oldestDue.slot.Format(dateLayout)
		out.SuggestedSlot = &sl
	case txDate != nil:
		var best *occ
		for _, o := range all {
			if o.status != StatusUpcoming && o.status != StatusDue {
				continue
			}
			if best == nil || absDays(o.date, *txDate) < absDays(best.date, *txDate) {
				best = o
			}
		}
		if best != nil {
			sl := best.slot.Format(dateLayout)
			out.SuggestedSlot = &sl
		}
	}
	return out, nil
}

func absDays(a, b time.Time) int {
	d := int(a.Sub(b).Hours() / 24)
	if d < 0 {
		return -d
	}
	return d
}

type OccurrencesResult struct {
	Data          []Item  `json:"data"`
	SuggestedSlot *string `json:"suggested_slot"`
}

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

// settingOf — the setting view the items of a write belong to (no re-fetch after a write): nil
// unless every given rule id is the same one.
func (s *Service) settingOf(ctx context.Context, userID uuid.UUID, ruleIDs ...*uuid.UUID) *Recurring {
	var id *uuid.UUID
	for _, r := range ruleIDs {
		if r == nil || (id != nil && *id != *r) {
			return nil
		}
		id = r
	}
	if id == nil {
		return nil
	}
	today, err := s.today(ctx, userID, "")
	if err != nil {
		return nil
	}
	r, err := getRule(ctx, s.db, userID, *id, false)
	if err != nil {
		return nil
	}
	views, err := s.recurringViews(ctx, userID, today, []*Rule{r})
	if err != nil || len(views) == 0 {
		return nil
	}
	return &views[0]
}

func ruleOf(it *Item) *uuid.UUID {
	if it.Recurring == nil {
		return nil
	}
	return &it.Recurring.ID
}
