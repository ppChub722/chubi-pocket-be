package pending

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/projects"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/tags"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

type apiEnv struct {
	pool *pgxpool.Pool
	r    *gin.Engine
	uid  uuid.UUID
	acct uuid.UUID
	cat  uuid.UUID
}

func newAPIEnv(t *testing.T) *apiEnv {
	t.Helper()
	pool := testdb.Pool(t)
	uid := testdb.User(t, pool)
	catSvc := categories.NewService(categories.NewStore(pool))
	txSvc := transactions.NewService(transactions.NewStore(pool), catSvc)
	pdSvc := personal_debts.NewService(personal_debts.NewStore(pool), txSvc)
	txSvc.WithDebtsCreator(pdSvc.CreateForTransactionTx)
	txSvc.WithSplitsEditor(pdSvc.EditForTransactionTx)
	txSvc.WithDebtLinks(pdSvc)
	prj := projects.NewService(projects.NewStore(pool))
	prj.WithTransactionsService(txSvc)
	svc := NewService(NewStore(pool), txSvc, tags.NewService(tags.NewStore(pool)), pdSvc, prj)
	h := NewHandler(svc)
	e := &apiEnv{pool: pool, uid: uid, acct: testdb.Account(t, pool, uid), cat: testdb.Category(t, pool, uid, "Food")}
	if _, err := pool.Exec(context.Background(), `UPDATE accounts SET balance = 1000 WHERE id = $1`, e.acct); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]string{{"TRANSFER_OUT", "expense"}, {"TRANSFER_IN", "income"}} {
		e.exec(t, `INSERT INTO categories (id, user_id, name, type, is_system, system_kind) VALUES ($1, $2, $3, $4, TRUE, $3)`, uuid.New(), uid, c[0], c[1])
	}
	e.r = apitest.Engine(uid, func(g *gin.RouterGroup) {
		g.GET("/pending-transactions", h.List)
		g.POST("/pending-transactions", h.Create)
		g.POST("/pending-transactions/submit", h.Submit)
		g.POST("/pending-transactions/submit-ready", h.SubmitReady)
		g.POST("/pending-transactions/:id/submit", h.SubmitOne)
		g.POST("/pending-transactions/discard", h.Discard)
		g.PUT("/pending-transactions/:id", h.Update)
		g.DELETE("/pending-transactions/:id", h.Delete)
	})
	return e
}

func (e *apiEnv) wallet(t *testing.T) uuid.UUID {
	t.Helper()
	return testdb.Account(t, e.pool, e.uid)
}

func (e *apiEnv) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(), q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func (e *apiEnv) balance(t *testing.T, id uuid.UUID) float64 {
	t.Helper()
	var b float64
	if err := e.pool.QueryRow(context.Background(), `SELECT balance FROM accounts WHERE id = $1`, id).Scan(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

func (e *apiEnv) call(t *testing.T, opID, method, path string, body any, wantStatus int, out any) {
	t.Helper()
	status, raw := apitest.Do(t, e.r, method, "/api/v1"+path, body)
	apitest.Conform(t, opID, status, raw)
	if status != wantStatus {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, status, wantStatus, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
}

type draftBody map[string]any

func (e *apiEnv) create(t *testing.T, drafts ...draftBody) []PendingTransaction {
	t.Helper()
	items := make([]map[string]any, len(drafts))
	for i, d := range drafts {
		items[i] = map[string]any{"draft": d}
	}
	var out struct {
		Data []PendingTransaction `json:"data"`
	}
	e.call(t, "createPending", "POST", "/pending-transactions", map[string]any{"items": items}, http.StatusCreated, &out)
	return out.Data
}

func (e *apiEnv) expense(amount float64) draftBody {
	return draftBody{"type": "expense", "amount": amount, "account_id": e.acct, "category_id": e.cat, "date": "2026-10-12"}
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestCreateAndListResolved(t *testing.T) {
	e := newAPIEnv(t)
	tagID := uuid.New()
	e.exec(t, `INSERT INTO tags (id, user_id, name) VALUES ($1, $2, 'trip')`, tagID, e.uid)
	contact := uuid.New()
	e.exec(t, `INSERT INTO contacts (id, user_id, display_name) VALUES ($1, $2, 'Aom')`, contact, e.uid)

	d := e.expense(120)
	d["tag_ids"] = []uuid.UUID{tagID}
	d["splits"] = []map[string]any{{"person_name": "Aom typed", "contact_id": contact, "owed_amount": 40}}
	created := e.create(t, d)
	if len(created) != 1 {
		t.Fatalf("created %d", len(created))
	}
	c := created[0]
	if c.Resolved.Account == nil || c.Resolved.Account.ID != e.acct || c.Resolved.Category == nil || c.Resolved.Category.Name != "Food" {
		t.Fatalf("resolved account / category: %+v", c.Resolved)
	}
	if len(c.Resolved.Tags) != 1 || c.Resolved.Tags[0].Name != "trip" {
		t.Fatalf("resolved tags: %+v", c.Resolved.Tags)
	}
	if len(c.Resolved.Splits) != 1 || c.Resolved.Splits[0].Person.Name != "Aom" || c.Resolved.Splits[0].OwedAmount != 40 {
		t.Fatalf("resolved splits: %+v", c.Resolved.Splits)
	}
	if len(c.Resolved.Missing) != 0 {
		t.Fatalf("missing: %v", c.Resolved.Missing)
	}

	e.exec(t, `UPDATE contacts SET nickname = 'Mimi', show_nickname = TRUE WHERE id = $1`, contact)
	var list PendingList
	e.call(t, "listPending", "GET", "/pending-transactions", nil, http.StatusOK, &list)
	if list.Count != 1 || list.Data[0].Resolved.Splits[0].Person.Name != "Mimi" {
		t.Fatalf("nickname not followed: %+v", list.Data)
	}

	e.exec(t, `DELETE FROM categories WHERE id = $1`, e.cat)
	e.exec(t, `DELETE FROM tags WHERE id = $1`, tagID)
	e.exec(t, `DELETE FROM contacts WHERE id = $1`, contact)
	e.call(t, "listPending", "GET", "/pending-transactions", nil, http.StatusOK, &list)
	r := list.Data[0].Resolved
	if r.Category != nil || !has(r.Missing, "category_id") || !has(r.Missing, "tag_ids") || !has(r.Missing, "contact_id") {
		t.Fatalf("dangling: %+v", r)
	}
	if r.Account == nil || has(r.Missing, "account_id") {
		t.Fatalf("account should still resolve: %+v", r)
	}
	if len(r.Tags) != 0 || len(r.Splits) != 1 || r.Splits[0].Person.Name != "Aom typed" {
		t.Fatalf("fallback: %+v", r)
	}
}

type PendingList = ListResponse

func TestCreateValidation(t *testing.T) {
	e := newAPIEnv(t)
	e.call(t, "createPending", "POST", "/pending-transactions", map[string]any{"items": []any{}}, http.StatusBadRequest, nil)
	e.call(t, "createPending", "POST", "/pending-transactions",
		map[string]any{"items": []any{map[string]any{"draft": map[string]any{"type": "gift"}}}}, http.StatusBadRequest, nil)
}

func TestUpdateClearsLastError(t *testing.T) {
	e := newAPIEnv(t)
	p := e.create(t, draftBody{"type": "expense", "date": "2026-10-12"})[0] // no amount
	var sub SubmitResponse
	e.call(t, "submitPending", "POST", "/pending-transactions/submit", map[string]any{"ids": []uuid.UUID{p.ID}}, http.StatusOK, &sub)
	if len(sub.Failed) != 1 || sub.Failed[0].Error.Code != "MISSING_AMOUNT" {
		t.Fatalf("submit: %+v", sub)
	}
	f := sub.Failed[0]
	if f.Pending == nil || f.Pending.LastError == nil || f.Pending.LastError.Code != "MISSING_AMOUNT" {
		t.Fatalf("failed item should carry the draft with last_error: %+v", f.Pending)
	}
	if len(sub.Submitted) != 0 || len(sub.AccountBalances) != 0 {
		t.Fatalf("nothing went through: %+v", sub)
	}

	var up PendingTransaction
	e.call(t, "updatePending", "PUT", "/pending-transactions/"+p.ID.String(),
		map[string]any{"draft": e.expense(50)}, http.StatusOK, &up)
	if up.LastError != nil || up.Resolved.Account == nil {
		t.Fatalf("update: %+v", up)
	}
	e.call(t, "updatePending", "PUT", "/pending-transactions/"+uuid.New().String(),
		map[string]any{"draft": e.expense(50)}, http.StatusNotFound, nil)
	e.call(t, "updatePending", "PUT", "/pending-transactions/"+p.ID.String(),
		map[string]any{"draft": map[string]any{"date": "nope"}}, http.StatusBadRequest, nil)
}

func TestSubmitReturnsRowsAndBalances(t *testing.T) {
	e := newAPIEnv(t)
	other := e.wallet(t)
	normal := e.create(t, e.expense(100))[0]
	transfer := e.create(t, draftBody{
		"type": "transfer", "amount": 300, "account_id": e.acct, "transfer_to_account_id": other, "date": "2026-10-12",
	})[0]
	missing := e.create(t, draftBody{"type": "expense", "date": "2026-10-12"})[0]
	unknown := uuid.New()

	var sub SubmitResponse
	e.call(t, "submitPending", "POST", "/pending-transactions/submit",
		map[string]any{"ids": []uuid.UUID{normal.ID, transfer.ID, missing.ID, unknown}}, http.StatusOK, &sub)

	if len(sub.Submitted) != 2 || len(sub.Failed) != 2 {
		t.Fatalf("submitted %d failed %d: %+v", len(sub.Submitted), len(sub.Failed), sub.Failed)
	}
	if rows := sub.Submitted[0].Rows; len(rows) != 1 || rows[0].Amount != 100 || rows[0].Account == nil || rows[0].Category == nil {
		t.Fatalf("normal rows: %+v", rows)
	}
	rows := sub.Submitted[1].Rows
	if len(rows) != 2 || rows[0].Account.ID != e.acct || rows[1].Account.ID != other {
		t.Fatalf("transfer rows must be OUT first: %+v", rows)
	}
	if rows[0].TransferPeer == nil || rows[0].TransferPeer.Direction != "out" {
		t.Fatalf("first row not OUT: %+v", rows[0].TransferPeer)
	}
	got := map[uuid.UUID]float64{}
	for _, b := range sub.AccountBalances {
		got[b.AccountID] = b.Balance
	}
	if len(sub.AccountBalances) != 2 || got[e.acct] != 600 || got[other] != 300 {
		t.Fatalf("balances: %+v", sub.AccountBalances)
	}
	if e.balance(t, e.acct) != 600 {
		t.Fatalf("wallet balance %v", e.balance(t, e.acct))
	}
	var gone, kept int
	for _, f := range sub.Failed {
		if f.ID == missing.ID {
			kept++
			if f.Pending == nil || f.Pending.LastError == nil {
				t.Fatalf("failed draft must carry last_error: %+v", f)
			}
		}
		if f.ID == unknown {
			gone++
			if f.Pending != nil || f.Error.Code != "NOT_FOUND" {
				t.Fatalf("unknown draft: %+v", f)
			}
		}
	}
	if gone != 1 || kept != 1 {
		t.Fatalf("failed items: %+v", sub.Failed)
	}
	if sub.Badges.PendingDrafts != 1 {
		t.Fatalf("badges: %+v", sub.Badges)
	}
	e.call(t, "submitPending", "POST", "/pending-transactions/submit", map[string]any{"ids": []uuid.UUID{}}, http.StatusBadRequest, nil)
}

func TestDiscard(t *testing.T) {
	e := newAPIEnv(t)
	ps := e.create(t, e.expense(1), e.expense(2), e.expense(3))
	foreign := uuid.New()

	var res DiscardResult
	e.call(t, "discardPending", "POST", "/pending-transactions/discard",
		map[string]any{"ids": []uuid.UUID{ps[0].ID, ps[1].ID, foreign}}, http.StatusOK, &res)
	if len(res.Discarded) != 2 || len(res.Skipped) != 1 || res.Skipped[0] != foreign || res.RemainingCount != 1 {
		t.Fatalf("discard: %+v", res)
	}
	if status, _ := apitest.Do(t, e.r, "POST", "/api/v1/pending-transactions/discard", map[string]any{"ids": []uuid.UUID{}}); status != http.StatusBadRequest {
		t.Fatalf("empty ids: %d", status)
	}

	e.call(t, "deletePending", "DELETE", "/pending-transactions/"+ps[2].ID.String(), nil, http.StatusOK, &res)
	if len(res.Discarded) != 1 || res.Discarded[0] != ps[2].ID || res.RemainingCount != 0 || res.Skipped == nil {
		t.Fatalf("delete: %+v", res)
	}
	e.call(t, "deletePending", "DELETE", "/pending-transactions/"+ps[2].ID.String(), nil, http.StatusNotFound, nil)
}

func TestSubmitUpdateTx(t *testing.T) {
	e := newAPIEnv(t)
	made := e.create(t, e.expense(100))[0]
	var sub SubmitResponse
	e.call(t, "submitPending", "POST", "/pending-transactions/submit", map[string]any{"ids": []uuid.UUID{made.ID}}, http.StatusOK, &sub)
	txID := sub.Submitted[0].Rows[0].ID

	amount := 70.0
	upd, err := NewStore(e.pool).Insert(context.Background(), e.uid, SourceManual, KindUpdateTx,
		Draft{Amount: &amount}, nil, &txID, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.call(t, "submitPending", "POST", "/pending-transactions/submit", map[string]any{"ids": []uuid.UUID{upd.ID}}, http.StatusOK, &sub)
	if len(sub.Submitted) != 1 || len(sub.Submitted[0].Rows) != 1 || sub.Submitted[0].Rows[0].Amount != 70 {
		t.Fatalf("update submit: %+v", sub)
	}
	if len(sub.AccountBalances) != 1 || sub.AccountBalances[0].AccountID != e.acct || sub.AccountBalances[0].Balance != 930 {
		t.Fatalf("balances: %+v", sub.AccountBalances)
	}
}

func TestNeedsInputSummaryAndSubmitReady(t *testing.T) {
	e := newAPIEnv(t)
	ready := e.create(t, e.expense(100))[0]
	reminder := e.create(t, draftBody{"type": "expense", "date": "2026-10-12", "account_id": e.acct})[0] // no amount, no category
	noWallet := e.create(t, draftBody{"type": "expense", "date": "2026-10-12", "amount": 40, "category_id": e.cat})[0]
	if len(ready.NeedsInput) != 0 || !ready.CanConfirm {
		t.Errorf("ready draft = %v / %v", ready.NeedsInput, ready.CanConfirm)
	}
	if !has(reminder.NeedsInput, "amount") || !has(reminder.NeedsInput, "category") || reminder.CanConfirm {
		t.Errorf("reminder draft = %v / %v", reminder.NeedsInput, reminder.CanConfirm)
	}
	if !has(noWallet.NeedsInput, "wallet") || has(noWallet.NeedsInput, "amount") || !noWallet.CanConfirm {
		t.Errorf("wallet-less draft = %v / %v", noWallet.NeedsInput, noWallet.CanConfirm)
	}

	var list PendingList
	e.call(t, "listPending", "GET", "/pending-transactions", nil, http.StatusOK, &list)
	s := list.Summary
	if s.Count != 3 || s.ExpenseTotal != 140 || s.IncomeTotal != 0 || s.NeedsInputCount != 2 || s.ReadyCount != 1 || s.BySource.Manual != 3 {
		t.Errorf("summary = %+v", s)
	}

	var sub SubmitResponse
	e.call(t, "submitOnePending", "POST", "/pending-transactions/"+reminder.ID.String()+"/submit", nil, http.StatusOK, &sub)
	if len(sub.Failed) != 1 || sub.Failed[0].Error.Code != "MISSING_AMOUNT" || len(sub.Submitted) != 0 {
		t.Fatalf("reminder confirm = %+v", sub)
	}
	e.call(t, "submitReadyPending", "POST", "/pending-transactions/submit-ready", nil, http.StatusOK, &sub)
	if len(sub.Submitted) != 1 || sub.Submitted[0].ID != ready.ID || len(sub.Failed) != 0 {
		t.Fatalf("submit-ready = %+v", sub)
	}
	d := draftBody{"type": "expense", "date": "2026-10-12", "account_id": e.acct, "amount": 25, "category_id": e.cat}
	e.call(t, "updatePending", "PUT", "/pending-transactions/"+reminder.ID.String(), map[string]any{"draft": d}, http.StatusOK, nil)
	e.call(t, "submitOnePending", "POST", "/pending-transactions/"+reminder.ID.String()+"/submit", nil, http.StatusOK, &sub)
	if len(sub.Submitted) != 1 || sub.Submitted[0].ID != reminder.ID {
		t.Fatalf("confirm after amount = %+v", sub)
	}
	e.call(t, "submitReadyPending", "POST", "/pending-transactions/submit-ready", nil, http.StatusOK, &sub)
	if len(sub.Submitted) != 0 || len(sub.Failed) != 0 {
		t.Errorf("empty submit-ready = %+v", sub)
	}
}
