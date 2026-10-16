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
}

func NewService(store *Store) *Service { return &Service{store: store} }

// ResolveTimezone — X-Timezone header → user_preferences.timezone →
// Asia/Bangkok (also used by the dashboard and /me).
func (s *Service) ResolveTimezone(ctx context.Context, userID uuid.UUID, headerTZ string) (*time.Location, string, error) {
	stored, err := s.store.userTimezone(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	for _, name := range []string{headerTZ, stored, defaultTimezone} {
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

// --- periods ---

// loadPeriods — the budget's stored periods, oldest first; writes the
// ones that started since (a new period begins on read).
func (s *Service) loadPeriods(ctx context.Context, b *Budget, today time.Time) ([]*periodRow, error) {
	rows, err := s.store.periods(ctx, s.store.db, b.ID, `ORDER BY start_date`)
	if err != nil {
		return nil, err
	}
	stale := len(rows) == 0
	if !stale && b.Recurring && b.Status == StatusActive && b.ClosedOn == nil {
		last := rows[len(rows)-1]
		stale = last.End != nil && last.End.Before(today)
	}
	if !stale {
		return rows, nil
	}
	tx, err := s.store.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := s.store.syncPeriods(ctx, tx, b, today); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.store.periods(ctx, s.store.db, b.ID, `ORDER BY start_date`)
}

// currentIndex — the period the budget is shown at: today's (active), the
// last (ended / archived), the first (not started).
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

// periodIndexOf — the period containing d (-1: none).
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

// figures — the counted rows of one period.
type figures struct {
	used, expected float64
	rows           []countRow
}

// forecastOf — the period dashboard (current & active only; else the
// history fields alone).
func forecastOf(r *periodRow, f figures, today time.Time, live bool) (Forecast, bool) {
	out := Forecast{RemainingAfterExpected: round2(r.Amount - f.used - f.expected)}
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
		// Open-ended: averages and when it runs out.
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
	out.ForecastEnd = fptr(end)
	if end > r.Amount+0.005 {
		out.ForecastOverBy = fptr(round2(end - r.Amount))
	}
	out.ForecastOverDate = overDate(r.End)
	if left > 0 {
		out.DailyAllowance = fptr(math.Max(0, math.Floor((r.Amount-f.used-f.expected)/float64(left))))
	}
	return out, end > r.Amount+0.005
}

// periodView — the API period.
func periodView(b *Budget, r *periodRow, f figures, current bool, today time.Time, pace bool) Period {
	p := Period{
		ID: r.ID, Seq: r.Seq, Start: r.Start.Format(dateLayout), End: dstr(r.End), Amount: r.Amount,
		BaseAmount: b.Amount, Overridden: r.Overridden, Note: r.Note, IsCurrent: current,
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
		p.DaysLeft = iptr(days(today, *r.End) + 1)
	}
	return p
}

// --- views ---

type refs struct {
	cats    map[uuid.UUID]shared.CategoryRef
	accs    map[uuid.UUID]shared.AccountRef
	events  map[uuid.UUID]shared.EventRef
	recItem map[uuid.UUID]shared.RecurringItemLink
}

func (s *Service) loadRefs(ctx context.Context, bs []*Budget, rows []countRow) (*refs, error) {
	var catIDs, accIDs, evIDs, itemIDs []uuid.UUID
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
	}
	out := &refs{recItem: map[uuid.UUID]shared.RecurringItemLink{}}
	var err error
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

func itemView(c countRow, rf *refs) Item {
	it := Item{
		Kind: c.Kind, ID: c.ID, Source: c.Source, CountsAs: c.CountsAs, Date: dstr(c.Date), Amount: round2(c.Amount),
		Description: c.Description, Tags: c.Tags,
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
	if c.ItemID != nil {
		if l, ok := rf.recItem[*c.ItemID]; ok {
			it.Recurring = &l
		}
	}
	return it
}

// sortItems — expected first (soonest first), then used (newest first).
func sortItems(rows []countRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.CountsAs != b.CountsAs {
			return a.CountsAs == "expected"
		}
		ad, bd := time.Time{}, time.Time{}
		if a.Date != nil {
			ad = *a.Date
		}
		if b.Date != nil {
			bd = *b.Date
		}
		if !ad.Equal(bd) {
			if a.CountsAs == "expected" {
				return ad.Before(bd)
			}
			return ad.After(bd)
		}
		return a.ID.String() < b.ID.String()
	})
}

// periodFigures collects the rows of periods [lo, hi] (indexes into rows)
// in one query and buckets them; expected only for period `cur` when live.
func (s *Service) periodFigures(ctx context.Context, b *Budget, prs []*periodRow, lo, hi, cur int, live bool, today time.Time) (map[int]*figures, error) {
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
	rows, err := s.collect(ctx, b, w)
	if err != nil {
		return nil, err
	}
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
	return out, nil
}

// --- reads ---

func (s *Service) List(ctx context.Context, userID uuid.UUID, status, sortBy, health, tz string) (*List, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	all, err := s.store.listAll(ctx, userID)
	if err != nil {
		return nil, err
	}
	if status == "" {
		status = "active"
	}
	type entry struct {
		b     *Budget
		state string
		p     Period
	}
	var (
		picked []entry
		sum    ListSummary
		seen   = map[string]bool{} // counted rows already in total_used
	)
	for _, b := range all {
		base, err := s.store.baseSeq(ctx, s.store.db, b)
		if err != nil {
			return nil, err
		}
		state := b.state(today, base)
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
		prs, err := s.loadPeriods(ctx, b, today)
		if err != nil {
			return nil, err
		}
		cur := currentIndex(prs, state, today)
		live := state == StateActive || state == StateUpcoming
		figs, err := s.periodFigures(ctx, b, prs, cur, cur, cur, live, today)
		if err != nil {
			return nil, err
		}
		_, pace := forecastOf(prs[cur], *figs[cur], today, state == StateActive)
		p := periodView(b, prs[cur], *figs[cur], true, today, pace)
		if group == "active" {
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
		if group == status && healthMatches(health, p.Status) {
			picked = append(picked, entry{b, state, p})
		}
	}
	sum.TotalLimit, sum.TotalUsed = round2(sum.TotalLimit), round2(sum.TotalUsed)
	sum.TotalRemaining = round2(sum.TotalLimit - sum.TotalUsed)
	if sum.TotalLimit > 0 {
		sum.UsedPct = int(math.Round(sum.TotalUsed / sum.TotalLimit * 100))
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
	rf, err := s.loadRefs(ctx, bs, nil)
	if err != nil {
		return nil, err
	}
	out := &List{Summary: sum, Data: make([]View, 0, len(picked))}
	for _, e := range picked {
		out.Data = append(out.Data, budgetView(e.b, e.state, e.p, rf))
	}
	return out, nil
}

// Totals — the Home tile / More budgets_summary.
func (s *Service) Totals(ctx context.Context, userID uuid.UUID, tz string) (*Totals, error) {
	l, err := s.List(ctx, userID, "active", "", "", tz)
	if err != nil {
		return nil, err
	}
	out := &Totals{}
	for _, v := range l.Data {
		if v.State != StateActive {
			continue
		}
		out.Count++
		out.TotalBudget += v.Period.Amount
		out.TotalSpent += v.Period.Used
		if v.Period.Status == PeriodOver {
			out.OverLimitCount++
		}
	}
	out.TotalBudget, out.TotalSpent = round2(out.TotalBudget), round2(out.TotalSpent)
	if out.TotalBudget > 0 {
		out.UtilizationPct = round2(out.TotalSpent / out.TotalBudget * 100)
	}
	return out, nil
}

// detail — the page at period periodID (nil = the current one).
func (s *Service) detail(ctx context.Context, b *Budget, periodID *uuid.UUID, today time.Time) (*Detail, error) {
	base, err := s.store.baseSeq(ctx, s.store.db, b)
	if err != nil {
		return nil, err
	}
	state := b.state(today, base)
	prs, err := s.loadPeriods(ctx, b, today)
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
	// One query: the recent periods, the viewed one and the ones before it.
	lo := len(prs) - recentPeriods
	if v := view - historyPeriods; v < lo {
		lo = v
	}
	if lo < 0 {
		lo = 0
	}
	live := state == StateActive || state == StateUpcoming
	figs, err := s.periodFigures(ctx, b, prs, lo, len(prs)-1, cur, live, today)
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
	rf, err := s.loadRefs(ctx, []*Budget{b}, f.rows)
	if err != nil {
		return nil, err
	}
	out := &Detail{
		Budget:   budgetView(b, state, periodView(b, prs[view], f, view == cur, today, pace), rf),
		Forecast: fc, BySource: []SourceRow{}, ByCategory: []CategoryRow{},
	}
	// By source / by category.
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
	for _, k := range []string{SourceNormal, SourceRecurring, SourceEvent, SourceUpcoming, SourcePending} {
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
	// The newest periods.
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
	// The first page of items.
	out.Items = pageItems(f.rows, rf, 1, itemsPerPage)
	return out, nil
}

func pageItems(rows []countRow, rf *refs, page, perPage int) ItemList {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = itemsPerPage
	}
	out := ItemList{Data: []Item{}, Pagination: shared.NewPage(page, perPage, len(rows))}
	for i := (page - 1) * perPage; i < len(rows) && i < page*perPage; i++ {
		out.Data = append(out.Data, itemView(rows[i], rf))
	}
	return out
}

func (s *Service) Get(ctx context.Context, userID, id uuid.UUID, periodID *uuid.UUID, tz string) (*Detail, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	b, err := s.store.get(ctx, nil, userID, id, false)
	if err != nil {
		return nil, err
	}
	return s.detail(ctx, b, periodID, today)
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
	base, err := s.store.baseSeq(ctx, s.store.db, b)
	if err != nil {
		return nil, err
	}
	state := b.state(today, base)
	prs, err := s.loadPeriods(ctx, b, today)
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
	// Newest first: page p covers indexes [n-p*pp, n-(p-1)*pp).
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
	figs, err := s.periodFigures(ctx, b, prs, lo, hi, c, live, today)
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
	base, err := s.store.baseSeq(ctx, s.store.db, b)
	if err != nil {
		return nil, err
	}
	state := b.state(today, base)
	prs, err := s.loadPeriods(ctx, b, today)
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
	figs, err := s.periodFigures(ctx, b, prs, view, view, c, state == StateActive || state == StateUpcoming, today)
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
	rf, err := s.loadRefs(ctx, nil, rows)
	if err != nil {
		return nil, err
	}
	out := pageItems(rows, rf, page, perPage)
	return &out, nil
}

// --- writes ---

func parseDate(s string) (time.Time, error) { return time.Parse(dateLayout, s) }

// apply copies the request onto b (validated). Returns whether the amount
// and the time setup changed.
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
	// Time.
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
	return s.Get(ctx, userID, b.ID, nil, tz)
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
	return s.Get(ctx, userID, id, periodID, tz)
}

// Update — PUT /budgets/:id (the setup).
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
		// "ทุกรอบ" with a rule: every period from the start, even when an
		// earlier "this and following" already changed the rule.
		timing = timing || (r.has("rule") && r.Apply == "all")
		if r.has("scope") {
			if err := s.store.checkScope(ctx, tx, userID, b); err != nil {
				return nil, err
			}
		}
		all := r.Apply == "all" || !b.Recurring || !old.Recurring
		// Where "this and following" starts: the period shown now.
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
			if _, err := tx.Exec(ctx, `UPDATE budget_periods SET amount = $2
				WHERE budget_id = $1 AND NOT overridden AND start_date >= $3`, b.ID, b.Amount, from); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
}

// UpdatePeriod — PUT /budgets/:id/periods/:period_id ("เฉพาะรอบนี้").
func (s *Service) UpdatePeriod(ctx context.Context, userID, id, periodID uuid.UUID, r *PeriodRequest, tz string) (*Detail, error) {
	return s.write(ctx, userID, id, tz, func(tx pgx.Tx, b *Budget, _ time.Time) (*uuid.UUID, error) {
		p, err := scanPeriod(tx.QueryRow(ctx, `SELECT `+periodColumns+` FROM budget_periods WHERE id = $1 AND budget_id = $2 FOR UPDATE`,
			periodID, b.ID))
		if err != nil {
			return nil, ErrPeriodNotFound
		}
		if r.present["amount"] {
			if r.Amount == nil {
				p.Amount, p.Overridden = b.Amount, false
			} else if *r.Amount <= 0 {
				return nil, ErrValidation
			} else {
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

// Close — ปิดงบ: counting stops after today.
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

// healthMatches — the list's `health` filter on a period status: normal =
// ok or pace, near = warn, over = over ("" = everything).
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
