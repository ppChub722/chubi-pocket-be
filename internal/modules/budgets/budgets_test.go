package budgets

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

type budFix struct {
	t      *testing.T
	ctx    context.Context
	pool   *pgxpool.Pool
	me     uuid.UUID
	wallet uuid.UUID
	food   uuid.UUID
	cafe   uuid.UUID // child of food
	travel uuid.UUID
	today  time.Time
	r      http.Handler
}

func newBudFix(t *testing.T) *budFix {
	t.Helper()
	pool := testdb.Pool(t)
	f := &budFix{t: t, ctx: context.Background(), pool: pool}
	f.me = testdb.User(t, pool)
	f.wallet = testdb.Account(t, pool, f.me)
	f.food = testdb.Category(t, pool, f.me, "Food")
	f.cafe = testdb.Category(t, pool, f.me, "Cafe")
	f.exec(`UPDATE categories SET parent_id = $2 WHERE id = $1`, f.cafe, f.food)
	f.travel = testdb.Category(t, pool, f.me, "Travel")
	loc, _ := time.LoadLocation("Asia/Bangkok")
	now := time.Now().In(loc)
	f.today = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	h := NewHandler(NewService(NewStore(pool)))
	f.r = apitest.Engine(f.me, func(g *gin.RouterGroup) {
		g.GET("/budgets", h.List)
		g.POST("/budgets", h.Create)
		g.GET("/budgets/:id/periods", h.Periods)
		g.PUT("/budgets/:id/periods/:period_id", h.UpdatePeriod)
		g.GET("/budgets/:id/items", h.Items)
		g.POST("/budgets/:id/close", h.Close)
		g.GET("/budgets/:id", h.Get)
		g.PUT("/budgets/:id", h.Update)
		g.DELETE("/budgets/:id", h.Delete)
		g.POST("/budgets/:id/archive", h.Archive)
		g.POST("/budgets/:id/restore", h.Restore)
	})
	t.Cleanup(func() {
		_, _ = pool.Exec(f.ctx, `DELETE FROM budgets WHERE user_id = $1`, f.me)
		_, _ = pool.Exec(f.ctx, `DELETE FROM recurring_items WHERE user_id = $1`, f.me)
		_, _ = pool.Exec(f.ctx, `DELETE FROM pending_transactions WHERE user_id = $1`, f.me)
		_, _ = pool.Exec(f.ctx, `DELETE FROM projects WHERE owner_user_id = $1`, f.me)
	})
	return f
}

func (f *budFix) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, q, args...); err != nil {
		f.t.Fatalf("exec %q: %v", q, err)
	}
}

func (f *budFix) day(n int) string { return f.today.AddDate(0, 0, n).Format(dateLayout) }

func (f *budFix) call(method, path, op string, body any, want int, out any) []byte {
	f.t.Helper()
	if body != nil && op != "" && (method == "POST" || method == "PUT") {
		apitest.ConformRequest(f.t, op, body)
	}
	status, raw := apitest.Do(f.t, f.r, method, "/api/v1"+path, body)
	if status != want {
		f.t.Fatalf("%s %s: status %d, want %d: %s", method, path, status, want, raw)
	}
	if op != "" {
		apitest.Conform(f.t, op, status, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			f.t.Fatal(err)
		}
	}
	return raw
}

func (f *budFix) tx(amount float64, cat *uuid.UUID, date string, tags ...string) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	f.exec(`INSERT INTO transactions (id, user_id, account_id, type, amount, category_id, date)
		VALUES ($1, $2, $3, 'expense', $4, $5, $6)`, id, f.me, f.wallet, amount, cat, date)
	for _, name := range tags {
		var tag uuid.UUID
		if err := f.pool.QueryRow(f.ctx, `INSERT INTO tags (id, user_id, name) VALUES (gen_random_uuid(), $1, $2)
			ON CONFLICT DO NOTHING RETURNING id`, f.me, name).Scan(&tag); err != nil {
			_ = f.pool.QueryRow(f.ctx, `SELECT id FROM tags WHERE user_id = $1 AND lower(name) = lower($2)`, f.me, name).Scan(&tag)
		}
		f.exec(`INSERT INTO transaction_tags (transaction_id, tag_id) VALUES ($1, $2)`, id, tag)
	}
	return id
}

func src(d Detail, s string) *SourceRow {
	for i := range d.BySource {
		if d.BySource[i].Source == s {
			return &d.BySource[i]
		}
	}
	return nil
}

func TestBudgetRecurringCategory(t *testing.T) {
	f := newBudFix(t)
	monthStart := time.Date(f.today.Year(), f.today.Month(), 1, 0, 0, 0, 0, time.UTC)
	start := monthStart.AddDate(0, -2, 0)
	var d Detail
	f.call("POST", "/budgets", "createBudget", map[string]any{
		"name": "Food", "amount": 1000, "recurring": true, "rule": map[string]any{"freq": "monthly"},
		"start_date": start.Format(dateLayout),
		"scope":      map[string]any{"categories": []map[string]any{{"id": f.food}}},
	}, http.StatusCreated, &d)
	id := d.Budget.ID.String()
	if d.Budget.WarnPct == nil || *d.Budget.WarnPct != 80 || d.Budget.Period.Seq != 3 || len(d.Periods.Data) != 3 {
		t.Fatalf("created = %+v (periods %d)", d.Budget, len(d.Periods.Data))
	}

	cur := f.today.Format(dateLayout)
	f.tx(300, &f.food, cur)
	f.tx(200, &f.cafe, cur)                                             // a sub-category counts
	f.tx(999, &f.travel, cur)                                           // out of scope
	f.tx(400, &f.food, monthStart.AddDate(0, -1, 0).Format(dateLayout)) // last month
	end := monthStart.AddDate(0, 1, -1)
	if f.today.Before(end) {
		f.exec(`INSERT INTO recurring_items (id, user_id, date, draft) VALUES (gen_random_uuid(), $1, $2,
			jsonb_build_object('type', 'expense', 'amount', 50, 'category_id', $3::text))`, f.me, end, f.food)
	}
	f.exec(`INSERT INTO pending_transactions (id, user_id, source, kind, draft) VALUES (gen_random_uuid(), $1, 'manual', 'create',
		jsonb_build_object('type', 'expense', 'amount', 20, 'date', $2::text, 'category_id', $3::text))`, f.me, cur, f.food)

	f.call("GET", "/budgets/"+id, "getBudget", nil, http.StatusOK, &d)
	p := d.Budget.Period
	if p.Used != 500 || p.Pct != 50 || p.Status == PeriodOver || !p.IsCurrent {
		t.Fatalf("current period = %+v", p)
	}
	if s := src(d, SourcePending); s == nil || s.Amount != 20 || s.CountsAs != "expected" {
		t.Errorf("pending source = %+v", s)
	}
	if f.today.Before(end) {
		if s := src(d, SourceUpcoming); s == nil || s.Amount != 50 || p.Expected != 70 {
			t.Errorf("upcoming = %+v, expected %v", s, p.Expected)
		}
	}
	if len(d.ByCategory) != 2 || d.ByCategory[0].Amount != 300 || d.ByCategory[0].Pct != 60 {
		t.Errorf("by_category = %+v", d.ByCategory)
	}
	if d.Items.Pagination.Total < 3 || d.Items.Data[0].CountsAs != "expected" {
		t.Errorf("items = %+v", d.Items)
	}
	if d.Forecast.Available != 500 || d.Forecast.PacePct == nil || *d.Forecast.PacePct < 0 || *d.Forecast.PacePct > 100 ||
		d.Forecast.DaysLeft == nil || d.Forecast.ProjectedEnd == nil {
		t.Errorf("forecast = %+v", d.Forecast)
	}
	reasons := map[string]int{}
	for _, it := range d.Items.Data {
		if it.CountsAs == "used" {
			reasons[it.Reason]++
		}
	}
	if reasons[ReasonCategory] != 1 || reasons[ReasonSubCategory] != 1 {
		t.Errorf("reasons = %v, want one category (Food) and one sub_category (Cafe)", reasons)
	}
	if d.Periods.Data[1].Used != 400 || d.Periods.Data[1].IsCurrent {
		t.Errorf("last month = %+v", d.Periods.Data[1])
	}

	f.tx(600, &f.food, cur)
	var l List
	f.call("GET", "/budgets", "listBudgets", nil, http.StatusOK, &l)
	if len(l.Data) != 1 || l.Data[0].Period.Status != PeriodOver || l.Data[0].Period.Tone != "danger" ||
		l.Summary.OverCount != 1 || l.Data[0].Period.OverBy != 100 {
		t.Fatalf("list = %+v", l)
	}

	last := d.Periods.Data[1]
	f.call("PUT", "/budgets/"+id+"/periods/"+last.ID.String(), "updateBudgetPeriod",
		map[string]any{"amount": 1500, "note": "trip month"}, http.StatusOK, &d)
	if !d.Budget.Period.Overridden || d.Budget.Period.Amount != 1500 || d.Budget.Period.ID != last.ID {
		t.Fatalf("period edit = %+v", d.Budget.Period)
	}
	f.call("PUT", "/budgets/"+id, "updateBudget", map[string]any{"amount": 2000}, http.StatusOK, &d)
	byStart := func(d Detail) map[int]Period {
		m := map[int]Period{}
		for _, p := range d.Periods.Data {
			m[p.Seq] = p
		}
		return m
	}
	m := byStart(d)
	if m[3].Amount != 2000 || m[2].Amount != 1500 || m[1].Amount != 1000 {
		t.Errorf("following: %v / %v / %v", m[1].Amount, m[2].Amount, m[3].Amount)
	}
	f.call("PUT", "/budgets/"+id, "updateBudget", map[string]any{"amount": 2500, "apply": "all"}, http.StatusOK, &d)
	m = byStart(d)
	if m[3].Amount != 2500 || m[2].Amount != 1500 || m[1].Amount != 2500 {
		t.Errorf("all: %v / %v / %v (the overridden one keeps 1500)", m[1].Amount, m[2].Amount, m[3].Amount)
	}
	var pl PeriodList
	f.call("GET", "/budgets/"+id+"/periods", "listBudgetPeriods", nil, http.StatusOK, &pl)
	if len(pl.Data) != 3 || pl.Data[0].Seq != 3 {
		t.Errorf("periods = %+v", pl.Data)
	}
	var il ItemList
	f.call("GET", "/budgets/"+id+"/items?source=normal&period_id="+last.ID.String(), "listBudgetItems", nil, http.StatusOK, &il)
	if len(il.Data) != 1 || il.Data[0].Amount != 400 || il.Data[0].Category == nil {
		t.Errorf("items of last month = %+v", il.Data)
	}

	f.call("POST", "/budgets/"+id+"/close", "closeBudget", nil, http.StatusOK, &d)
	if d.Budget.State != StateEnded || d.Budget.ClosedOn == nil {
		t.Fatalf("close = %s", d.Budget.State)
	}
	f.call("POST", "/budgets/"+id+"/close", "closeBudget", nil, http.StatusConflict, nil)
	f.call("GET", "/budgets?status=ended", "listBudgets", nil, http.StatusOK, &l)
	if len(l.Data) != 1 || l.Summary.EndedCount != 1 || l.Summary.ActiveCount != 0 {
		t.Errorf("ended list = %+v", l.Summary)
	}
	f.call("POST", "/budgets/"+id+"/archive", "archiveBudget", nil, http.StatusOK, &d)
	f.call("POST", "/budgets/"+id+"/restore", "restoreBudget", nil, http.StatusOK, &d)
	f.call("DELETE", "/budgets/"+id, "deleteBudget", nil, http.StatusOK, nil)
	f.call("GET", "/budgets/"+id, "", nil, http.StatusNotFound, nil)
}

func TestBudgetTagRangeWithEvent(t *testing.T) {
	f := newBudFix(t)
	var d Detail
	f.call("POST", "/budgets", "createBudget", map[string]any{
		"name": "Chiang Mai", "amount": 1000, "warn_pct": nil, "start_date": f.day(-4), "end": map[string]any{"kind": "date", "date": f.day(5)},
		"scope": map[string]any{"tags": []string{"Trip"}},
	}, http.StatusCreated, &d)
	id := d.Budget.ID.String()
	f.tx(100, &f.travel, f.day(0), "trip")
	f.tx(50, &f.travel, f.day(0), "other")

	ev, meM, beeM := uuid.New(), uuid.New(), uuid.New()
	f.exec(`INSERT INTO projects (id, owner_user_id, name, status) VALUES ($1, $2, 'CNX', 'active')`, ev, f.me)
	f.exec(`INSERT INTO project_members (id, project_id, user_id, display_name, role, status) VALUES
		($1, $2, $3, 'Me', 'owner', 'active'), ($4, $2, NULL, 'Bee', 'contributor', 'active')`, meM, ev, f.me, beeM)
	tag := uuid.New()
	f.exec(`INSERT INTO event_tags (id, project_id, name) VALUES ($1, $2, 'trip')`, tag, ev)
	b1, c1 := uuid.New(), uuid.New()
	f.exec(`INSERT INTO project_transactions (id, project_id, transaction_member_id, record_user_id, type, amount, currency, date) VALUES
		($1, $2, $3, $4, 'expense', 300, 'THB', $5)`, b1, ev, beeM, f.me, f.day(-1))
	f.exec(`INSERT INTO project_transactions (id, project_id, transaction_member_id, record_user_id, type, amount, currency, date, parent_project_transaction_id) VALUES
		($1, $2, $3, $4, 'expense', 100, 'THB', $5, $6)`, c1, ev, meM, f.me, f.day(-1), b1)
	f.exec(`INSERT INTO project_transaction_tags (project_transaction_id, event_tag_id) VALUES ($1, $2)`, b1, tag)
	mine := f.tx(200, &f.food, f.day(-2), "trip")
	b2 := uuid.New()
	f.exec(`INSERT INTO project_transactions (id, project_id, transaction_member_id, record_user_id, type, amount, currency, date, linked_transaction_id) VALUES
		($1, $2, $3, $4, 'expense', 200, 'THB', $5, $6)`, b2, ev, meM, f.me, f.day(-2), mine)
	f.exec(`INSERT INTO project_transactions (id, project_id, transaction_member_id, record_user_id, type, amount, currency, date, parent_project_transaction_id) VALUES
		(gen_random_uuid(), $1, $2, $3, 'expense', 80, 'THB', $4, $5)`, ev, beeM, f.me, f.day(-2), b2)

	f.call("GET", "/budgets/"+id, "getBudget", nil, http.StatusOK, &d)
	p := d.Budget.Period
	if p.Used != 320 { // 100 (tx) + 100 (Bee's bill) + 120 (my bill, not my 200 tx)
		t.Fatalf("used = %v, want 320; sources %+v", p.Used, d.BySource)
	}
	if s := src(d, SourceEvent); s == nil || s.Count != 2 || s.Amount != 220 {
		t.Errorf("event source = %+v", s)
	}
	if d.Budget.Recurring || d.Budget.Period.End == nil || d.Forecast.TotalDays == nil || *d.Forecast.TotalDays != 10 {
		t.Errorf("range = %+v / %+v", d.Budget, d.Forecast)
	}
	if d.Forecast.AvgDaily == nil || *d.Forecast.AvgDaily != 64 || d.Forecast.ForecastOverBy != nil || p.Status != PeriodOK {
		t.Errorf("forecast = %+v status %s", d.Forecast, p.Status)
	}
	for _, it := range d.Items.Data {
		if it.Source == SourceEvent && (it.Event == nil || it.Event.ID != ev) {
			t.Errorf("event item = %+v", it)
		}
	}
	f.tx(380, &f.travel, f.day(0), "Trip")
	f.call("GET", "/budgets/"+id, "getBudget", nil, http.StatusOK, &d)
	if d.Budget.Period.Status != PeriodPace || d.Forecast.ForecastOverDate == nil || d.Forecast.ForecastOverBy == nil {
		t.Errorf("pace = %s / %+v", d.Budget.Period.Status, d.Forecast)
	}
}

func TestBudgetOpenEndedAndValidation(t *testing.T) {
	f := newBudFix(t)
	if raw := f.call("POST", "/budgets", "", map[string]any{"name": "X", "amount": 10, "start_date": f.day(0),
		"scope": map[string]any{}}, http.StatusBadRequest, nil); !json.Valid(raw) {
		t.Fatal("bad json")
	}
	f.call("POST", "/budgets", "", map[string]any{"name": "X", "amount": 10, "start_date": f.day(0), "recurring": true,
		"rule": map[string]any{"freq": "hourly"}, "scope": map[string]any{"all": true}}, http.StatusBadRequest, nil)
	f.call("POST", "/budgets", "", map[string]any{"name": "X", "amount": 10, "start_date": f.day(0),
		"scope": map[string]any{"categories": []map[string]any{{"id": uuid.New()}}}}, http.StatusBadRequest, nil)

	var d Detail
	f.call("POST", "/budgets", "createBudget", map[string]any{
		"name": "Renovation", "amount": 10000, "start_date": f.day(-9), "scope": map[string]any{"wallet_ids": []uuid.UUID{f.wallet}},
	}, http.StatusCreated, &d)
	f.tx(1000, &f.travel, f.day(0))
	f.call("GET", "/budgets/"+d.Budget.ID.String(), "getBudget", nil, http.StatusOK, &d)
	if d.Budget.Period.End != nil || d.Budget.Period.Used != 1000 || d.Forecast.AvgMonthly == nil ||
		*d.Forecast.AvgMonthly != 3000 || d.Forecast.ThisMonth == nil || d.Budget.Period.DaysLeft != nil {
		t.Errorf("open-ended = %+v / %+v", d.Budget.Period, d.Forecast)
	}
	if len(d.Budget.Scope.Wallets) != 1 || d.Budget.Scope.Wallets[0].ID != f.wallet {
		t.Errorf("scope = %+v", d.Budget.Scope)
	}
	f.call("POST", "/budgets", "createBudget", map[string]any{
		"name": "New year", "amount": 5000, "start_date": f.day(40), "end": map[string]any{"kind": "date", "date": f.day(70)},
		"scope": map[string]any{"all": true},
	}, http.StatusCreated, &d)
	if d.Budget.State != StateUpcoming || d.Budget.Period.Used != 0 {
		t.Errorf("upcoming = %+v", d.Budget)
	}
	var l List
	f.call("GET", "/budgets?sort=name", "listBudgets", nil, http.StatusOK, &l)
	if len(l.Data) != 2 || l.Data[0].Name != "New year" || l.Summary.ActiveCount != 2 {
		t.Errorf("list = %+v", l.Summary)
	}
}

func TestBudgetRuleChangeScopes(t *testing.T) {
	f := newBudFix(t)
	var d Detail
	f.call("POST", "/budgets", "createBudget", map[string]any{
		"name": "Coffee", "amount": 500, "recurring": true, "rule": map[string]any{"freq": "weekly"},
		"start_date": f.day(-20), "scope": map[string]any{"all": true},
	}, http.StatusCreated, &d)
	id := d.Budget.ID.String()
	if d.Budget.Period.Seq != 3 {
		t.Fatalf("weekly from 20 days ago: current seq %d", d.Budget.Period.Seq)
	}
	f.call("PUT", "/budgets/"+id, "updateBudget", map[string]any{"rule": map[string]any{"freq": "daily", "interval": 2}}, http.StatusOK, &d)
	p := d.Budget.Period
	seqStart := map[int]string{}
	for _, q := range d.Periods.Data {
		seqStart[q.Seq] = q.Start
	}
	if d.Budget.Rule == nil || d.Budget.Rule.Freq != "daily" || p.Seq != 6 || p.Start != f.day(0) ||
		seqStart[2] != f.day(-13) || seqStart[3] != f.day(-6) {
		t.Fatalf("following: rule %+v current %+v previous %+v", d.Budget.Rule, p, d.Periods.Data[1])
	}
	f.call("PUT", "/budgets/"+id, "updateBudget", map[string]any{"rule": map[string]any{"freq": "daily", "interval": 2}, "apply": "all"}, http.StatusOK, &d)
	if d.Budget.Period.Seq != 11 || d.Periods.Data[len(d.Periods.Data)-1].Start == f.day(-13) {
		t.Errorf("all: current %+v", d.Budget.Period)
	}
}

func TestBudgetListSummaryCard(t *testing.T) {
	f := newBudFix(t)
	monthStart := time.Date(f.today.Year(), f.today.Month(), 1, 0, 0, 0, 0, time.UTC).Format(dateLayout)
	mk := func(name string, amount float64, cat uuid.UUID) {
		f.call("POST", "/budgets", "createBudget", map[string]any{
			"name": name, "amount": amount, "recurring": true, "rule": map[string]any{"freq": "monthly"},
			"start_date": monthStart,
			"scope":      map[string]any{"categories": []map[string]any{{"id": cat}}},
		}, http.StatusCreated, nil)
	}
	mk("Food", 1000, f.food)
	mk("Cafe", 300, f.cafe) // a sub-category of Food: the two scopes overlap
	cur := f.today.Format(dateLayout)
	f.tx(300, &f.food, cur)
	f.tx(200, &f.cafe, cur) // counted by BOTH budgets

	var l List
	f.call("GET", "/budgets", "listBudgets", nil, http.StatusOK, &l)
	s := l.Summary
	if s.TotalLimit != 1300 || s.TotalUsed != 500 || s.TotalRemaining != 800 || s.UsedPct != 38 ||
		s.Counts != (HealthCounts{All: 2, OK: 2}) {
		t.Fatalf("summary = %+v", s)
	}
	sumOfRows := 0.0
	for _, v := range l.Data {
		sumOfRows += v.Period.Used
	}
	if sumOfRows != 700 {
		t.Errorf("rows' used add up to %v, want 700 (the overlap counted by both)", sumOfRows)
	}

	f.tx(200, &f.cafe, cur) // Cafe 400 / 300 → over
	f.call("GET", "/budgets?health=over", "listBudgets", nil, http.StatusOK, &l)
	if len(l.Data) != 1 || l.Data[0].Name != "Cafe" || l.Summary.Counts.Over != 1 || l.Summary.Counts.All != 2 ||
		l.Summary.TotalUsed != 700 || l.Summary.Counts.OK != 1 {
		t.Errorf("health=over: rows %d summary %+v", len(l.Data), l.Summary)
	}
	f.call("GET", "/budgets?health=normal", "listBudgets", nil, http.StatusOK, &l)
	if len(l.Data) != 1 || l.Data[0].Name != "Food" {
		t.Errorf("health=normal rows = %d", len(l.Data))
	}
	f.call("GET", "/budgets?health=near", "listBudgets", nil, http.StatusOK, &l)
	if len(l.Data) != 0 || l.Summary.Counts.All != 2 {
		t.Errorf("health=near rows = %d summary %+v", len(l.Data), l.Summary)
	}
	f.call("GET", "/budgets?health=nope", "", nil, http.StatusBadRequest, nil)
}
