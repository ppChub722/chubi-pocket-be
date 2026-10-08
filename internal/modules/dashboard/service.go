package dashboard

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/accounts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/budgets"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/saving_goals"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

const dateLayout = "2006-01-02"

var ErrInvalidMonth = errors.New("month must be YYYY-MM")

// Service aggregates the home screen in one call (contract §4). It owns
// no money rules: totals come from the modules that define them, so the
// dashboard can never disagree with the screen behind each tile.
type Service struct {
	store        *Store
	transactions *transactions.Service
	accounts     *accounts.Service
	budgets      *budgets.Service
	goals        *saving_goals.Service
	debts        *personal_debts.Service
}

func NewService(
	store *Store,
	tx *transactions.Service,
	acc *accounts.Service,
	bud *budgets.Service,
	goals *saving_goals.Service,
	debts *personal_debts.Service,
) *Service {
	return &Service{store: store, transactions: tx, accounts: acc, budgets: bud, goals: goals, debts: debts}
}

// Get builds the dashboard for `month` (YYYY-MM; "" = current month in
// the caller's timezone).
func (s *Service) Get(ctx context.Context, userID uuid.UUID, month, headerTZ string) (*Response, error) {
	loc, _, err := s.budgets.ResolveTimezone(ctx, userID, headerTZ)
	if err != nil {
		return nil, err
	}
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	if month != "" {
		m, err := time.ParseInLocation("2006-01", month, loc)
		if err != nil {
			return nil, ErrInvalidMonth
		}
		start = m
	}
	end := start.AddDate(0, 1, -1)

	out := &Response{
		Month:         start.Format("2006-01"),
		From:          start.Format(dateLayout),
		To:            end.Format(dateLayout),
		Today:         today.Format(dateLayout),
		Currency:      "THB", // single-currency assumption, same as /transactions/summary
		TopCategories: []CategorySlice{},
		Trend:         []TrendPoint{},
		Upcoming:      Upcoming{Days: upcomingDays, Items: []UpcomingItem{}},
		Recent:        []transactions.TransactionDetail{},
	}

	// Each block writes only its own fields, so the goroutines never
	// share state.
	g, gctx := errgroup.WithContext(ctx)
	var accts []accounts.Account
	g.Go(func() error {
		var err error
		accts, err = s.accounts.List(gctx, userID, accounts.StatusActive, "")
		return err
	})
	g.Go(func() error { return s.fillSummary(gctx, userID, start, end, out) })
	g.Go(func() error { return s.fillCategories(gctx, userID, start, end, out) })
	g.Go(func() error { return s.fillTrend(gctx, userID, start, end, out) })
	g.Go(func() error { return s.fillBudgets(gctx, userID, headerTZ, out) })
	g.Go(func() error { return s.fillDebts(gctx, userID, out) })
	g.Go(func() error { return s.fillGoals(gctx, userID, out) })
	g.Go(func() error { return s.fillRecent(gctx, userID, out) })
	var scheduled []scheduledRow
	g.Go(func() error {
		var err error
		scheduled, err = s.store.DueScheduled(gctx, userID, today.AddDate(0, 0, upcomingDays).Format(dateLayout))
		return err
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}

	out.NetWorth = netWorth(accts)
	out.Upcoming = buildUpcoming(today, scheduled, accts)
	return out, nil
}

func (s *Service) summary(ctx context.Context, userID uuid.UUID, from, to time.Time, groupBy string, txType *transactions.TxType) (*transactions.SummaryResponse, error) {
	return s.transactions.Summary(ctx, userID, transactions.SummaryRequest{
		From: from.Format(dateLayout), To: to.Format(dateLayout), GroupBy: groupBy, Type: txType,
	})
}

func toTotals(r *transactions.SummaryResponse) Totals {
	return Totals{Income: r.TotalIncome, Expense: r.TotalExpense, Net: r.Net, TransactionCount: r.TransactionCount}
}

func (s *Service) fillSummary(ctx context.Context, userID uuid.UUID, start, end time.Time, out *Response) error {
	cur, err := s.summary(ctx, userID, start, end, "", nil)
	if err != nil {
		return err
	}
	prevStart := start.AddDate(0, -1, 0)
	prev, err := s.summary(ctx, userID, prevStart, start.AddDate(0, 0, -1), "", nil)
	if err != nil {
		return err
	}
	out.Summary, out.Previous = toTotals(cur), toTotals(prev)
	return nil
}

func (s *Service) fillCategories(ctx context.Context, userID uuid.UUID, start, end time.Time, out *Response) error {
	expense := transactions.TypeExpense
	r, err := s.summary(ctx, userID, start, end, "parent_category", &expense)
	if err != nil {
		return err
	}
	ids := make([]uuid.UUID, 0, topCategoriesMax)
	for i, grp := range r.Groups {
		if i >= topCategoriesMax {
			out.OtherExpense += grp.Expense
			continue
		}
		slice := CategorySlice{Name: grp.Name, Expense: grp.Expense, Count: grp.Count}
		if id, err := uuid.Parse(grp.Key); err == nil {
			slice.CategoryID = &id
			ids = append(ids, id)
		}
		out.TopCategories = append(out.TopCategories, slice)
	}
	icons, err := s.store.CategoryIcons(ctx, userID, ids)
	if err != nil {
		return err
	}
	for i := range out.TopCategories {
		if id := out.TopCategories[i].CategoryID; id != nil {
			out.TopCategories[i].IconCode = icons[*id]
		}
	}
	out.OtherExpense = round2(out.OtherExpense)
	return nil
}

// fillTrend — one point per month, zero-filled, ending at the selected month.
func (s *Service) fillTrend(ctx context.Context, userID uuid.UUID, start, end time.Time, out *Response) error {
	first := start.AddDate(0, -(trendMonths - 1), 0)
	r, err := s.summary(ctx, userID, first, end, "month", nil)
	if err != nil {
		return err
	}
	byMonth := make(map[string]transactions.SummaryGroup, len(r.Groups))
	for _, grp := range r.Groups {
		if len(grp.Key) >= 7 {
			byMonth[grp.Key[:7]] = grp
		}
	}
	for i := 0; i < trendMonths; i++ {
		m := first.AddDate(0, i, 0).Format("2006-01")
		grp := byMonth[m]
		out.Trend = append(out.Trend, TrendPoint{Month: m, Income: grp.Income, Expense: grp.Expense})
	}
	return nil
}

// fillBudgets — every active personal budget at its own current period
// (weekly / monthly / yearly alike), not just monthly ones.
func (s *Service) fillBudgets(ctx context.Context, userID uuid.UUID, headerTZ string, out *Response) error {
	r, err := s.budgets.Overview(ctx, userID, "", budgets.ScopeUser, nil, headerTZ)
	if err != nil {
		return err
	}
	out.Budgets = BudgetsTile{
		Count:          len(r.Budgets),
		TotalBudget:    r.TotalBudget,
		TotalSpent:     r.TotalSpent,
		UtilizationPct: r.OverallUtilizationPct,
		OverLimitCount: r.BudgetsOverLimit,
	}
	return nil
}

func (s *Service) fillDebts(ctx context.Context, userID uuid.UUID, out *Response) error {
	r, err := s.debts.People(ctx, userID)
	if err != nil {
		return err
	}
	t := DebtsTile{OwedToMe: r.TotalOwedToMe, IOwe: r.TotalIOwe, Net: r.NetPosition}
	for _, p := range r.Data {
		t.OpenCount += p.OpenCount
	}
	out.Debts = t
	return nil
}

func (s *Service) fillGoals(ctx context.Context, userID uuid.UUID, out *Response) error {
	r, err := s.goals.List(ctx, userID, saving_goals.ListFilter{Status: "active"})
	if err != nil {
		return err
	}
	var t GoalsTile
	for _, g := range r.Data {
		t.Count++
		if g.IsCompleted {
			t.CompletedCount++
		}
		t.TotalTarget += g.TargetAmount
		t.TotalCurrent += math.Min(g.CurrentAmount, g.TargetAmount)
	}
	if t.TotalTarget > 0 {
		t.ProgressPct = round2(t.TotalCurrent / t.TotalTarget * 100)
	}
	out.SavingGoals = t
	return nil
}

func (s *Service) fillRecent(ctx context.Context, userID uuid.UUID, out *Response) error {
	r, err := s.transactions.List(ctx, userID, transactions.ListFilter{Page: 1, PerPage: recentMax, Sort: "date_desc"})
	if err != nil {
		return err
	}
	out.Recent = r.Data
	return nil
}

// netWorth — accounts the caller reports on (scope 'none' wallets are
// hidden from personal figures, spec §14/5). Positive balances are
// assets, negative ones (credit cards, overdrafts) liabilities.
func netWorth(accts []accounts.Account) NetWorth {
	var nw NetWorth
	for _, a := range accts {
		if a.MyReportScope == accounts.ReportScopeNone {
			continue
		}
		nw.AccountsCount++
		if a.Balance >= 0 {
			nw.Assets += a.Balance
		} else {
			nw.Liabilities -= a.Balance
		}
	}
	nw.Assets, nw.Liabilities = round2(nw.Assets), round2(nw.Liabilities)
	nw.Total = round2(nw.Assets - nw.Liabilities)
	return nw
}

// buildUpcoming merges scheduled items (overdue included) with credit
// card / pay-later due dates inside the window. A card only shows when
// it has an outstanding balance; its amount is that balance.
func buildUpcoming(today time.Time, scheduled []scheduledRow, accts []accounts.Account) Upcoming {
	up := Upcoming{Days: upcomingDays, Items: make([]UpcomingItem, 0)}
	for _, r := range scheduled {
		due, err := time.ParseInLocation(dateLayout, r.DueDate, today.Location())
		if err != nil {
			continue
		}
		days := daysBetween(today, due)
		up.Items = append(up.Items, UpcomingItem{
			Kind: KindScheduled, ID: r.ID, Name: r.Name, Type: r.Type, Amount: r.Amount,
			DueDate: r.DueDate, DaysUntil: days, Overdue: days < 0,
			IconCode: r.IconCode, LogoURL: r.LogoURL,
		})
	}
	for _, a := range accts {
		if !accounts.IsCreditType(a.Type) || a.PaymentDueDate == nil || a.Balance >= 0 {
			continue
		}
		due := nextDayOfMonth(today, *a.PaymentDueDate)
		days := daysBetween(today, due)
		if days > upcomingDays {
			continue
		}
		up.Items = append(up.Items, UpcomingItem{
			Kind: KindCardDue, ID: a.ID, Name: a.Name, Type: string(transactions.TypeExpense),
			Amount: round2(-a.Balance), DueDate: due.Format(dateLayout), DaysUntil: days,
			IconCode: a.IconCode, LogoURL: a.LogoURL,
		})
	}
	sort.SliceStable(up.Items, func(i, j int) bool { return up.Items[i].DueDate < up.Items[j].DueDate })
	if len(up.Items) > upcomingMax {
		up.Items = up.Items[:upcomingMax]
	}
	for _, it := range up.Items {
		if it.Type == string(transactions.TypeIncome) {
			up.TotalIncome += it.Amount
		} else {
			up.TotalExpense += it.Amount
		}
	}
	up.TotalIncome, up.TotalExpense = round2(up.TotalIncome), round2(up.TotalExpense)
	return up
}

// nextDayOfMonth — the next date (today included) falling on `day`,
// clamped to the month's last day (31 → 30 Apr, 28/29 Feb).
func nextDayOfMonth(today time.Time, day int) time.Time {
	at := func(y int, m time.Month) time.Time {
		last := time.Date(y, m+1, 0, 0, 0, 0, 0, today.Location()).Day()
		return time.Date(y, m, min(day, last), 0, 0, 0, 0, today.Location())
	}
	d := at(today.Year(), today.Month())
	if d.Before(today) {
		d = at(today.Year(), today.Month()+1)
	}
	return d
}

func daysBetween(from, to time.Time) int {
	// Dates are local midnights; rounding absorbs DST hour shifts.
	return int(math.Round(to.Sub(from).Hours() / 24))
}

func round2(f float64) float64 {
	return math.Round(f*100) / 100
}
