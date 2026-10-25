package budgets

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

const (
	defaultTimezone = "Asia/Bangkok"
	recentPeriods   = 6
	historyPeriods  = 6
	openAheadDays   = 30 // an open-ended budget's expected window
	itemsPerPage    = 20
)

type Service struct {
	store *Store
	// expected, when set, supplies the recurring occurrences that are still to
	// be recorded (รอจ่าย); nil keeps reading the stored recurring items.
	expected shared.Expecter
}

func NewService(store *Store) *Service { return &Service{store: store} }

func (s *Service) WithExpected(e shared.Expecter) *Service { s.expected = e; return s }

func (s *Service) ResolveTimezone(ctx context.Context, userID uuid.UUID, headerTZ string) (*time.Location, string, error) {
	prefs, err := shared.UserPrefs(ctx, s.store.db, userID)
	if err != nil {
		return nil, "", err
	}
	for _, name := range []string{headerTZ, prefs.Timezone, defaultTimezone} {
		if name == "" {
			continue
		}
		if loc, err := time.LoadLocation(name); err == nil {
			return loc, name, nil
		}
	}
	loc, err := time.LoadLocation("UTC")
	return loc, "UTC", err
}

func (s *Service) today(ctx context.Context, userID uuid.UUID, tz string) (time.Time, error) {
	loc, _, err := s.ResolveTimezone(ctx, userID, tz)
	if err != nil {
		return time.Time{}, err
	}
	now := time.Now().In(loc)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC), nil
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

func fptr(f float64) *float64 { return &f }

func iptr(i int) *int { return &i }

func dstr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(dateLayout)
	return &s
}

// periodsAndBase reads the budget's periods in one statement and brings them up to date
// first when they are missing or stale. base is how many of them start before its anchor.
func (s *Service) periodsAndBase(ctx context.Context, b *Budget, today time.Time) ([]*periodRow, int, error) {
	all, err := s.store.periodsOf(ctx, s.store.db, []uuid.UUID{b.ID})
	if err != nil {
		return nil, 0, err
	}
	return s.settlePeriods(ctx, b, all[b.ID], today)
}

func (s *Service) settlePeriods(ctx context.Context, b *Budget, rows []*periodRow, today time.Time) ([]*periodRow, int, error) {
	base := baseSeqIn(b, rows)
	stale := len(rows) == 0
	if !stale && b.Recurring && b.Status == StatusActive && b.ClosedOn == nil {
		last := rows[len(rows)-1]
		stale = last.End != nil && last.End.Before(today)
	}
	if !stale {
		return rows, base, nil
	}
	tx, err := s.store.db.Begin(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx)
	if err := s.store.syncPeriods(ctx, tx, b, today); err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	again, err := s.store.periodsOf(ctx, s.store.db, []uuid.UUID{b.ID})
	return again[b.ID], base, err
}

func currentIndex(rows []*periodRow, state string, today time.Time) int {
	if len(rows) == 0 {
		return -1
	}
	if state == StateUpcoming {
		return 0
	}
	if state == StateActive {
		for i := len(rows) - 1; i >= 0; i-- {
			r := rows[i]
			if !r.Start.After(today) && (r.End == nil || !r.End.Before(today)) {
				return i
			}
		}
	}
	return len(rows) - 1
}

func periodIndexOf(rows []*periodRow, d time.Time) int {
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		if !r.Start.After(d) {
			if r.End == nil || !r.End.Before(d) {
				return i
			}
			return -1
		}
	}
	return -1
}

type figures struct {
	used, expected float64
	rows           []countRow
}

func forecastOf(r *periodRow, f figures, today time.Time, live bool) (Forecast, bool) {
	out := Forecast{RemainingAfterExpected: round2(r.Amount - f.used - f.expected), Available: round2(r.Amount - f.used)}
	if !live {
		out.RemainingAfterExpected = round2(r.Amount - f.used)
		return out, false
	}
	elapsed := days(r.Start, today) + 1
	if elapsed < 1 {
		elapsed = 1
	}
	avg := f.used / float64(elapsed)
	out.ElapsedDays, out.AvgDaily = iptr(elapsed), fptr(round2(avg))
	overDate := func(limit *time.Time) *string {
		if f.used+f.expected > r.Amount {
			return dstr(dptr(today))
		}
		if avg <= 0 {
			return nil
		}
		n := int(math.Floor((r.Amount-f.used-f.expected)/avg)) + 1
		d := today.AddDate(0, 0, n)
		if limit != nil && d.After(*limit) {
			return nil
		}
		if limit == nil && n > 3650 {
			return nil
		}
		return dstr(&d)
	}
	if r.End == nil {
		out.AvgMonthly = fptr(round2(avg * 30))
		out.ForecastOverDate = overDate(nil)
		var month float64
		first := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
		for _, c := range f.rows {
			if c.CountsAs == "used" && c.Date != nil && !c.Date.Before(first) {
				month += c.Amount
			}
		}
		out.ThisMonth = fptr(round2(month))
		return out, false
	}
	total := days(r.Start, *r.End) + 1
	left := days(today, *r.End) + 1
	out.TotalDays = iptr(total)
	end := round2(avg*float64(total) + f.expected)
	out.ForecastEnd, out.ProjectedEnd = fptr(end), fptr(end)
	if left > 0 {
		out.DaysLeft = iptr(left)
	}
	out.PacePct = iptr(int(math.Round(math.Min(1, float64(elapsed)/float64(total)) * 100)))
	if end > r.Amount+0.005 {
		out.ForecastOverBy = fptr(round2(end - r.Amount))
	}
	out.ForecastOverDate = overDate(r.End)
	if left > 0 {
		out.DailyAllowance = fptr(math.Max(0, math.Floor((r.Amount-f.used-f.expected)/float64(left))))
	}
	return out, end > r.Amount+0.005
}

func periodView(b *Budget, r *periodRow, f figures, current bool, today time.Time, pace bool) Period {
	base := r.Base
	if base <= 0 { // a period not stored yet
		base = b.Amount
	}
	p := Period{
		ID: r.ID, Seq: r.Seq, Start: r.Start.Format(dateLayout), End: dstr(r.End), Amount: r.Amount,
		BaseAmount: base, Overridden: r.Overridden, Note: r.Note, IsCurrent: current,
		OnPace: pace && current,
		Used:   round2(f.used), Expected: round2(f.expected), Remaining: round2(r.Amount - f.used),
	}
	if f.used > r.Amount {
		p.OverBy = round2(f.used - r.Amount)
	}
	if r.Amount > 0 {
		p.Pct = int(math.Round(f.used / r.Amount * 100))
	}
	switch {
	case f.used > r.Amount+0.005:
		p.Status, p.Tone = PeriodOver, "danger"
	case b.WarnPct != nil && f.used >= r.Amount*float64(*b.WarnPct)/100:
		p.Status, p.Tone = PeriodWarn, "warning"
	case pace:
		p.Status, p.Tone = PeriodPace, "neutral"
	default:
		p.Status, p.Tone = PeriodOK, "neutral"
	}
	if current && r.End != nil && !today.After(*r.End) && !today.Before(r.Start) {
		total, elapsed := days(r.Start, *r.End)+1, days(r.Start, today)+1
		p.PacePct = iptr(int(math.Round(math.Min(1, float64(elapsed)/float64(total)) * 100)))
		p.DaysLeft = iptr(days(today, *r.End) + 1)
	}
	return p
}

type refs struct {
	cats    map[uuid.UUID]shared.CategoryRef
	accs    map[uuid.UUID]shared.AccountRef
	events  map[uuid.UUID]shared.EventRef
	recItem map[uuid.UUID]shared.RecurringItemLink
	// tagIcons — the user's own tags by lower-cased name.
	tagIcons map[string]*shared.IconCode
}

func (s *Service) loadRefs(ctx context.Context, userID uuid.UUID, bs []*Budget, rows []countRow) (*refs, error) {
	var catIDs, accIDs, evIDs, itemIDs []uuid.UUID
	tagNames := map[string]bool{}
	for _, b := range bs {
		for _, c := range b.Categories {
			catIDs = append(catIDs, c.ID)
		}
		accIDs = append(accIDs, b.Wallets...)
	}
	for _, r := range rows {
		if r.CategoryID != nil {
			catIDs = append(catIDs, *r.CategoryID)
		}
		if r.AccountID != nil {
			accIDs = append(accIDs, *r.AccountID)
		}
		if r.EventID != nil {
			evIDs = append(evIDs, *r.EventID)
		}
		if r.ItemID != nil {
			itemIDs = append(itemIDs, *r.ItemID)
		}
		for _, t := range r.Tags {
			tagNames[strings.ToLower(t)] = true
		}
	}
	out := &refs{recItem: map[uuid.UUID]shared.RecurringItemLink{}, tagIcons: map[string]*shared.IconCode{}}
	var err error
	if len(tagNames) > 0 {
		names := make([]string, 0, len(tagNames))
		for n := range tagNames {
			names = append(names, n)
		}
		q, err := s.store.db.Query(ctx, `SELECT lower(name), icon_code FROM tags WHERE user_id = $1 AND lower(name) = ANY($2)`, userID, names)
		if err != nil {
			return nil, err
		}
		for q.Next() {
			var (
				name string
				icon []byte
			)
			if err := q.Scan(&name, &icon); err != nil {
				q.Close()
				return nil, err
			}
			out.tagIcons[name] = shared.IconFromJSON(icon)
		}
		q.Close()
		if err := q.Err(); err != nil {
			return nil, err
		}
	}
	if out.cats, err = shared.LoadCategoryRefs(ctx, s.store.db, catIDs); err != nil {
		return nil, err
	}
	if out.accs, err = shared.LoadAccountRefs(ctx, s.store.db, accIDs); err != nil {
		return nil, err
	}
	if out.events, err = shared.LoadEventRefs(ctx, s.store.db, evIDs); err != nil {
		return nil, err
	}
	if len(itemIDs) > 0 {
		q, err := s.store.db.Query(ctx, `SELECT ri.id, ri.seq, r.id, r.name, r.icon_code, r.total_count
			FROM recurring_items ri LEFT JOIN recurring_rules r ON r.id = ri.rule_id WHERE ri.id = ANY($1)`, itemIDs)
		if err != nil {
			return nil, err
		}
		for q.Next() {
			var (
				id      uuid.UUID
				seq     *int32
				ruleID  *uuid.UUID
				name    *string
				icon    []byte
				total   *int32
				l       shared.RecurringItemLink
				toIntPt = func(p *int32) *int {
					if p == nil {
						return nil
					}
					v := int(*p)
					return &v
				}
			)
			if err := q.Scan(&id, &seq, &ruleID, &name, &icon, &total); err != nil {
				q.Close()
				return nil, err
			}
			l.ItemID, l.Seq, l.Total = id, toIntPt(seq), toIntPt(total)
			if ruleID != nil && name != nil {
				l.Recurring = &shared.RecurringRef{ID: *ruleID, Name: *name, IconCode: shared.IconFromJSON(icon)}
			}
			out.recItem[id] = l
		}
		q.Close()
	}
	return out, nil
}

func scopeView(b *Budget, rf *refs) Scope {
	sc := Scope{All: b.ScopeAll, Categories: []ScopeCategory{}, Tags: append([]string{}, b.TagNames...), Wallets: []shared.AccountRef{}}
	for _, c := range b.Categories {
		if ref, ok := rf.cats[c.ID]; ok {
			sc.Categories = append(sc.Categories, ScopeCategory{Category: ref, IncludeChildren: c.IncludeChildren})
		}
	}
	for _, w := range b.Wallets {
		if ref, ok := rf.accs[w]; ok {
			sc.Wallets = append(sc.Wallets, ref)
		}
	}
	return sc
}

func budgetView(b *Budget, state string, p Period, rf *refs) View {
	v := View{
		ID: b.ID, Name: b.Name, Description: b.Description, Note: b.Note, IconCode: b.IconCode, Amount: b.Amount,
		WarnPct: b.WarnPct, Scope: scopeView(b, rf), Recurring: b.Recurring, Rule: b.apiRule(),
		StartDate: b.StartDate.Format(dateLayout), End: b.apiEnd(), State: state, ClosedOn: dstr(b.ClosedOn),
		Period: p, CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt,
	}
	v.Actions = Actions{
		Edit: state != StateArchived, Close: state == StateActive,
		Archive: state != StateArchived, Restore: state == StateArchived,
	}
	return v
}

func itemView(c countRow, rf *refs, reason func(countRow) string) Item {
	it := Item{
		Kind: c.Kind, ID: c.ID, Source: c.Source, CountsAs: c.CountsAs, Date: dstr(c.Date), Amount: round2(c.Amount),
		Description: c.Description, Tags: c.Tags, Reason: reason(c), TagRefs: []ItemTag{},
	}
	for _, t := range c.Tags {
		it.TagRefs = append(it.TagRefs, ItemTag{Name: t, IconCode: rf.tagIcons[strings.ToLower(t)]})
	}
	if c.CategoryID != nil {
		if ref, ok := rf.cats[*c.CategoryID]; ok {
			it.Category = &ref
		}
	}
	if c.AccountID != nil {
		if ref, ok := rf.accs[*c.AccountID]; ok {
			it.Account = &ref
		}
	}
	if c.EventID != nil {
		if ref, ok := rf.events[*c.EventID]; ok {
			it.Event = &ref
		}
	}
	it.RuleID, it.Slot = c.RuleID, dstr(c.Slot)
	if c.Link != nil {
		it.Recurring = c.Link
	} else if c.ItemID != nil {
		if l, ok := rf.recItem[*c.ItemID]; ok {
			it.Recurring = &ItemRecurring{ItemID: &l.ItemID, Recurring: l.Recurring, Seq: l.Seq, Total: l.Total}
		}
	}
	return it
}

// sortItems: newest date first for every row (expected ones among the used
// ones by date); on one day the expected ones lead.
func sortItems(rows []countRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		ad, bd := time.Time{}, time.Time{}
		if a.Date != nil {
			ad = *a.Date
		}
		if b.Date != nil {
			bd = *b.Date
		}
		if !ad.Equal(bd) {
			return ad.After(bd)
		}
		if a.CountsAs != b.CountsAs {
			return a.CountsAs == "expected"
		}
		return a.ID.String() < b.ID.String()
	})
}

func figureWindow(prs []*periodRow, lo, hi, cur int, live bool, today time.Time) window {
	w := window{From: prs[lo].Start}
	if prs[hi].End != nil {
		w.To = *prs[hi].End
	}
	if live && cur >= 0 {
		c := prs[cur]
		w.Expected, w.ExpFrom = true, c.Start
		if c.End != nil {
			w.ExpTo = *c.End
		} else {
			w.ExpTo = today.AddDate(0, 0, openAheadDays)
		}
	}
	return w
}

func figuresOf(prs []*periodRow, lo, hi, cur int, rows []countRow) map[int]*figures {
	out := map[int]*figures{}
	for i := lo; i <= hi; i++ {
		out[i] = &figures{}
	}
	if cur >= 0 && out[cur] == nil {
		out[cur] = &figures{}
	}
	for _, r := range rows {
		idx := cur
		if r.CountsAs == "used" {
			if r.Date == nil {
				continue
			}
			idx = periodIndexOf(prs, *r.Date)
		}
		f, ok := out[idx]
		if !ok {
			continue
		}
		if r.CountsAs == "used" {
			f.used += r.Amount
		} else {
			f.expected += r.Amount
		}
		f.rows = append(f.rows, r)
	}
	return out
}

// periodFigures is figuresOf for ONE budget (cats: its scope categories, expanded).
func (s *Service) periodFigures(ctx context.Context, b *Budget, cats []uuid.UUID, prs []*periodRow, lo, hi, cur int, live bool, today time.Time) (map[int]*figures, error) {
	rows, err := s.collectMany(ctx, b.UserID, []collectSpec{{b: b, w: figureWindow(prs, lo, hi, cur, live, today), cats: cats}})
	if err != nil {
		return nil, err
	}
	return figuresOf(prs, lo, hi, cur, rows[0]), nil
}

type listEntry struct {
	b     *Budget
	state string
	p     Period
}

// listEntries is the pass behind List and Totals, whatever the number of budgets: one
// read of the budgets, one of all their periods, one expansion of the scope categories,
// one expected-occurrences call and one statement for the rows (see collectMany). It
// returns the budgets of the status group (and health) with their current period, and the
// summary over the active ones.
func (s *Service) listEntries(ctx context.Context, userID uuid.UUID, status, health string, today time.Time) ([]listEntry, ListSummary, error) {
	var sum ListSummary
	all, err := s.store.listAll(ctx, userID)
	if err != nil {
		return nil, sum, err
	}
	if status == "" {
		status = "active"
	}
	ids := make([]uuid.UUID, len(all))
	for i, b := range all {
		ids[i] = b.ID
	}
	periods, err := s.store.periodsOf(ctx, s.store.db, ids)
	if err != nil {
		return nil, sum, err
	}
	type work struct {
		b     *Budget
		state string
		group string
		prs   []*periodRow
		cur   int
		live  bool
	}
	var works []*work
	for _, b := range all {
		rows := periods[b.ID]
		state := b.state(today, baseSeqIn(b, rows))
		group := "active"
		switch state {
		case StateEnded:
			group, sum.EndedCount = "ended", sum.EndedCount+1
		case StateArchived:
			group, sum.ArchivedCount = "archived", sum.ArchivedCount+1
		default:
			sum.ActiveCount++
		}
		if group != status && group != "active" {
			continue
		}
		prs, _, err := s.settlePeriods(ctx, b, rows, today)
		if err != nil {
			return nil, sum, err
		}
		works = append(works, &work{b: b, state: state, group: group, prs: prs,
			cur: currentIndex(prs, state, today), live: state == StateActive || state == StateUpcoming})
	}
	bs := make([]*Budget, len(works))
	for i, w := range works {
		bs[i] = w.b
	}
	cats, err := s.store.expandCategoriesOf(ctx, bs)
	if err != nil {
		return nil, sum, err
	}
	specs := make([]collectSpec, len(works))
	for i, w := range works {
		specs[i] = collectSpec{b: w.b, w: figureWindow(w.prs, w.cur, w.cur, w.cur, w.live, today), cats: cats[w.b.ID]}
	}
	rowsOf, err := s.collectMany(ctx, userID, specs)
	if err != nil {
		return nil, sum, err
	}
	var (
		picked []listEntry
		seen   = map[string]bool{} // counted rows already in total_used
	)
	for i, w := range works {
		b, prs, cur := w.b, w.prs, w.cur
		figs := figuresOf(prs, cur, cur, cur, rowsOf[i])
		_, pace := forecastOf(prs[cur], *figs[cur], today, w.state == StateActive)
		p := periodView(b, prs[cur], *figs[cur], true, today, pace)
		if w.group == "active" {
			switch p.Status {
			case PeriodOver:
				sum.OverCount++
				sum.Counts.Over++
			case PeriodWarn:
				sum.WarnCount++
				sum.Counts.Near++
			case PeriodPace:
				sum.PaceCount++
				sum.Counts.OK++
			default:
				sum.Counts.OK++
			}
			sum.Counts.All++
			sum.TotalLimit += prs[cur].Amount
			for _, r := range figs[cur].rows {
				if r.CountsAs != "used" {
					continue
				}
				k := r.Kind + ":" + r.ID.String()
				if !seen[k] {
					seen[k] = true
					sum.TotalUsed += r.Amount
				}
			}
		}
		if w.group == status && healthMatches(health, p.Status) {
			picked = append(picked, listEntry{b, w.state, p})
		}
	}
	sum.TotalLimit, sum.TotalUsed = round2(sum.TotalLimit), round2(sum.TotalUsed)
	sum.TotalRemaining = round2(sum.TotalLimit - sum.TotalUsed)
	if sum.TotalLimit > 0 {
		sum.UsedPct = int(math.Round(sum.TotalUsed / sum.TotalLimit * 100))
	}
	return picked, sum, nil
}

func (s *Service) List(ctx context.Context, userID uuid.UUID, status, sortBy, health, tz string) (*List, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	picked, sum, err := s.listEntries(ctx, userID, status, health, today)
	if err != nil {
		return nil, err
	}
	rank := map[string]int{PeriodOver: 0, PeriodWarn: 1, PeriodPace: 2, PeriodOK: 3}
	sort.SliceStable(picked, func(i, j int) bool {
		a, c := picked[i], picked[j]
		switch sortBy {
		case "name":
			return strings.ToLower(a.b.Name) < strings.ToLower(c.b.Name)
		case "newest":
			return a.b.CreatedAt.After(c.b.CreatedAt)
		}
		if rank[a.p.Status] != rank[c.p.Status] {
			return rank[a.p.Status] < rank[c.p.Status]
		}
		if a.p.Pct != c.p.Pct {
			return a.p.Pct > c.p.Pct
		}
		return strings.ToLower(a.b.Name) < strings.ToLower(c.b.Name)
	})
	bs := make([]*Budget, len(picked))
	for i, e := range picked {
		bs[i] = e.b
	}
	rf, err := s.loadRefs(ctx, userID, bs, nil)
	if err != nil {
		return nil, err
	}
	out := &List{Summary: sum, Data: make([]View, 0, len(picked))}
	for _, e := range picked {
		out.Data = append(out.Data, budgetView(e.b, e.state, e.p, rf))
	}
	return out, nil
}

// Totals are the figures /me/counts and the dashboard show: the same pass as List, without
// the views (no refs to resolve).
func (s *Service) Totals(ctx context.Context, userID uuid.UUID, tz string) (*Totals, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	picked, _, err := s.listEntries(ctx, userID, "active", "", today)
	if err != nil {
		return nil, err
	}
	out := &Totals{}
	for _, e := range picked {
		if e.state != StateActive {
			continue
		}
		out.Count++
		out.TotalBudget += e.p.Amount
		out.TotalSpent += e.p.Used
		if e.p.Status == PeriodOver {
			out.OverLimitCount++
		}
	}
	out.TotalBudget, out.TotalSpent = round2(out.TotalBudget), round2(out.TotalSpent)
	if out.TotalBudget > 0 {
		out.UtilizationPct = round2(out.TotalSpent / out.TotalBudget * 100)
	}
	return out, nil
}

func (s *Service) detail(ctx context.Context, b *Budget, periodID *uuid.UUID, today time.Time) (*Detail, error) {
	prs, base, err := s.periodsAndBase(ctx, b, today)
	if err != nil {
		return nil, err
	}
	state := b.state(today, base)
	scope, err := s.store.expandCategories(ctx, b.Categories)
	if err != nil {
		return nil, err
	}
	cur := currentIndex(prs, state, today)
	view := cur
	if periodID != nil {
		view = -1
		for i, p := range prs {
			if p.ID == *periodID {
				view = i
			}
		}
		if view < 0 {
			return nil, ErrPeriodNotFound
		}
	}
	lo := len(prs) - recentPeriods
	if v := view - historyPeriods; v < lo {
		lo = v
	}
	if lo < 0 {
		lo = 0
	}
	live := state == StateActive || state == StateUpcoming
	figs, err := s.periodFigures(ctx, b, scope, prs, lo, len(prs)-1, cur, live, today)
	if err != nil {
		return nil, err
	}
	f := *figs[view]
	fc, pace := forecastOf(prs[view], f, today, view == cur && state == StateActive)
	if b.Recurring {
		var n int
		var tot float64
		for i := view - 1; i >= 0 && n < historyPeriods; i-- {
			if g, ok := figs[i]; ok {
				tot += g.used
				n++
			}
		}
		fc.HistoryCount = n
		if n > 0 {
			fc.HistoryAvg = fptr(round2(tot / float64(n)))
		}
	}
	sortItems(f.rows)
	rf, err := s.loadRefs(ctx, b.UserID, []*Budget{b}, f.rows)
	if err != nil {
		return nil, err
	}
	out := &Detail{
		Budget:   budgetView(b, state, periodView(b, prs[view], f, view == cur, today, pace), rf),
		Forecast: fc, BySource: []SourceRow{}, ByCategory: []CategoryRow{},
	}
	src := map[string]*SourceRow{}
	cats := map[uuid.UUID]*CategoryRow{}
	var noCat *CategoryRow
	for _, r := range f.rows {
		sr := src[r.Source]
		if sr == nil {
			sr = &SourceRow{Source: r.Source, CountsAs: r.CountsAs}
			src[r.Source] = sr
		}
		sr.Count++
		sr.Amount += r.Amount
		if r.CountsAs != "used" {
			continue
		}
		if r.CategoryID == nil {
			if noCat == nil {
				noCat = &CategoryRow{}
			}
			noCat.Amount += r.Amount
			continue
		}
		cr := cats[*r.CategoryID]
		if cr == nil {
			cr = &CategoryRow{}
			if ref, ok := rf.cats[*r.CategoryID]; ok {
				cr.Category = &ref
			}
			cats[*r.CategoryID] = cr
		}
		cr.Amount += r.Amount
	}
	for _, k := range []string{SourceNormal, SourceRecurring, SourceEvent, SourceUpcoming, SourcePending, SourceFutureTx} {
		if sr := src[k]; sr != nil && sr.Amount != 0 {
			sr.Amount = round2(sr.Amount)
			out.BySource = append(out.BySource, *sr)
		}
	}
	for _, cr := range cats {
		out.ByCategory = append(out.ByCategory, *cr)
	}
	if noCat != nil {
		out.ByCategory = append(out.ByCategory, *noCat)
	}
	sort.SliceStable(out.ByCategory, func(i, j int) bool { return out.ByCategory[i].Amount > out.ByCategory[j].Amount })
	for i := range out.ByCategory {
		out.ByCategory[i].Amount = round2(out.ByCategory[i].Amount)
		if f.used > 0 {
			out.ByCategory[i].Pct = int(math.Round(out.ByCategory[i].Amount / f.used * 100))
		}
	}
	out.Periods.Data = []Period{}
	for i := len(prs) - 1; i >= 0 && len(out.Periods.Data) < recentPeriods; i-- {
		g := figs[i]
		if g == nil {
			g = &figures{}
		}
		pace := false
		if i == cur {
			_, pace = forecastOf(prs[i], *g, today, state == StateActive)
		}
		out.Periods.Data = append(out.Periods.Data, periodView(b, prs[i], *g, i == cur, today, pace))
	}
	out.Periods.HasMore = len(prs) > recentPeriods
	reason := reasoner(b, scope)
	out.Items = pageItems(f.rows, rf, reason, 1, itemsPerPage)
	return out, nil
}

func pageItems(rows []countRow, rf *refs, reason func(countRow) string, page, perPage int) ItemList {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = itemsPerPage
	}
	out := ItemList{Data: []Item{}, Pagination: shared.NewPage(page, perPage, len(rows))}
	for i := (page - 1) * perPage; i < len(rows) && i < page*perPage; i++ {
		out.Data = append(out.Data, itemView(rows[i], rf, reason))
	}
	return out
}

func (s *Service) Get(ctx context.Context, userID, id uuid.UUID, periodID *uuid.UUID, tz string, withDeletePreview bool) (*Detail, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	b, err := s.store.get(ctx, nil, userID, id, false)
	if err != nil {
		return nil, err
	}
	d, err := s.detail(ctx, b, periodID, today)
	if err != nil || !withDeletePreview {
		return d, err
	}
	p := &DeletePreview{Categories: len(b.Categories), Wallets: len(b.Wallets)}
	if err := s.store.db.QueryRow(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE overridden) FROM budget_periods WHERE budget_id = $1`, id).Scan(&p.Periods, &p.OverriddenPeriods); err != nil {
		return nil, err
	}
	d.DeletePreview = p
	return d, nil
}

func (s *Service) Periods(ctx context.Context, userID, id uuid.UUID, page, perPage int, tz string) (*PeriodList, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	b, err := s.store.get(ctx, nil, userID, id, false)
	if err != nil {
		return nil, err
	}
	prs, base, err := s.periodsAndBase(ctx, b, today)
	if err != nil {
		return nil, err
	}
	state := b.state(today, base)
	cats, err := s.store.expandCategories(ctx, b.Categories)
	if err != nil {
		return nil, err
	}
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}
	cur := currentIndex(prs, state, today)
	out := &PeriodList{Data: []Period{}, Pagination: shared.NewPage(page, perPage, len(prs))}
	hi := len(prs) - 1 - (page-1)*perPage
	lo := hi - perPage + 1
	if lo < 0 {
		lo = 0
	}
	if hi < 0 {
		return out, nil
	}
	live := state == StateActive || state == StateUpcoming
	c := cur
	if c < lo || c > hi {
		c = -1
	}
	figs, err := s.periodFigures(ctx, b, cats, prs, lo, hi, c, live, today)
	if err != nil {
		return nil, err
	}
	for i := hi; i >= lo; i-- {
		pace := false
		if i == cur {
			_, pace = forecastOf(prs[i], *figs[i], today, state == StateActive)
		}
		out.Data = append(out.Data, periodView(b, prs[i], *figs[i], i == cur, today, pace))
	}
	return out, nil
}

func (s *Service) Items(ctx context.Context, userID, id uuid.UUID, periodID *uuid.UUID, source string, page, perPage int, tz string) (*ItemList, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	b, err := s.store.get(ctx, nil, userID, id, false)
	if err != nil {
		return nil, err
	}
	prs, base, err := s.periodsAndBase(ctx, b, today)
	if err != nil {
		return nil, err
	}
	state := b.state(today, base)
	cats, err := s.store.expandCategories(ctx, b.Categories)
	if err != nil {
		return nil, err
	}
	cur := currentIndex(prs, state, today)
	view := cur
	if periodID != nil {
		view = -1
		for i, p := range prs {
			if p.ID == *periodID {
				view = i
			}
		}
		if view < 0 {
			return nil, ErrPeriodNotFound
		}
	}
	c := -1
	if view == cur {
		c = cur
	}
	figs, err := s.periodFigures(ctx, b, cats, prs, view, view, c, state == StateActive || state == StateUpcoming, today)
	if err != nil {
		return nil, err
	}
	rows := figs[view].rows
	if source != "" {
		var kept []countRow
		for _, r := range rows {
			if r.Source == source {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	sortItems(rows)
	rf, err := s.loadRefs(ctx, userID, nil, rows)
	if err != nil {
		return nil, err
	}
	reason := reasoner(b, cats)
	out := pageItems(rows, rf, reason, page, perPage)
	return &out, nil
}

func parseDate(s string) (time.Time, error) { return time.Parse(dateLayout, s) }

func apply(b *Budget, r *Request, creating bool) (amount, timing bool, err error) {
	if creating || r.has("name") {
		if r.Name == nil {
			return false, false, ErrValidation
		}
		name := strings.TrimSpace(*r.Name)
		if name == "" || len([]rune(name)) > 100 {
			return false, false, ErrValidation
		}
		b.Name = name
	}
	if r.has("description") {
		if r.Description != nil && len([]rune(*r.Description)) > 200 {
			return false, false, ErrValidation
		}
		b.Description = shared.CleanText(r.Description)
	}
	if r.has("note") {
		if r.Note != nil && len([]rune(*r.Note)) > 500 {
			return false, false, ErrValidation
		}
		b.Note = shared.CleanText(r.Note)
	}
	if r.has("icon_code") {
		b.IconCode = r.IconCode
	}
	if creating || r.has("amount") {
		if r.Amount == nil || *r.Amount <= 0 {
			return false, false, ErrValidation
		}
		amount = *r.Amount != b.Amount
		b.Amount = *r.Amount
	}
	if creating && !r.has("warn_pct") {
		b.WarnPct = iptr(defaultWarnPct)
	} else if r.has("warn_pct") {
		if r.WarnPct != nil && (*r.WarnPct < 1 || *r.WarnPct > 100) {
			return false, false, ErrValidation
		}
		b.WarnPct = r.WarnPct
	}
	if creating || r.has("scope") {
		if r.Scope == nil {
			return false, false, ErrValidation
		}
		sc := r.Scope
		b.ScopeAll, b.Categories, b.TagNames, b.Wallets = sc.All, []scopeCat{}, []string{}, []uuid.UUID{}
		seenCat := map[uuid.UUID]bool{}
		for _, c := range sc.Categories {
			if seenCat[c.ID] {
				continue
			}
			seenCat[c.ID] = true
			b.Categories = append(b.Categories, scopeCat{ID: c.ID, IncludeChildren: c.IncludeChildren == nil || *c.IncludeChildren})
		}
		seenTag := map[string]bool{}
		for _, t := range sc.Tags {
			t = strings.TrimSpace(t)
			if t == "" || len([]rune(t)) > 50 {
				return false, false, ErrValidation
			}
			if !seenTag[strings.ToLower(t)] {
				seenTag[strings.ToLower(t)] = true
				b.TagNames = append(b.TagNames, t)
			}
		}
		seenW := map[uuid.UUID]bool{}
		for _, w := range sc.WalletIDs {
			if !seenW[w] {
				seenW[w] = true
				b.Wallets = append(b.Wallets, w)
			}
		}
		if !b.ScopeAll && len(b.Categories) == 0 && len(b.TagNames) == 0 && len(b.Wallets) == 0 {
			return false, false, ErrValidation
		}
	}
	if creating || r.has("recurring") {
		rec := r.Recurring != nil && *r.Recurring
		timing = timing || rec != b.Recurring
		b.Recurring = rec
	}
	if r.has("rule") || (creating && b.Recurring) || (r.has("recurring") && b.Recurring) {
		if r.Rule == nil && b.Freq == nil {
			return false, false, ErrInvalidRule
		}
		if r.Rule != nil {
			switch r.Rule.Freq {
			case "daily", "weekly", "monthly", "yearly":
			default:
				return false, false, ErrInvalidRule
			}
			iv := r.Rule.Interval
			if iv <= 0 {
				iv = 1
			}
			if b.Freq == nil || *b.Freq != r.Rule.Freq || b.Interval != iv {
				timing = true
			}
			f := r.Rule.Freq
			b.Freq, b.Interval = &f, iv
		}
	}
	if !b.Recurring {
		b.Freq, b.Interval = nil, 1
	}
	if creating || r.has("start_date") {
		if r.StartDate == nil {
			return false, false, ErrValidation
		}
		d, err := parseDate(*r.StartDate)
		if err != nil {
			return false, false, ErrValidation
		}
		timing = timing || !d.Equal(b.StartDate)
		b.StartDate = d
	}
	if creating || r.has("end") {
		kind := "none"
		if r.End != nil && r.End.Kind != "" {
			kind = r.End.Kind
		}
		b.EndDate, b.EndCount = nil, nil
		switch kind {
		case "none":
		case "date":
			if r.End.Date == nil {
				return false, false, ErrInvalidRule
			}
			d, err := parseDate(*r.End.Date)
			if err != nil || d.Before(b.StartDate) {
				return false, false, ErrInvalidRule
			}
			b.EndDate = &d
		case "count":
			if !b.Recurring || r.End.Count == nil || *r.End.Count < 1 {
				return false, false, ErrInvalidRule
			}
			c := *r.End.Count
			b.EndCount = &c
		default:
			return false, false, ErrInvalidRule
		}
		b.EndKind = kind
		timing = true
	}
	if b.EndKind == "count" && !b.Recurring {
		return false, false, ErrInvalidRule
	}
	if b.EndDate != nil && b.EndDate.Before(b.StartDate) {
		return false, false, ErrInvalidRule
	}
	return amount, timing, nil
}

func (s *Service) Create(ctx context.Context, userID uuid.UUID, r *Request, tz string) (*Detail, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	b := &Budget{UserID: userID, Status: StatusActive, EndKind: "none", Interval: 1}
	if _, _, err := apply(b, r, true); err != nil {
		return nil, err
	}
	b.AnchorDate = b.StartDate
	tx, err := s.store.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := s.store.checkScope(ctx, tx, userID, b); err != nil {
		return nil, err
	}
	if err := s.store.save(ctx, tx, b); err != nil {
		return nil, err
	}
	if err := s.store.syncPeriods(ctx, tx, b, today); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Get(ctx, userID, b.ID, nil, tz, false)
}

// write — lock, change, save + sync periods, commit, the page.
func (s *Service) write(ctx context.Context, userID, id uuid.UUID, tz string, fn func(tx pgx.Tx, b *Budget, today time.Time) (*uuid.UUID, error)) (*Detail, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	tx, err := s.store.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	b, err := s.store.get(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	periodID, err := fn(tx, b, today)
	if err != nil {
		return nil, err
	}
	if err := s.store.save(ctx, tx, b); err != nil {
		return nil, err
	}
	if err := s.store.syncPeriods(ctx, tx, b, today); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Get(ctx, userID, id, periodID, tz, false)
}

func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, r *Request, tz string) (*Detail, error) {
	if r.Apply != "" && r.Apply != "following" && r.Apply != "all" {
		return nil, ErrValidation
	}
	return s.write(ctx, userID, id, tz, func(tx pgx.Tx, b *Budget, today time.Time) (*uuid.UUID, error) {
		old := *b
		amountChanged, timing, err := apply(b, r, false)
		if err != nil {
			return nil, err
		}
		timing = timing || (r.has("rule") && r.Apply == "all")
		if r.has("scope") {
			if err := s.store.checkScope(ctx, tx, userID, b); err != nil {
				return nil, err
			}
		}
		all := r.Apply == "all" || !b.Recurring || !old.Recurring
		base, err := s.store.baseSeq(ctx, tx, &old)
		if err != nil {
			return nil, err
		}
		prs, err := s.store.periods(ctx, tx, b.ID, `ORDER BY start_date`)
		if err != nil {
			return nil, err
		}
		from := old.StartDate
		if i := currentIndex(prs, old.state(today, base), today); i >= 0 && !all {
			from = prs[i].Start
		}
		if timing {
			ruleChanged := !all && (old.Freq == nil || b.Freq == nil || *old.Freq != *b.Freq || old.Interval != b.Interval)
			switch {
			case all || !b.StartDate.Equal(old.StartDate):
				b.AnchorDate = b.StartDate
				if _, err := tx.Exec(ctx, `DELETE FROM budget_periods WHERE budget_id = $1 AND start_date < $2`,
					b.ID, b.AnchorDate); err != nil {
					return nil, err
				}
			case ruleChanged:
				b.AnchorDate = from
			}
		}
		if amountChanged {
			// From the current period on, the base follows; a period with its own amount keeps it.
			if _, err := tx.Exec(ctx, `UPDATE budget_periods
				SET base_amount = $2, amount = CASE WHEN overridden THEN amount ELSE $2 END
				WHERE budget_id = $1 AND start_date >= $3`, b.ID, b.Amount, from); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
}

func (s *Service) UpdatePeriod(ctx context.Context, userID, id, periodID uuid.UUID, r *PeriodRequest, tz string) (*Detail, error) {
	return s.write(ctx, userID, id, tz, func(tx pgx.Tx, b *Budget, _ time.Time) (*uuid.UUID, error) {
		p, err := scanPeriod(tx.QueryRow(ctx, `SELECT `+periodColumns+` FROM budget_periods WHERE id = $1 AND budget_id = $2 FOR UPDATE`,
			periodID, b.ID))
		if err != nil {
			return nil, ErrPeriodNotFound
		}
		// null, or the base itself, = back to the period's own base (never today's
		// budget amount: a past period keeps its history).
		if r.present["amount"] {
			switch {
			case r.Amount == nil || math.Abs(*r.Amount-p.Base) < 0.005:
				p.Amount, p.Overridden = p.Base, false
			case *r.Amount <= 0:
				return nil, ErrValidation
			default:
				p.Amount, p.Overridden = *r.Amount, true
			}
		}
		if r.present["note"] {
			if r.Note != nil && len([]rune(*r.Note)) > 500 {
				return nil, ErrValidation
			}
			p.Note = shared.CleanText(r.Note)
		}
		if _, err := tx.Exec(ctx, `UPDATE budget_periods SET amount = $2, overridden = $3, note = $4 WHERE id = $1`,
			p.ID, p.Amount, p.Overridden, p.Note); err != nil {
			return nil, err
		}
		return &p.ID, nil
	})
}

func (s *Service) transition(ctx context.Context, userID, id uuid.UUID, tz string, fn func(b *Budget, state string, today time.Time) error) (*Detail, error) {
	return s.write(ctx, userID, id, tz, func(tx pgx.Tx, b *Budget, today time.Time) (*uuid.UUID, error) {
		base, err := s.store.baseSeq(ctx, tx, b)
		if err != nil {
			return nil, err
		}
		return nil, fn(b, b.state(today, base), today)
	})
}

func (s *Service) Close(ctx context.Context, userID, id uuid.UUID, tz string) (*Detail, error) {
	return s.transition(ctx, userID, id, tz, func(b *Budget, state string, today time.Time) error {
		if state != StateActive {
			return ErrInvalidTransition
		}
		b.ClosedOn = dptr(today)
		return nil
	})
}

func (s *Service) Archive(ctx context.Context, userID, id uuid.UUID, tz string) (*Detail, error) {
	return s.transition(ctx, userID, id, tz, func(b *Budget, state string, _ time.Time) error {
		if state == StateArchived {
			return ErrInvalidTransition
		}
		b.Status = StatusArchived
		return nil
	})
}

func (s *Service) Restore(ctx context.Context, userID, id uuid.UUID, tz string) (*Detail, error) {
	return s.transition(ctx, userID, id, tz, func(b *Budget, state string, _ time.Time) error {
		if state != StateArchived {
			return ErrInvalidTransition
		}
		b.Status = StatusActive
		return nil
	})
}

func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) error {
	tag, err := s.store.db.Exec(ctx, `DELETE FROM budgets WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrBudgetNotFound
	}
	return nil
}

func healthMatches(health, status string) bool {
	switch health {
	case "normal":
		return status == PeriodOK || status == PeriodPace
	case "near":
		return status == PeriodWarn
	case "over":
		return status == PeriodOver
	}
	return true
}
