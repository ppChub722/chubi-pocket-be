package saving_goals

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type goalFix struct {
	t       *testing.T
	ctx     context.Context
	pool    *pgxpool.Pool
	me      uuid.UUID
	kbank   uuid.UUID
	scb     uuid.UUID
	untrack uuid.UUID
	today   time.Time
	r       http.Handler
}

func newGoalFix(t *testing.T) *goalFix {
	t.Helper()
	pool := testdb.StatsPool(t)
	f := &goalFix{t: t, ctx: context.Background(), pool: pool}
	f.me = testdb.User(t, pool)
	f.kbank, f.scb, f.untrack = testdb.Account(t, pool, f.me), testdb.Account(t, pool, f.me), testdb.Account(t, pool, f.me)
	f.exec(`UPDATE accounts SET balance = 190000, name = 'KBank' WHERE id = $1`, f.kbank)
	f.exec(`UPDATE accounts SET balance = 30000, name = 'SCB' WHERE id = $1`, f.scb)
	f.exec(`UPDATE accounts SET track_balance = FALSE WHERE id = $1`, f.untrack)
	loc, _ := time.LoadLocation("Asia/Bangkok")
	now := time.Now().In(loc)
	f.today = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	h := NewHandler(NewService(NewStore(pool)))
	f.r = apitest.CountedEngine(f.me, func(g *gin.RouterGroup) {
		g.GET("/saving-goals", h.List)
		g.POST("/saving-goals", h.Create)
		g.GET("/saving-goals/spare", h.Spare)
		g.GET("/saving-goals/:id", h.Get)
		g.PUT("/saving-goals/:id", h.Update)
		g.DELETE("/saving-goals/:id", h.Delete)
		g.PUT("/saving-goals/:id/auto-saves", h.PutAutoSaves)
		g.PUT("/saving-goals/spare/auto-saves", h.PutAutoSavesSpare)
		g.PUT("/saving-goals/:id/auto-save", h.PutAutoSave)
		g.DELETE("/saving-goals/:id/auto-save", h.DeleteAutoSave)
		g.PUT("/saving-goals/spare/auto-save", h.PutAutoSaveSpare)
		g.DELETE("/saving-goals/spare/auto-save", h.DeleteAutoSaveSpare)
		g.POST("/saving-goals/:id/close", h.Close)
		g.POST("/saving-goals/:id/archive", h.Archive)
		g.POST("/saving-goals/:id/restore", h.Restore)
		g.GET("/saving-entries", h.Entries)
		g.POST("/saving-entries", h.CreateEntry)
		g.PUT("/saving-entries/:id", h.UpdateEntry)
		g.DELETE("/saving-entries/:id", h.DeleteEntry)
	})
	return f
}

func (f *goalFix) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, q, args...); err != nil {
		f.t.Fatalf("exec %q: %v", q, err)
	}
}

func (f *goalFix) call(method, path, op string, body any, want int, out any) []byte {
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

func (f *goalFix) goal(name string, target float64, deadline *string) uuid.UUID {
	f.t.Helper()
	body := map[string]any{"name": name, "target_amount": target}
	if deadline != nil {
		body["deadline"] = *deadline
	}
	var d GoalDetail
	f.call("POST", "/saving-goals", "createSavingGoal", body, http.StatusCreated, &d)
	return d.Goal.ID
}

func (f *goalFix) save(wallet uuid.UUID, goal *uuid.UUID, amount float64) WriteResult {
	f.t.Helper()
	var w WriteResult
	f.call("POST", "/saving-entries", "createSavingEntry", map[string]any{
		"kind": "save", "amount": amount, "wallet_id": wallet, "goal_id": goal,
	}, http.StatusCreated, &w)
	return w
}

func walletOf(w WriteResult, id uuid.UUID) *shared.WalletSavings {
	for _, x := range w.Wallets {
		if x.Wallet.ID == id {
			return &x.Savings
		}
	}
	return nil
}

func errCode(raw []byte) string {
	var e struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	return e.Error.Code
}

func TestSavingsNotebook(t *testing.T) {
	f := newGoalFix(t)
	dl := f.today.AddDate(0, 5, 0).Format(dateLayout)
	trip := f.goal("Japan", 80000, &dl)
	car := f.goal("Car", 300000, nil)

	f.save(f.kbank, &trip, 40000)
	w := f.save(f.scb, &trip, 12000)
	if w.GoalPage == nil || w.GoalPage.Goal.ID != trip || len(w.GoalPage.ByWallet) != 2 || w.SparePage != nil {
		t.Errorf("goal_page = %+v", w.GoalPage)
	}
	if len(w.Goals) != 1 || w.Goals[0].SavedAmount != 52000 || w.Goals[0].ProgressPct == nil || *w.Goals[0].ProgressPct != 65 {
		t.Fatalf("trip after saves = %+v", w.Goals)
	}
	f.save(f.kbank, &car, 60000)
	w = f.save(f.scb, &car, 36000) // SCB: 48,000 saved > 30,000 balance → red, not blocked
	scb := walletOf(w, f.scb)
	if scb == nil || !scb.SavedExceedsBalance || scb.OverBy != 18000 || scb.Available == nil || *scb.Available != -18000 {
		t.Fatalf("scb savings = %+v", scb)
	}
	w = f.save(f.kbank, nil, 8500)
	kb := walletOf(w, f.kbank)
	if kb == nil || kb.Saved != 108500 || kb.Spare != 8500 || *kb.Available != 81500 || len(kb.Buckets) != 3 {
		t.Fatalf("kbank savings = %+v", kb)
	}
	if w.SparePage == nil || w.SparePage.Spare.Amount != 8500 || w.GoalPage != nil {
		t.Errorf("spare_page = %+v", w.SparePage)
	}
	if w.Spare.Amount != 8500 || len(w.Spare.ByWallet) != 1 {
		t.Errorf("spare = %+v", w.Spare)
	}

	raw := f.call("POST", "/saving-entries", "", map[string]any{"kind": "save", "amount": 10, "wallet_id": f.untrack}, http.StatusBadRequest, nil)
	if errCode(raw) != "WALLET_NOT_TRACKED" {
		t.Errorf("untracked = %s", raw)
	}

	var l GoalList
	f.call("GET", "/saving-goals", "listSavingGoals", nil, http.StatusOK, &l)
	if l.Summary.Saved != 156500 || l.Summary.InGoals != 148000 || l.Summary.Spare != 8500 || len(l.Data) != 2 {
		t.Fatalf("list = %+v", l.Summary)
	}

	var d GoalDetail
	f.call("GET", "/saving-goals/"+trip.String(), "getSavingGoal", nil, http.StatusOK, &d)
	if len(d.ByWallet) != 2 || d.ByWallet[0].Wallet.ID != f.kbank || d.ByWallet[0].SharePct != 77 || d.Entries.Pagination.Total != 2 {
		t.Fatalf("trip page = %+v / %+v", d.ByWallet, d.Entries.Pagination)
	}
	if d.Goal.NeededMonthly == nil || d.Goal.PaceMonthly == nil || d.Goal.Pace == "" || len(d.History) == 0 ||
		d.History[len(d.History)-1].Cumulative != 52000 || len(d.Goal.Milestones) != 4 || !d.Goal.Milestones[1].Reached {
		t.Errorf("trip figures = %+v history %+v", d.Goal, d.History)
	}

	f.call("POST", "/saving-entries", "createSavingEntry", map[string]any{
		"kind": "move", "amount": 45000, "goal_id": trip, "to_goal_id": car,
	}, http.StatusCreated, &w)
	if len(w.Entries) != 2 || w.Entries[0].Amount != 40000 || w.Entries[0].GroupID != w.Entries[1].GroupID ||
		w.Entries[1].Amount != 5000 || len(w.Goals) != 2 {
		t.Fatalf("move = %+v", w.Entries)
	}
	raw = f.call("POST", "/saving-entries", "createSavingEntry", map[string]any{
		"kind": "withdraw", "amount": 9000, "goal_id": trip,
	}, http.StatusConflict, nil)
	if errCode(raw) != "INSUFFICIENT_SAVING" || !strings.Contains(string(raw), `"available":7000`) {
		t.Errorf("insufficient = %s", raw)
	}
	raw = f.call("POST", "/saving-entries", "", map[string]any{
		"kind": "withdraw", "amount": 5000, "goal_id": trip, "splits": []map[string]any{{"wallet_id": f.scb, "amount": 4000}},
	}, http.StatusBadRequest, nil)
	if errCode(raw) != "SPLIT_MISMATCH" {
		t.Errorf("split mismatch = %s", raw)
	}
	f.call("POST", "/saving-entries", "createSavingEntry", map[string]any{
		"kind": "withdraw", "amount": 500, "goal_id": nil, "note": "coffee",
	}, http.StatusCreated, &w)
	if w.Spare.Amount != 8000 || w.Entries[0].From.Type != "spare" || w.Entries[0].To.Type != "wallet" {
		t.Errorf("withdraw spare = %+v", w.Entries)
	}

	// An entry whose money already moved on can't be deleted.
	var el EntryList
	f.call("GET", "/saving-entries?goal_id="+trip.String(), "listSavingEntries", nil, http.StatusOK, &el)
	var firstSave Entry
	for _, e := range el.Data {
		if e.Kind == "save" && e.Wallet.ID == f.kbank {
			firstSave = e
		}
	}
	raw = f.call("DELETE", "/saving-entries/"+firstSave.ID.String(), "deleteSavingEntry", nil, http.StatusConflict, nil)
	if errCode(raw) != "INSUFFICIENT_SAVING" {
		t.Errorf("delete moved = %s", raw)
	}
	f.call("PUT", "/saving-entries/"+firstSave.ID.String(), "updateSavingEntry", map[string]any{"amount": 41000, "note": "bonus"}, http.StatusOK, &w)
	if w.Entries[0].Amount != 41000 || w.Entries[0].Note == nil {
		t.Errorf("edit = %+v", w.Entries)
	}

	if raw := f.call("DELETE", "/saving-goals/"+car.String(), "", nil, http.StatusConflict, nil); errCode(raw) != "GOAL_HAS_SAVINGS" {
		t.Errorf("delete car = %s", raw)
	}
	var before, after GoalList
	f.call("GET", "/saving-goals", "listSavingGoals", nil, http.StatusOK, &before)
	walletsBefore, _ := shared.LoadWalletSavings(f.ctx, f.pool, f.me, []uuid.UUID{f.kbank, f.scb})
	f.call("POST", "/saving-goals/"+car.String()+"/archive", "archiveSavingGoal", nil, http.StatusOK, &d)
	held := d.Goal.SavedAmount
	if d.Goal.State != StateArchived || held <= 0 || !d.Goal.Actions.Restore || d.Goal.Actions.Archive {
		t.Fatalf("archived with money = %+v", d.Goal)
	}
	f.call("GET", "/saving-goals", "listSavingGoals", nil, http.StatusOK, &after)
	walletsAfter, _ := shared.LoadWalletSavings(f.ctx, f.pool, f.me, []uuid.UUID{f.kbank, f.scb})
	if after.Summary.Saved != round2(before.Summary.Saved-held) || after.Summary.InGoals != round2(before.Summary.InGoals-held) {
		t.Errorf("totals with car archived = %+v (before %+v, car %v)", after.Summary, before.Summary, held)
	}
	var freed float64
	for _, w := range []uuid.UUID{f.kbank, f.scb} {
		freed += walletsBefore[w].Saved - walletsAfter[w].Saved
		if *walletsAfter[w].Available-*walletsBefore[w].Available != walletsBefore[w].Saved-walletsAfter[w].Saved {
			t.Errorf("wallet %v available = %v → %v", w, *walletsBefore[w].Available, *walletsAfter[w].Available)
		}
	}
	if round2(freed) != held {
		t.Errorf("wallets' saved dropped by %v, want %v", freed, held)
	}
	f.call("POST", "/saving-goals/"+car.String()+"/restore", "restoreSavingGoal", nil, http.StatusOK, &d)
	f.call("GET", "/saving-goals", "listSavingGoals", nil, http.StatusOK, &after)
	if d.Goal.State != StateActive || after.Summary.Saved != before.Summary.Saved {
		t.Errorf("restored: %s, saved %v (want %v)", d.Goal.State, after.Summary.Saved, before.Summary.Saved)
	}
	f.call("POST", "/saving-goals/"+car.String()+"/close", "closeSavingGoal", map[string]any{"to": "wallets"}, http.StatusOK, &w)
	if len(w.Entries) != 2 || len(w.Goals) != 1 || w.Goals[0].State != StateCompleted || w.Goals[0].SavedAmount != 0 {
		t.Fatalf("close = %+v / %+v", w.Entries, w.Goals)
	}
	f.call("POST", "/saving-entries", "", map[string]any{"kind": "save", "amount": 10, "wallet_id": f.kbank, "goal_id": car},
		http.StatusConflict, nil)
	f.call("GET", "/saving-goals?status=completed", "listSavingGoals", nil, http.StatusOK, &l)
	if len(l.Data) != 1 || l.Data[0].ID != car {
		t.Errorf("completed = %+v", l.Data)
	}
	f.call("POST", "/saving-goals/"+car.String()+"/restore", "restoreSavingGoal", nil, http.StatusOK, &d)
	f.call("POST", "/saving-goals/"+car.String()+"/archive", "archiveSavingGoal", nil, http.StatusOK, &d)
	f.call("PUT", "/saving-goals/"+trip.String(), "updateSavingGoal", map[string]any{"name": "Japan 2027", "deadline": nil, "milestones": []int{50, 100}}, http.StatusOK, &d)
	if d.Goal.Deadline != nil || len(d.Goal.Milestones) != 2 || d.Goal.Pace != "none" {
		t.Errorf("update = %+v", d.Goal)
	}
	var sp SpareDetail
	f.call("GET", "/saving-goals/spare", "getSavingSpare", nil, http.StatusOK, &sp)
	if sp.Spare.Amount != 8000 || sp.Entries.Pagination.Total != 2 {
		t.Errorf("spare page = %+v", sp.Spare)
	}
	f.call("DELETE", "/saving-goals/"+car.String(), "deleteSavingGoal", nil, http.StatusOK, nil)
	f.call("GET", "/saving-entries?goal_id="+trip.String(), "listSavingEntries", nil, http.StatusOK, &el)
	for _, e := range el.Data {
		if e.Kind == "move" && (e.To.Type != "goal" || e.To.ID != nil || e.To.Name != nil) {
			t.Errorf("move to the deleted goal = %+v", e.To)
		}
	}
}

func TestSavingGoalFigures(t *testing.T) {
	f := newGoalFix(t)
	dl := f.today.AddDate(0, 3, 0).Format(dateLayout)
	g := f.goal("Laptop", 35000, &dl)
	month := func(back int) string {
		m := time.Date(f.today.Year(), f.today.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -back, 0)
		return m.Format(dateLayout)
	}
	for _, back := range []int{4, 2, 1, 0} {
		f.call("POST", "/saving-entries", "createSavingEntry", map[string]any{
			"kind": "save", "amount": 3000, "wallet_id": f.kbank, "goal_id": g, "date": month(back),
		}, http.StatusCreated, nil)
	}
	var d GoalDetail
	f.call("GET", "/saving-goals/"+g.String(), "getSavingGoal", nil, http.StatusOK, &d)
	gv := d.Goal
	if gv.SavedAmount != 12000 || gv.PaceMonthly == nil || *gv.PaceMonthly != 3000 {
		t.Fatalf("pace = %+v", gv)
	}
	if gv.EtaDate == nil || gv.Pace != "late" || gv.LateMonths == nil || *gv.LateMonths != 5 || gv.NeededMonthly == nil {
		t.Errorf("eta = %v pace %s late %v needed %v", gv.EtaDate, gv.Pace, gv.LateMonths, gv.NeededMonthly)
	}
	if len(d.History) != 5 || d.History[0].Cumulative != 3000 || d.History[1].Cumulative != 3000 || d.History[4].Cumulative != 12000 {
		t.Errorf("history = %+v", d.History)
	}
}

func TestSavingsWithoutWallet(t *testing.T) {
	f := newGoalFix(t)
	goal := f.goal("Japan", 80000, nil)
	post := func(body map[string]any) WriteResult {
		var w WriteResult
		f.call("POST", "/saving-entries", "createSavingEntry", body, http.StatusCreated, &w)
		return w
	}

	f.exec(`INSERT INTO user_notification_settings (user_id, default_account_id) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET default_account_id = EXCLUDED.default_account_id`, f.me, f.kbank)
	w := post(map[string]any{"kind": "save", "amount": 1000, "goal_id": goal})
	if len(w.Entries) != 1 || w.Entries[0].Wallet != nil || w.Entries[0].From.Type != "wallet" || w.Entries[0].From.ID != nil {
		t.Fatalf("wallet-less entry = %+v", w.Entries)
	}
	if len(w.Wallets) != 0 || w.Goals[0].SavedAmount != 1000 {
		t.Errorf("wallets %v goals %+v", w.Wallets, w.Goals)
	}
	if w.GoalPage == nil || len(w.GoalPage.ByWallet) != 1 || w.GoalPage.ByWallet[0].Wallet != nil {
		t.Errorf("by_wallet = %+v", w.GoalPage)
	}

	w = post(map[string]any{"kind": "save", "amount": 2000, "goal_id": goal, "wallet_id": f.kbank})
	if w.Entries[0].Wallet == nil || w.Entries[0].Wallet.ID != f.kbank {
		t.Fatalf("explicit-wallet entry = %+v", w.Entries[0].Wallet)
	}
	if kb := walletOf(w, f.kbank); kb == nil || kb.Saved != 2000 {
		t.Errorf("kbank savings = %+v (the wallet-less 1000 must not count)", kb)
	}
	if w.Goals[0].SavedAmount != 3000 {
		t.Errorf("goal saved = %v, want 3000", w.Goals[0].SavedAmount)
	}

	// Withdraw 2500: largest-first takes the wallet's 2000 and 500 of the wallet-less money; the
	// wallet-less pool can't go below 0 on its own.
	var out WriteResult
	f.call("POST", "/saving-entries", "createSavingEntry", map[string]any{"kind": "withdraw", "amount": 2500, "goal_id": goal},
		http.StatusCreated, &out)
	if out.Goals[0].SavedAmount != 500 {
		t.Errorf("after withdraw: saved = %v, want 500", out.Goals[0].SavedAmount)
	}
	raw := f.call("POST", "/saving-entries", "", map[string]any{"kind": "withdraw", "amount": 600, "goal_id": goal}, http.StatusConflict, nil)
	_ = raw

	f.exec(`UPDATE saving_entries SET account_id = NULL WHERE account_id = $1`, f.kbank)
	var g GoalDetail
	f.call("GET", "/saving-goals/"+goal.String(), "getSavingGoal", nil, http.StatusOK, &g)
	if g.Goal.SavedAmount != 500 {
		t.Errorf("saved after unlinking = %v, want 500", g.Goal.SavedAmount)
	}
}

func TestGoalMonthNumbersAndAutoSave(t *testing.T) {
	f := newGoalFix(t)
	dl := f.today.AddDate(0, 5, 0).Format(dateLayout)
	goal := f.goal("Japan", 80000, &dl)
	f.save(f.kbank, &goal, 10000)

	var d GoalDetail
	f.call("GET", "/saving-goals/"+goal.String(), "getSavingGoal", nil, http.StatusOK, &d)
	g := d.Goal
	if g.MonthSaved != 10000 || g.MonthPlan == nil || g.MonthShort == nil || *g.MonthShort != round2(*g.MonthPlan-10000) && *g.MonthShort != 0 || g.AutoSave != nil {
		t.Fatalf("goal month numbers = %+v", g)
	}
	f.call("PUT", "/saving-goals/"+goal.String()+"/auto-save", "setGoalAutoSave",
		map[string]any{"amount": 15000, "day": 31, "account_id": f.kbank}, http.StatusOK, &d)
	g = d.Goal
	if g.AutoSave == nil || g.AutoSave.Day != 31 || g.AutoSave.Wallet == nil || g.AutoSave.Wallet.ID != f.kbank ||
		g.MonthPlan == nil || *g.MonthPlan != 15000 || g.MonthShort == nil || *g.MonthShort != 5000 {
		t.Fatalf("with auto-save = plan %v short %v auto %+v", g.MonthPlan, g.MonthShort, g.AutoSave)
	}
	f.call("PUT", "/saving-goals/"+goal.String()+"/auto-save", "", map[string]any{"amount": 0, "day": 5}, http.StatusBadRequest, nil)
	f.call("PUT", "/saving-goals/"+goal.String()+"/auto-save", "", map[string]any{"amount": 10, "day": 32}, http.StatusBadRequest, nil)
	f.call("PUT", "/saving-goals/"+goal.String()+"/auto-save", "", map[string]any{"amount": 10, "day": 5, "account_id": f.untrack}, http.StatusBadRequest, nil)

	var l GoalList
	f.call("GET", "/saving-goals", "listSavingGoals", nil, http.StatusOK, &l)
	if l.Summary.SavedTotal != 10000 || l.Summary.MonthShortTotal != 5000 || l.Summary.Unallocated != 0 {
		t.Errorf("summary = %+v", l.Summary)
	}
	var sp SpareDetail
	f.call("PUT", "/saving-goals/spare/auto-save", "setSpareAutoSave", map[string]any{"amount": 200, "day": 1}, http.StatusOK, &sp)
	if sp.AutoSave == nil || sp.AutoSave.Wallet != nil || sp.AutoSave.Amount != 200 {
		t.Errorf("spare auto-save = %+v", sp.AutoSave)
	}
	f.call("DELETE", "/saving-goals/spare/auto-save", "clearSpareAutoSave", nil, http.StatusOK, &sp)
	f.call("DELETE", "/saving-goals/"+goal.String()+"/auto-save", "clearGoalAutoSave", nil, http.StatusOK, &d)
	if sp.AutoSave != nil || d.Goal.AutoSave != nil {
		t.Errorf("cleared: spare %+v goal %+v", sp.AutoSave, d.Goal.AutoSave)
	}
}

func TestGoalManyAutoSaves(t *testing.T) {
	f := newGoalFix(t)
	dl := f.today.AddDate(0, 5, 0).Format(dateLayout)
	goal := f.goal("Japan", 80000, &dl)
	path := "/saving-goals/" + goal.String() + "/auto-saves"
	start := f.today.Format(dateLayout)
	monthly := func(day int) map[string]any {
		return map[string]any{"freq": "monthly", "interval": 1, "monthly": map[string]any{"mode": "day", "day": day}}
	}
	weekly := map[string]any{"freq": "weekly", "interval": 1}

	var d GoalDetail
	f.call("GET", "/saving-goals/"+goal.String(), "getSavingGoal", nil, http.StatusOK, &d)
	if d.Goal.AutoSaves == nil || len(d.Goal.AutoSaves) != 0 || d.Goal.AutoSave != nil {
		t.Fatalf("none yet = %+v / %+v", d.Goal.AutoSaves, d.Goal.AutoSave)
	}

	f.call("PUT", path, "setGoalAutoSaves", map[string]any{"items": []map[string]any{
		{"amount": 1000, "account_id": f.kbank, "rule": monthly(31), "start_date": start, "end": map[string]any{"kind": "none"}},
		{"amount": 200, "account_id": f.scb, "rule": weekly, "start_date": start, "end": map[string]any{"kind": "count", "count": 3}},
	}}, http.StatusOK, &d)
	got := d.Goal.AutoSaves
	if len(got) != 2 || got[0].Amount != 1000 || got[0].Wallet == nil || got[0].Wallet.ID != f.kbank ||
		got[0].Rule.Freq != "monthly" || got[0].StartDate != start || got[0].End.Kind != "none" ||
		got[1].Rule.Freq != "weekly" || got[1].End.Kind != "count" || got[1].NextDate == nil || *got[1].NextDate != start {
		t.Fatalf("two items = %+v", got)
	}
	if d.Goal.AutoSave == nil || d.Goal.AutoSave.Amount != 1000 || d.Goal.AutoSave.Day != 31 {
		t.Errorf("legacy auto_save = %+v", d.Goal.AutoSave)
	}
	// 1000 for the 31st + 200 × the weekly dates (≤ 3) that fall in this month.
	if d.Goal.MonthPlan == nil || *d.Goal.MonthPlan < 1200 || *d.Goal.MonthPlan > 1600 {
		t.Errorf("month_plan = %v", d.Goal.MonthPlan)
	}

	// Replacing: the first is kept in place (same id), the weekly goes, a new one comes.
	keep := got[0].ID
	f.call("PUT", path, "setGoalAutoSaves", map[string]any{"items": []map[string]any{
		{"id": keep, "amount": 1500, "account_id": nil, "rule": monthly(31), "start_date": start},
		{"amount": 50, "rule": map[string]any{"freq": "daily", "interval": 7}, "start_date": start},
	}}, http.StatusOK, &d)
	got = d.Goal.AutoSaves
	if len(got) != 2 || got[0].ID != keep || got[0].Amount != 1500 || got[0].Wallet != nil || got[1].Rule.Interval != 7 {
		t.Fatalf("replaced = %+v", got)
	}

	// Validation.
	for name, items := range map[string][]map[string]any{
		"amount":       {{"amount": 0, "rule": monthly(5), "start_date": start}},
		"no rule":      {{"amount": 10, "start_date": start}},
		"no start":     {{"amount": 10, "rule": monthly(5)}},
		"unknown id":   {{"id": uuid.New(), "amount": 10, "rule": monthly(5), "start_date": start}},
		"untracked":    {{"amount": 10, "account_id": f.untrack, "rule": monthly(5), "start_date": start}},
		"duplicate id": {{"id": keep, "amount": 10, "rule": monthly(5), "start_date": start}, {"id": keep, "amount": 10, "rule": monthly(6), "start_date": start}},
	} {
		raw := f.call("PUT", path, "", map[string]any{"items": items}, http.StatusBadRequest, nil)
		if name == "untracked" && errCode(raw) != "WALLET_NOT_TRACKED" {
			t.Errorf("%s: %s", name, raw)
		}
	}
	bad := f.call("PUT", path, "", map[string]any{"items": []map[string]any{
		{"amount": 10, "rule": map[string]any{"freq": "sometimes"}, "start_date": start},
	}}, http.StatusBadRequest, nil)
	if errCode(bad) != "INVALID_RULE" {
		t.Errorf("bad rule: %s", bad)
	}
	far := f.today.AddDate(3, 0, 0).Format(dateLayout)
	tooFar := f.call("PUT", path, "", map[string]any{"items": []map[string]any{
		{"amount": 10, "rule": monthly(5), "start_date": start, "end": map[string]any{"kind": "date", "date": far}},
	}}, http.StatusBadRequest, nil)
	if errCode(tooFar) != "END_TOO_FAR" {
		t.Errorf("far end: %s", tooFar)
	}
	f.call("PUT", path, "", map[string]any{}, http.StatusBadRequest, nil)
	f.call("GET", "/saving-goals/"+goal.String(), "getSavingGoal", nil, http.StatusOK, &d)
	if len(d.Goal.AutoSaves) != 2 {
		t.Fatalf("a failed PUT changed the set: %+v", d.Goal.AutoSaves)
	}

	// The old call replaces the set with one monthly item, keeping the first row.
	f.call("PUT", "/saving-goals/"+goal.String()+"/auto-save", "setGoalAutoSave",
		map[string]any{"amount": 700, "day": 31}, http.StatusOK, &d)
	if len(d.Goal.AutoSaves) != 1 || d.Goal.AutoSaves[0].ID != keep || d.Goal.AutoSaves[0].Amount != 700 {
		t.Errorf("legacy PUT = %+v", d.Goal.AutoSaves)
	}

	// The spare, and clearing.
	var sp SpareDetail
	f.call("PUT", "/saving-goals/spare/auto-saves", "setSpareAutoSaves", map[string]any{"items": []map[string]any{
		{"amount": 30, "rule": weekly, "start_date": start},
	}}, http.StatusOK, &sp)
	if len(sp.AutoSaves) != 1 || sp.AutoSave == nil || sp.AutoSave.Amount != 30 {
		t.Errorf("spare = %+v / %+v", sp.AutoSaves, sp.AutoSave)
	}
	f.call("PUT", "/saving-goals/spare/auto-saves", "setSpareAutoSaves", map[string]any{"items": []map[string]any{}}, http.StatusOK, &sp)
	f.call("PUT", path, "setGoalAutoSaves", map[string]any{"items": []map[string]any{}}, http.StatusOK, &d)
	if sp.AutoSaves == nil || len(sp.AutoSaves) != 0 || sp.AutoSave != nil || d.Goal.AutoSaves == nil || len(d.Goal.AutoSaves) != 0 {
		t.Errorf("cleared: %+v / %+v", sp.AutoSaves, d.Goal.AutoSaves)
	}
}

func TestGoalMonthParam(t *testing.T) {
	f := newGoalFix(t)
	dl := f.today.AddDate(0, 5, 0).Format(dateLayout)
	goal := f.goal("Japan", 80000, &dl)
	start := f.today.Format(dateLayout)
	path := "/saving-goals/" + goal.String()
	next := f.today.AddDate(0, 1, 0)
	nextMonth := next.Format("2006-01")
	f.call("PUT", path+"/auto-saves", "setGoalAutoSaves", map[string]any{"items": []map[string]any{
		{"amount": 1000, "rule": map[string]any{"freq": "monthly", "interval": 1, "monthly": map[string]any{"mode": "day", "day": 31}}, "start_date": start},
		{"amount": 100, "rule": map[string]any{"freq": "daily", "interval": 1}, "start_date": f.today.AddDate(0, -3, 0).Format(dateLayout)},
	}}, http.StatusOK, nil)

	var now, later GoalDetail
	f.call("GET", path, "getSavingGoal", nil, http.StatusOK, &now)
	f.call("GET", path+"?month="+nextMonth, "getSavingGoal", nil, http.StatusOK, &later)
	days := time.Date(next.Year(), next.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	// next month: the monthly item once (the 31st clamps) + the daily item every day.
	if later.Goal.MonthPlan == nil || *later.Goal.MonthPlan != float64(1000+100*days) {
		t.Errorf("month_plan for %s = %v, want %d", nextMonth, later.Goal.MonthPlan, 1000+100*days)
	}
	thisDays := time.Date(f.today.Year(), f.today.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if now.Goal.MonthPlan == nil || *now.Goal.MonthPlan != float64(1000+100*thisDays) {
		t.Errorf("this month's plan = %v, want %d", now.Goal.MonthPlan, 1000+100*thisDays)
	}
	if later.Goal.MonthSaved != 0 {
		t.Errorf("month_saved for a future month = %v", later.Goal.MonthSaved)
	}

	var l GoalList
	f.call("GET", "/saving-goals?month="+nextMonth, "listSavingGoals", nil, http.StatusOK, &l)
	if len(l.Data) != 1 || l.Data[0].MonthPlan == nil || *l.Data[0].MonthPlan != float64(1000+100*days) {
		t.Errorf("list month_plan = %+v", l.Data)
	}
	f.call("GET", path+"?month=2026-13", "", nil, http.StatusBadRequest, nil)
	f.call("GET", "/saving-goals?month=next", "", nil, http.StatusBadRequest, nil)
}

func TestOpenEndedGoal(t *testing.T) {
	f := newGoalFix(t)
	var d GoalDetail
	f.call("POST", "/saving-goals", "createSavingGoal", map[string]any{"name": "For the kid"}, http.StatusCreated, &d)
	kid := d.Goal.ID
	g := d.Goal
	if g.TargetAmount != nil || g.ProgressPct != nil || g.RemainingAmount != nil || len(g.Milestones) != 0 ||
		g.Group == nil || *g.Group != GroupOngoing || g.Status != "none" || g.MonthPlan != nil || g.MonthShort != nil {
		t.Fatalf("open-ended = %+v", g)
	}
	f.save(f.kbank, &kid, 2000)
	f.call("PUT", "/saving-goals/"+kid.String()+"/auto-save", "setGoalAutoSave",
		map[string]any{"amount": 3000, "day": 31, "account_id": f.kbank}, http.StatusOK, &d)
	g = d.Goal
	if g.SavedAmount != 2000 || g.MonthSaved != 2000 || g.MonthPlan == nil || *g.MonthPlan != 3000 ||
		g.MonthShort != nil || g.EtaDate != nil || g.NeededMonthly != nil || g.Status != "none" {
		t.Fatalf("open-ended with auto-save = %+v", g)
	}
	for _, p := range d.History {
		if p.Plan != nil {
			t.Errorf("history plan line = %+v", p)
		}
	}
	dl := f.today.AddDate(0, 5, 0).Format(dateLayout)
	trip := f.goal("Trip", 50000, &dl)
	var l GoalList
	f.call("GET", "/saving-goals?sort=progress", "listSavingGoals", nil, http.StatusOK, &l)
	if len(l.Data) != 2 || l.Data[1].ID != kid {
		t.Fatalf("progress sort puts open-ended last = %+v", l.Data)
	}
	if l.Summary.SavedTotal != 2000 || l.Summary.MonthSavedTotal != 0 || l.Summary.MonthPlanTotal != *l.Data[0].MonthPlan ||
		l.Summary.MonthShortTotal != *l.Data[0].MonthShort {
		t.Errorf("summary leaves open-ended out = %+v", l.Summary)
	}
	_ = trip
	f.call("PUT", "/saving-goals/"+kid.String(), "updateSavingGoal", map[string]any{"target_amount": 10000}, http.StatusOK, &d)
	if d.Goal.TargetAmount == nil || *d.Goal.ProgressPct != 20 || d.Goal.Group != nil || d.Goal.MonthShort == nil {
		t.Errorf("with a target = %+v", d.Goal)
	}
	f.call("PUT", "/saving-goals/"+kid.String(), "updateSavingGoal", map[string]any{"target_amount": nil}, http.StatusOK, &d)
	if d.Goal.TargetAmount != nil || d.Goal.Group == nil {
		t.Errorf("target cleared = %+v", d.Goal)
	}
	f.call("POST", "/saving-goals", "", map[string]any{"name": "Bad", "target_amount": 0}, http.StatusBadRequest, nil)
}

func TestListLongTermTotals(t *testing.T) {
	f := newGoalFix(t)
	var l GoalList
	f.call("GET", "/saving-goals", "listSavingGoals", nil, http.StatusOK, &l)
	s := l.Summary
	if len(l.Data) != 0 || s.TargetTotal != 0 || s.TargetSavedTotal != 0 || s.TargetShortTotal != 0 || s.TargetPercent != 0 ||
		s.CountShort != 0 || s.CountDone != 0 || s.CountNoTarget != 0 {
		t.Fatalf("empty list summary = %+v", s)
	}

	over := f.goal("Over", 1000, nil)
	f.save(f.kbank, &over, 1500)
	reached := f.goal("Reached", 2000, nil)
	f.save(f.kbank, &reached, 2000)
	short := f.goal("Short", 10000, nil)
	f.save(f.kbank, &short, 2500)
	var d GoalDetail
	f.call("POST", "/saving-goals", "createSavingGoal", map[string]any{"name": "Ongoing"}, http.StatusCreated, &d)
	ongoing := d.Goal.ID
	f.save(f.kbank, &ongoing, 700)
	done := f.goal("Done", 5000, nil)
	f.save(f.kbank, &done, 5000)
	f.call("POST", "/saving-goals/"+done.String()+"/close", "closeSavingGoal", map[string]any{"to": "wallets"}, http.StatusOK, nil)

	f.call("GET", "/saving-goals", "listSavingGoals", nil, http.StatusOK, &l)
	s = l.Summary
	// Active goals with a target: Over 1500/1000 (capped to 1000), Reached 2000/2000, Short 2500/10000.
	// Over's surplus never covers Short's gap.
	if s.TargetTotal != 13000 || s.TargetSavedTotal != 5500 || s.TargetShortTotal != 7500 || s.TargetPercent != 42 {
		t.Errorf("active target totals = %+v", s)
	}
	if round2(s.TargetTotal-s.TargetSavedTotal) != s.TargetShortTotal {
		t.Errorf("target_total - target_saved_total != target_short_total: %+v", s)
	}
	if s.CountShort != 1 || s.CountDone != 2 || s.CountNoTarget != 1 || s.CountShort+s.CountDone+s.CountNoTarget != len(l.Data) {
		t.Errorf("active counts = %+v (len %d)", s, len(l.Data))
	}

	f.call("GET", "/saving-goals?status=completed", "listSavingGoals", nil, http.StatusOK, &l)
	s = l.Summary
	if len(l.Data) != 1 || s.CountDone != 1 || s.CountShort != 0 || s.CountNoTarget != 0 ||
		s.TargetTotal != 0 || s.TargetSavedTotal != 0 || s.TargetShortTotal != 0 || s.TargetPercent != 0 {
		t.Errorf("completed list summary = %+v", s)
	}
}

func TestLongTermTotalsBuckets(t *testing.T) {
	tgt := func(v float64) *float64 { return &v }
	goals := []GoalView{
		{State: StateActive, TargetAmount: tgt(1000), SavedAmount: 3000},  // over → done
		{State: StateActive, TargetAmount: tgt(1000), SavedAmount: 999.9}, // short
		{State: StateActive, SavedAmount: 50},                             // ongoing
		{State: StateCompleted, TargetAmount: tgt(500)},                   // completed → done, not summed
		{State: StateCompleted},                                           // completed, no target → done
		{State: StateArchived, TargetAmount: tgt(800), SavedAmount: 100},  // short, not summed
	}
	var s ListSummary
	longTermTotals(&s, goals)
	if s.CountDone != 3 || s.CountShort != 2 || s.CountNoTarget != 1 {
		t.Errorf("counts = %+v", s)
	}
	// Over (3000/1000) is capped to 1000 and does not hide Short's 0.1 gap.
	if s.TargetTotal != 2000 || s.TargetSavedTotal != 1999.9 || s.TargetShortTotal != 0.1 || s.TargetPercent != 99 {
		t.Errorf("totals = %+v", s)
	}
	if round2(s.TargetTotal-s.TargetSavedTotal) != s.TargetShortTotal {
		t.Errorf("identity broken: %+v", s)
	}
	var full ListSummary
	longTermTotals(&full, []GoalView{{State: StateActive, TargetAmount: tgt(1000), SavedAmount: 5000}})
	if full.TargetSavedTotal != 1000 || full.TargetShortTotal != 0 || full.TargetPercent != 100 {
		t.Errorf("over only = %+v", full)
	}
	var one ListSummary
	longTermTotals(&one, []GoalView{{State: StateActive, TargetAmount: tgt(3), SavedAmount: 0.87}})
	if one.TargetPercent != 29 {
		t.Errorf("floor percent = %d, want 29", one.TargetPercent)
	}
	var none ListSummary
	longTermTotals(&none, nil)
	if none != (ListSummary{}) {
		t.Errorf("empty = %+v", none)
	}
}
