package saving_goals

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

const (
	defaultTimezone = "Asia/Bangkok"
	daysPerMonth    = 30.44
	paceMonths      = 3
	entriesPerPage  = 20
)

type Service struct {
	store *Store
}

func NewService(store *Store) *Service { return &Service{store: store} }

func round2(f float64) float64 { return math.Round(f*100) / 100 }

func fptr(f float64) *float64 { return &f }

func dstr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := t.Format(dateLayout)
	return &v
}

func days(a, b time.Time) int { return int(math.Round(b.Sub(a).Hours() / 24)) }

func (s *Service) today(ctx context.Context, userID uuid.UUID) (time.Time, error) {
	stored, err := s.store.userTimezone(ctx, userID)
	if err != nil {
		return time.Time{}, err
	}
	loc := time.UTC
	for _, n := range []string{stored, defaultTimezone} {
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

// --- figures ---

// monthlyNets — the net saved per month (YYYY-MM) into each goal (key) and
// the spare bucket (key uuid.Nil when spare).
func (s *Service) monthlyNets(ctx context.Context, q shared.Querier, userID uuid.UUID, goalIDs []uuid.UUID, spare bool) (map[uuid.UUID]map[string]float64, error) {
	if goalIDs == nil {
		goalIDs = []uuid.UUID{}
	}
	rows, err := q.Query(ctx, `SELECT COALESCE(x.k, '00000000-0000-0000-0000-000000000000'::uuid), to_char(x.date, 'YYYY-MM'),
			SUM(x.v)::float8
		FROM (
			SELECT CASE WHEN to_bucket = 'spare' THEN NULL ELSE to_goal_id END AS k, to_bucket AS b, date, amount AS v
			FROM saving_entries WHERE user_id = $1 AND to_bucket <> 'available'
			UNION ALL
			SELECT CASE WHEN from_bucket = 'spare' THEN NULL ELSE from_goal_id END, from_bucket, date, -amount
			FROM saving_entries WHERE user_id = $1 AND from_bucket <> 'available'
		) x
		WHERE (x.b = 'spare' AND $3) OR (x.b = 'goal' AND x.k = ANY($2))
		GROUP BY 1, 2`, userID, goalIDs, spare)
	if err != nil {
		return nil, fmt.Errorf("monthly nets: %w", err)
	}
	defer rows.Close()
	out := map[uuid.UUID]map[string]float64{}
	for rows.Next() {
		var (
			k uuid.UUID
			m string
			v float64
		)
		if err := rows.Scan(&k, &m, &v); err != nil {
			return nil, err
		}
		if out[k] == nil {
			out[k] = map[string]float64{}
		}
		out[k][m] = v
	}
	return out, rows.Err()
}

func monthKey(t time.Time) string { return t.Format("2006-01") }

// firstMonth — the earliest month with an entry, else `fallback`.
func firstMonth(nets map[string]float64, fallback time.Time) time.Time {
	first := time.Date(fallback.Year(), fallback.Month(), 1, 0, 0, 0, 0, time.UTC)
	for m := range nets {
		if t, err := time.Parse("2006-01", m); err == nil && t.Before(first) {
			first = t
		}
	}
	return first
}

// history — month by month from the first entry (or `since`) to today's
// month: the balance at each month's end.
func history(nets map[string]float64, since, today time.Time, plan *float64, target float64) []HistoryPoint {
	out := []HistoryPoint{}
	cur := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	var cum float64
	for m := firstMonth(nets, since); !m.After(cur) && len(out) < 600; m = m.AddDate(0, 1, 0) {
		cum += nets[monthKey(m)]
		p := HistoryPoint{Month: monthKey(m), Cumulative: round2(cum)}
		if plan != nil {
			p.Plan = fptr(round2(math.Min(target, *plan*float64(len(out)+1))))
		}
		out = append(out, p)
	}
	return out
}

// paceOf — the net saved per month over the last 3 months, this month
// included (fewer when younger); nil when nothing was saved.
func paceOf(nets map[string]float64, since, today time.Time) *float64 {
	first := firstMonth(nets, since)
	cur := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	var (
		sum float64
		n   int
	)
	for i := 0; i < paceMonths; i++ {
		m := cur.AddDate(0, -i, 0)
		if m.Before(first) {
			break
		}
		sum += nets[monthKey(m)]
		n++
	}
	if n == 0 || sum <= 0 {
		return nil
	}
	return fptr(round2(sum / float64(n)))
}

func monthsBetween(a, b time.Time) int {
	return (b.Year()-a.Year())*12 + int(b.Month()) - int(a.Month())
}

// goalView — the page-ready goal. A goal with no target (open-ended) has
// no %, remaining, milestones, needed monthly, ETA or pace (0.4.0-1).
func goalView(g *Goal, saved float64, nets map[string]float64, today time.Time, currency string) GoalView {
	v := GoalView{
		ID: g.ID, Name: g.Name, Description: g.Description, Note: g.Note, IconCode: g.IconCode,
		TargetAmount: g.TargetAmount, Deadline: dstr(g.Deadline), State: g.Status, SavedAmount: round2(saved),
		Milestones: []Milestone{}, Currency: currency,
		ClosedAt: g.ClosedAt, CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt, Pace: "none",
	}
	if g.TargetAmount == nil {
		group := GroupOngoing
		v.Group = &group
	} else {
		target := *g.TargetAmount
		v.RemainingAmount = fptr(round2(math.Max(0, target-saved)))
		if target > 0 {
			pct := int(math.Round(saved / target * 100))
			v.ProgressPct = &pct
		}
		for _, p := range g.Milestones {
			amt := round2(target * float64(p) / 100)
			v.Milestones = append(v.Milestones, Milestone{Pct: p, Amount: amt, Reached: saved >= amt-0.004})
		}
	}
	if g.Deadline != nil {
		d := days(today, *g.Deadline)
		v.DaysRemaining = &d
	}
	active := g.Status == StateActive
	hasTarget := g.TargetAmount != nil
	reached := hasTarget && saved >= *g.TargetAmount-0.004
	if active && hasTarget && !reached {
		v.PaceMonthly = paceOf(nets, g.CreatedAt, today)
		if g.Deadline != nil && !g.Deadline.Before(today) {
			months := math.Max(1, float64(days(today, *g.Deadline))/daysPerMonth)
			v.NeededMonthly = fptr(math.Ceil(*v.RemainingAmount / months))
		}
		if v.PaceMonthly != nil {
			eta := today.AddDate(0, int(math.Ceil(*v.RemainingAmount / *v.PaceMonthly)), 0)
			v.EtaDate = dstr(&eta)
		}
	}
	switch {
	case !active, !hasTarget:
	case reached:
		v.Pace = "reached"
	case g.Deadline == nil:
	case v.EtaDate == nil:
		v.Pace = "late"
	default:
		eta, _ := time.Parse(dateLayout, *v.EtaDate)
		if eta.After(*g.Deadline) {
			v.Pace = "late"
			lm := monthsBetween(*g.Deadline, eta)
			if lm < 1 {
				lm = 1
			}
			v.LateMonths = &lm
		} else {
			v.Pace = "on_track"
		}
	}
	empty := saved < 0.005
	v.Actions = Actions{
		Save: active, Withdraw: active && !empty, Move: active && !empty, Close: active,
		// Archive with money is fine (0.4.0-1): an archived goal's money is left
		// out of every total until it is restored.
		Archive: g.Status != StateArchived, Restore: g.Status != StateActive, Delete: empty,
	}
	return v
}

// shares — a bucket's balance per wallet, largest first.
func (s *Service) shares(ctx context.Context, per map[uuid.UUID]float64) ([]WalletShare, float64, error) {
	var total float64
	ids := make([]uuid.UUID, 0, len(per))
	for id, a := range per {
		total += a
		ids = append(ids, id)
	}
	refs, err := shared.LoadAccountRefs(ctx, s.store.db, ids)
	if err != nil {
		return nil, 0, err
	}
	out := []WalletShare{}
	for id, a := range per {
		if a < 0.005 {
			continue
		}
		ws := WalletShare{Amount: round2(a)}
		if id != uuid.Nil { // uuid.Nil = money with no wallet → wallet null
			r, ok := refs[id]
			if !ok {
				r = shared.AccountRef{ID: id}
			}
			ws.Wallet = &r
		}
		if total > 0 {
			ws.SharePct = int(math.Round(a / total * 100))
		}
		out = append(out, ws)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Amount != out[j].Amount {
			return out[i].Amount > out[j].Amount
		}
		return walletKey(out[i]) < walletKey(out[j])
	})
	return out, round2(total), nil
}

func (s *Service) spare(ctx context.Context, q shared.Querier, userID uuid.UUID) (Spare, error) {
	per, err := bucketBalances(ctx, q, userID, nil)
	if err != nil {
		return Spare{}, err
	}
	by, total, err := s.shares(ctx, per)
	if err != nil {
		return Spare{}, err
	}
	return Spare{Amount: total, ByWallet: by}, nil
}

// goalViews — page-ready goals (one balances + one nets query).
func (s *Service) goalViews(ctx context.Context, userID uuid.UUID, goals []*Goal, today time.Time) ([]GoalView, error) {
	out := make([]GoalView, 0, len(goals))
	if len(goals) == 0 {
		return out, nil
	}
	currency, err := s.store.userCurrency(ctx, userID)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(goals))
	for i, g := range goals {
		ids[i] = g.ID
	}
	bals, err := shared.LoadSavingBalances(ctx, s.store.db, userID, `m.bucket = 'goal' AND m.goal_id = ANY($2)`, ids)
	if err != nil {
		return nil, err
	}
	saved := map[uuid.UUID]float64{}
	for _, b := range bals {
		saved[*b.GoalID] += b.Amount
	}
	nets, err := s.monthlyNets(ctx, s.store.db, userID, ids, false)
	if err != nil {
		return nil, err
	}
	autos, err := s.autoViews(ctx, userID, today)
	if err != nil {
		return nil, err
	}
	month := monthKey(today)
	for _, g := range goals {
		v := goalView(g, saved[g.ID], nets[g.ID], today, currency)
		v.AutoSave = autos[g.ID]
		v.MonthSaved = round2(nets[g.ID][month])
		active := v.State == StateActive
		switch {
		case !active:
		case v.AutoSave != nil:
			v.MonthPlan = fptr(v.AutoSave.Amount)
		case v.NeededMonthly != nil:
			v.MonthPlan = v.NeededMonthly
		}
		// An open-ended goal is never short this month (no month_short).
		if v.MonthPlan != nil && v.TargetAmount != nil {
			v.MonthShort = fptr(round2(math.Max(0, *v.MonthPlan-v.MonthSaved)))
		}
		switch v.Pace {
		case "reached":
			v.Status = "reached"
		case "on_track":
			v.Status = "on_track"
		case "late":
			v.Status, v.BehindMonths = "behind", v.LateMonths
		default:
			v.Status = "none"
		}
		out = append(out, v)
	}
	return out, nil
}

// --- entries ---

func (s *Service) entryViews(ctx context.Context, rows []*entryRow) ([]Entry, error) {
	out := make([]Entry, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	var accIDs, goalIDs, ids []uuid.UUID
	for _, e := range rows {
		if e.AccountID != uuid.Nil {
			accIDs = append(accIDs, e.AccountID)
		}
		ids = append(ids, e.ID)
		for _, g := range []*uuid.UUID{e.FromGoalID, e.ToGoalID} {
			if g != nil {
				goalIDs = append(goalIDs, *g)
			}
		}
	}
	accs, err := shared.LoadAccountRefs(ctx, s.store.db, accIDs)
	if err != nil {
		return nil, err
	}
	goals, err := shared.LoadSavingGoalRefs(ctx, s.store.db, goalIDs)
	if err != nil {
		return nil, err
	}
	links := map[uuid.UUID]shared.RecurringItemLink{}
	q, err := s.store.db.Query(ctx, `SELECT ri.saving_entry_id, ri.id, ri.seq, r.id, r.name, r.icon_code, r.total_count
		FROM recurring_items ri LEFT JOIN recurring_rules r ON r.id = ri.rule_id WHERE ri.saving_entry_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("entry links: %w", err)
	}
	for q.Next() {
		var (
			eid, iid uuid.UUID
			seq, tot *int32
			rid      *uuid.UUID
			name     *string
			icon     []byte
			l        shared.RecurringItemLink
			intOrNil = func(p *int32) *int {
				if p == nil {
					return nil
				}
				v := int(*p)
				return &v
			}
		)
		if err := q.Scan(&eid, &iid, &seq, &rid, &name, &icon, &tot); err != nil {
			q.Close()
			return nil, err
		}
		l.ItemID, l.Seq, l.Total = iid, intOrNil(seq), intOrNil(tot)
		if rid != nil && name != nil {
			l.Recurring = &shared.RecurringRef{ID: *rid, Name: *name, IconCode: shared.IconFromJSON(icon)}
		}
		links[eid] = l
	}
	q.Close()
	bucket := func(b string, goal *uuid.UUID, acc *shared.AccountRef) BucketRef {
		switch b {
		case BucketAvailable:
			if acc == nil {
				return BucketRef{Type: "wallet"}
			}
			name := acc.Name
			id := acc.ID
			return BucketRef{Type: "wallet", ID: &id, Name: &name, IconCode: acc.IconCode}
		case BucketSpare:
			return BucketRef{Type: BucketSpare}
		}
		r := BucketRef{Type: BucketGoal}
		if goal != nil {
			if g, ok := goals[*goal]; ok {
				id, name := g.ID, g.Name
				r.ID, r.Name, r.IconCode = &id, &name, g.IconCode
			}
		}
		return r
	}
	for _, e := range rows {
		var acc *shared.AccountRef
		if e.AccountID != uuid.Nil {
			a, ok := accs[e.AccountID]
			if !ok {
				a = shared.AccountRef{ID: e.AccountID}
			}
			acc = &a
		}
		v := Entry{
			ID: e.ID, GroupID: e.GroupID, Kind: e.Kind, Date: e.Date.Format(dateLayout), Note: e.Note, Wallet: acc,
			Amount: round2(e.Amount), From: bucket(e.FromBucket, e.FromGoalID, acc), To: bucket(e.ToBucket, e.ToGoalID, acc),
			Source: e.Source, GoalIDs: []uuid.UUID{}, CreatedAt: e.CreatedAt,
		}
		for _, g := range []*uuid.UUID{e.FromGoalID, e.ToGoalID} {
			if g != nil {
				v.GoalIDs = append(v.GoalIDs, *g)
			}
		}
		if l, ok := links[e.ID]; ok {
			v.Recurring = &l
		}
		out = append(out, v)
	}
	return out, nil
}

// entryPage — entries matching where (newest first), one page.
func (s *Service) entryPage(ctx context.Context, userID uuid.UUID, where string, page, perPage int, args ...any) (EntryList, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = entriesPerPage
	}
	var total int
	if err := s.store.db.QueryRow(ctx, `SELECT COUNT(*) FROM saving_entries WHERE user_id = $1 `+where,
		append([]any{userID}, args...)...).Scan(&total); err != nil {
		return EntryList{}, err
	}
	n := len(args)
	rows, err := s.store.listEntries(ctx, s.store.db, userID, where+fmt.Sprintf(
		` ORDER BY date DESC, created_at DESC, id LIMIT $%d OFFSET $%d`, n+2, n+3),
		append(args, perPage, (page-1)*perPage)...)
	if err != nil {
		return EntryList{}, err
	}
	data, err := s.entryViews(ctx, rows)
	if err != nil {
		return EntryList{}, err
	}
	return EntryList{Data: data, Pagination: shared.NewPage(page, perPage, total)}, nil
}

const (
	whereGoal  = `AND (from_goal_id = $2 OR to_goal_id = $2)`
	whereSpare = `AND (from_bucket = 'spare' OR to_bucket = 'spare')`
)

// Entries — GET /saving-entries.
func (s *Service) Entries(ctx context.Context, userID uuid.UUID, goalID *uuid.UUID, spare bool, accountID *uuid.UUID, page, perPage int) (*EntryList, error) {
	var (
		where string
		args  []any
	)
	switch {
	case goalID != nil:
		where, args = whereGoal, []any{*goalID}
	case spare:
		where = whereSpare
	case accountID != nil:
		where, args = `AND account_id = $2`, []any{*accountID}
	}
	out, err := s.entryPage(ctx, userID, where, page, perPage, args...)
	return &out, err
}

// --- reads ---

func (s *Service) List(ctx context.Context, userID uuid.UUID, status, sortBy string) (*GoalList, error) {
	today, err := s.today(ctx, userID)
	if err != nil {
		return nil, err
	}
	if status == "" {
		status = StateActive
	}
	order := `ORDER BY created_at, id`
	if status != StateActive {
		order = `ORDER BY COALESCE(closed_at, updated_at) DESC, id`
	}
	goals, err := s.store.listGoals(ctx, s.store.db, userID, `AND status = $2 `+order, status)
	if err != nil {
		return nil, err
	}
	out := &GoalList{Data: []GoalView{}}
	if out.Data, err = s.goalViews(ctx, userID, goals, today); err != nil {
		return nil, err
	}
	if out.Spare, err = s.spare(ctx, s.store.db, userID); err != nil {
		return nil, err
	}
	t, err := shared.LoadSavingTotals(ctx, s.store.db, userID)
	if err != nil {
		return nil, err
	}
	if out.Summary.Currency, err = s.store.userCurrency(ctx, userID); err != nil {
		return nil, err
	}
	out.Summary.Saved, out.Summary.InGoals, out.Summary.Spare = t.Saved, t.InGoals, t.Spare
	out.Summary.SavedTotal, out.Summary.Unallocated = t.Saved, t.Spare
	for _, v := range out.Data {
		// Open-ended goals stay out of the card's month numbers ("เดือนนี้ขาด"
		// and its bar): they have no plan to fall short of (0.4.0-1).
		if v.TargetAmount == nil {
			continue
		}
		out.Summary.MonthSavedTotal += v.MonthSaved
		if v.MonthPlan != nil {
			out.Summary.MonthPlanTotal += *v.MonthPlan
		}
		if v.MonthShort != nil {
			out.Summary.MonthShortTotal += *v.MonthShort
		}
	}
	out.Summary.MonthShortTotal = round2(out.Summary.MonthShortTotal)
	out.Summary.MonthPlanTotal, out.Summary.MonthSavedTotal = round2(out.Summary.MonthPlanTotal), round2(out.Summary.MonthSavedTotal)
	sortGoals(out.Data, sortBy)
	return out, nil
}

func (s *Service) Get(ctx context.Context, userID, id uuid.UUID, perPage int, withDeletePreview bool) (*GoalDetail, error) {
	today, err := s.today(ctx, userID)
	if err != nil {
		return nil, err
	}
	g, err := s.store.getGoal(ctx, s.store.db, userID, id, false)
	if err != nil {
		return nil, err
	}
	views, err := s.goalViews(ctx, userID, []*Goal{g}, today)
	if err != nil {
		return nil, err
	}
	out := &GoalDetail{Goal: views[0]}
	per, err := bucketBalances(ctx, s.store.db, userID, &g.ID)
	if err != nil {
		return nil, err
	}
	if out.ByWallet, _, err = s.shares(ctx, per); err != nil {
		return nil, err
	}
	nets, err := s.monthlyNets(ctx, s.store.db, userID, []uuid.UUID{g.ID}, false)
	if err != nil {
		return nil, err
	}
	// The plan line climbs to the target; an open-ended goal has none.
	plan, target := out.Goal.MonthPlan, 0.0
	if g.TargetAmount != nil {
		target = *g.TargetAmount
	} else {
		plan = nil
	}
	out.History = history(nets[g.ID], g.CreatedAt, today, plan, target)
	if out.Entries, err = s.entryPage(ctx, userID, whereGoal, 1, perPage, g.ID); err != nil {
		return nil, err
	}
	if withDeletePreview {
		p := &DeletePreview{SavedAmount: out.Goal.SavedAmount, CanDelete: out.Goal.SavedAmount <= 0.004}
		if err := s.store.db.QueryRow(ctx, `SELECT COUNT(*) FROM saving_entries WHERE user_id = $1 AND (from_goal_id = $2 OR to_goal_id = $2)`, userID, id).Scan(&p.Entries); err != nil {
			return nil, err
		}
		out.DeletePreview = p
	}
	return out, nil
}

// Spare — GET /saving-goals/spare.
func (s *Service) Spare(ctx context.Context, userID uuid.UUID, perPage int) (*SpareDetail, error) {
	today, err := s.today(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := &SpareDetail{}
	autos, err := s.autoViews(ctx, userID, today)
	if err != nil {
		return nil, err
	}
	out.AutoSave = autos[uuid.Nil]
	if out.Spare, err = s.spare(ctx, s.store.db, userID); err != nil {
		return nil, err
	}
	nets, err := s.monthlyNets(ctx, s.store.db, userID, nil, true)
	if err != nil {
		return nil, err
	}
	out.History = history(nets[uuid.Nil], today, today, nil, 0)
	if out.Entries, err = s.entryPage(ctx, userID, whereSpare, 1, perPage); err != nil {
		return nil, err
	}
	return out, nil
}

// Totals — Home / More (shared).
func (s *Service) Totals(ctx context.Context, userID uuid.UUID) (*shared.SavingTotals, error) {
	return shared.LoadSavingTotals(ctx, s.store.db, userID)
}

// walletKey — a stable sort key of a share ("" for the no-wallet share).
func walletKey(w WalletShare) string {
	if w.Wallet == nil {
		return ""
	}
	return w.Wallet.ID.String()
}

// Goal list sorts: created (default, as stored), deadline (nearest first, none
// last), progress (most done first), remaining (least left first), name.
var goalSorts = map[string]bool{"": true, "created": true, "deadline": true, "progress": true, "remaining": true, "name": true}

func sortGoals(v []GoalView, by string) {
	less := map[string]func(a, b *GoalView) bool{
		"deadline": func(a, b *GoalView) bool {
			if (a.Deadline == nil) != (b.Deadline == nil) {
				return a.Deadline != nil
			}
			return a.Deadline != nil && *a.Deadline < *b.Deadline
		},
		// Open-ended goals (no % / remaining) go last.
		"progress": func(a, b *GoalView) bool {
			if (a.ProgressPct == nil) != (b.ProgressPct == nil) {
				return a.ProgressPct != nil
			}
			return a.ProgressPct != nil && *a.ProgressPct > *b.ProgressPct
		},
		"remaining": func(a, b *GoalView) bool {
			if (a.RemainingAmount == nil) != (b.RemainingAmount == nil) {
				return a.RemainingAmount != nil
			}
			return a.RemainingAmount != nil && *a.RemainingAmount < *b.RemainingAmount
		},
		"name":      func(a, b *GoalView) bool { return strings.ToLower(a.Name) < strings.ToLower(b.Name) },
	}[by]
	if less == nil {
		return
	}
	sort.SliceStable(v, func(i, j int) bool { return less(&v[i], &v[j]) })
}
