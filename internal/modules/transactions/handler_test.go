package transactions_test

// Handler tests for the 9 transaction operations (API spec v2): the real
// service + handler over HTTP (apitest), every body checked against the
// spec (Conform) and the behaviour asserted (balances, tags, live person
// names, transfer twins, form options order).

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
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type hfix struct {
	t        *testing.T
	pool     *pgxpool.Pool
	ctx      context.Context
	me       uuid.UUID
	partner  uuid.UUID // an app user my contact Bee is linked to
	svc      *transactions.Service
	h        *transactions.Handler
	r        *gin.Engine // as me
	walletA  uuid.UUID
	walletB  uuid.UUID
	food     uuid.UUID
	beeID    uuid.UUID // my contact, linked to partner
	ceeID    uuid.UUID // my contact, no app account
	tagA     uuid.UUID
	tagB     uuid.UUID
	tagC     uuid.UUID
	otherTag uuid.UUID // partner's tag
}

func routes(h *transactions.Handler) func(g *gin.RouterGroup) {
	return func(g *gin.RouterGroup) {
		g.POST("/transactions", h.Create)
		g.GET("/transactions", h.List)
		g.GET("/transactions/form-options", h.FormOptions)
		g.GET("/transactions/summary", h.Summary)
		g.GET("/transactions/:id", h.Get)
		g.PUT("/transactions/:id", h.Update)
		g.DELETE("/transactions/:id", h.Delete)
		g.PUT("/transactions/:id/splits", h.EditSplits)
		g.PUT("/transactions/:id/tags", h.SetTags)
	}
}

// newHFix — me (system categories seeded), wallets A / B, a Food category,
// three tags, a linked contact Bee (→ partner "Partner Live") and an
// unlinked contact Cee.
func newHFix(t *testing.T, seed bool) *hfix {
	t.Helper()
	pool := testdb.Pool(t)
	f := &hfix{t: t, pool: pool, ctx: context.Background()}
	f.me = testdb.User(t, pool)
	f.partner = testdb.User(t, pool)
	cats := categories.NewService(categories.NewStore(pool))
	if seed {
		tx, err := pool.Begin(f.ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if err := cats.SeedForUser(f.ctx, tx, f.me); err != nil {
			t.Fatalf("seed categories: %v", err)
		}
		if err := tx.Commit(f.ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	f.svc = transactions.NewService(transactions.NewStore(pool), cats)
	debts := personal_debts.NewService(personal_debts.NewStore(pool), f.svc)
	f.svc.WithDebtsCreator(debts.CreateForTransactionTx)
	f.svc.WithSplitsEditor(debts.EditForTransactionTx)
	f.svc.WithDebtLinks(debts)
	f.h = transactions.NewHandler(f.svc)
	f.r = apitest.Engine(f.me, routes(f.h))

	f.exec(`UPDATE users SET display_name = 'Partner Live' WHERE id = $1`, f.partner)
	f.walletA = testdb.Account(t, pool, f.me)
	f.walletB = testdb.Account(t, pool, f.me)
	f.exec(`UPDATE accounts SET name = 'Wallet A' WHERE id = $1`, f.walletA)
	f.exec(`UPDATE accounts SET name = 'Wallet B' WHERE id = $1`, f.walletB)
	f.food = testdb.Category(t, pool, f.me, "Food")
	f.tagA, f.tagB, f.tagC = f.tag(f.me, "alpha"), f.tag(f.me, "beta"), f.tag(f.me, "gamma")
	f.otherTag = f.tag(f.partner, "theirs")
	f.beeID, f.ceeID = uuid.New(), uuid.New()
	f.exec(`INSERT INTO contacts (id, user_id, display_name, linked_user_id, status)
		VALUES ($1, $2, 'Bee', $3, 'active'), ($4, $2, 'Cee', NULL, 'active')`,
		f.beeID, f.me, f.partner, f.ceeID)
	// Runs before the users' own cleanups: debts point at each other
	// (mirror) and my contact points at the partner.
	t.Cleanup(func() {
		_, _ = pool.Exec(f.ctx, `DELETE FROM personal_debts WHERE user_id = ANY($1)`, []uuid.UUID{f.me, f.partner})
		_, _ = pool.Exec(f.ctx, `DELETE FROM contacts WHERE user_id = ANY($1)`, []uuid.UUID{f.me, f.partner})
		_, _ = pool.Exec(f.ctx, `DELETE FROM account_members WHERE user_id = ANY($1)`, []uuid.UUID{f.me, f.partner})
	})
	return f
}

func (f *hfix) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, q, args...); err != nil {
		f.t.Fatalf("exec %q: %v", q, err)
	}
}

func (f *hfix) tag(owner uuid.UUID, name string) uuid.UUID {
	f.t.Helper()
	id, _ := uuid.NewV7()
	f.exec(`INSERT INTO tags (id, user_id, name) VALUES ($1, $2, $3)`, id, owner, name)
	return id
}

func (f *hfix) balance(id uuid.UUID) float64 {
	f.t.Helper()
	var b float64
	if err := f.pool.QueryRow(f.ctx, `SELECT balance FROM accounts WHERE id = $1`, id).Scan(&b); err != nil {
		f.t.Fatalf("balance: %v", err)
	}
	return b
}

// call sends a request as me and checks the status (and the spec).
func (f *hfix) call(op, method, path string, body any, want int) []byte {
	f.t.Helper()
	return callAs(f.t, f.r, op, method, path, body, want)
}

func callAs(t *testing.T, r *gin.Engine, op, method, path string, body any, want int) []byte {
	t.Helper()
	status, out := apitest.Do(t, r, method, path, body)
	if status != want {
		t.Fatalf("%s %s: status %d, want %d; body %s", method, path, status, want, out)
	}
	if op != "" {
		apitest.Conform(t, op, status, out)
	}
	return out
}

func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	return v
}

func errCode(t *testing.T, body []byte) string {
	t.Helper()
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	return e.Error.Code
}

func balanceOf(bs []shared.WalletBalance, id uuid.UUID) (float64, bool) {
	for _, b := range bs {
		if b.AccountID == id {
			return b.Balance, true
		}
	}
	return 0, false
}

type writeResult = transactions.WriteResult

func tagNames(d transactions.TransactionDetail) []string {
	out := []string{}
	for _, t := range d.Tags {
		out = append(out, t.Name)
	}
	return out
}

func splitByName(d transactions.TransactionDetail) map[string]transactions.SplitRef {
	out := map[string]transactions.SplitRef{}
	if d.Splits != nil {
		for _, s := range *d.Splits {
			out[s.DisplayName] = s
		}
	}
	return out
}

// TestCreateGetListHandlers — createTransaction / getTransaction /
// listTransactions.
func TestCreateGetListHandlers(t *testing.T) {
	f := newHFix(t, true)

	// An expense with a split on a linked contact, a free-name split and a tag.
	body := map[string]any{
		"type": "expense", "account_id": f.walletA, "category_id": f.food, "amount": 300,
		"date": "2026-10-12", "description": "Dinner", "tag_ids": []uuid.UUID{f.tagA},
		"splits": []map[string]any{
			{"person_name": "Typed", "contact_id": f.beeID, "owed_amount": 100},
			{"person_name": "Aom", "owed_amount": 50},
		},
	}
	apitest.ConformRequest(t, "createTransaction", body)
	res := decode[writeResult](t, f.call("createTransaction", "POST", "/api/v1/transactions", body, http.StatusCreated))
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(res.Rows))
	}
	bill := res.Rows[0]
	if b, ok := balanceOf(res.AccountBalances, f.walletA); !ok || b != -300 || len(res.AccountBalances) != 1 {
		t.Errorf("account_balances = %+v, want [A -300]", res.AccountBalances)
	}
	if bill.Account == nil || bill.Account.ID != f.walletA || bill.Account.Name != "Wallet A" ||
		bill.Category == nil || bill.Category.ID != f.food || bill.Currency != "THB" {
		t.Errorf("refs: account=%+v category=%+v currency=%q", bill.Account, bill.Category, bill.Currency)
	}
	if got := tagNames(bill); len(got) != 1 || got[0] != "alpha" {
		t.Errorf("tags = %v, want [alpha]", got)
	}
	if bill.MyShare == nil || *bill.MyShare != 150 || bill.SplitCount != 2 || bill.Counterparty != nil || bill.EventBill != nil {
		t.Errorf("my_share=%v split_count=%d counterparty=%v", bill.MyShare, bill.SplitCount, bill.Counterparty)
	}
	if !bill.CanSplit || !bill.CanEditSplits || !bill.CanJoinEvent || !bill.CanEditCategory || bill.IsLocked ||
		bill.CreatedBy != nil || bill.TransferPeer != nil {
		t.Errorf("flags: %+v", bill)
	}
	splits := splitByName(bill)
	bee, ok := splits["Partner Live"]
	if !ok || bee.Person.ContactID == nil || *bee.Person.ContactID != f.beeID || !bee.Person.Linked ||
		bee.Person.LinkedUserID == nil || *bee.Person.LinkedUserID != f.partner || bee.Amount != 100 {
		t.Errorf("linked split = %+v (all %v), want the linked user's live name", bee, splits)
	}
	if aom, ok := splits["Aom"]; !ok || aom.Person.ContactID != nil || aom.Person.Linked {
		t.Errorf("free-name split = %+v", aom)
	}

	// The person name is live: nickname shown → nickname; hidden → the
	// linked user's profile name, which follows their profile.
	f.exec(`UPDATE contacts SET nickname = 'Beebee', show_nickname = TRUE WHERE id = $1`, f.beeID)
	got := decode[transactions.TransactionDetail](t,
		f.call("getTransaction", "GET", "/api/v1/transactions/"+bill.ID.String(), nil, http.StatusOK))
	if _, ok := splitByName(got)["Beebee"]; !ok {
		t.Errorf("after nickname: splits %v, want Beebee", splitByName(got))
	}
	f.exec(`UPDATE contacts SET show_nickname = FALSE WHERE id = $1`, f.beeID)
	f.exec(`UPDATE users SET display_name = 'Partner Renamed' WHERE id = $1`, f.partner)
	got = decode[transactions.TransactionDetail](t,
		f.call("getTransaction", "GET", "/api/v1/transactions/"+bill.ID.String(), nil, http.StatusOK))
	if s, ok := splitByName(got)["Partner Renamed"]; !ok || s.Person.Name != "Partner Renamed" {
		t.Errorf("after profile rename: splits %v, want Partner Renamed", splitByName(got))
	}

	// A transfer A → B: both rows, OUT first, each pointing at its twin.
	tr := decode[writeResult](t, f.call("createTransaction", "POST", "/api/v1/transactions", map[string]any{
		"type": "transfer", "account_id": f.walletA, "transfer_to_account_id": f.walletB,
		"amount": 50, "date": "2026-10-12", "tag_ids": []uuid.UUID{f.tagB},
	}, http.StatusCreated))
	if len(tr.Rows) != 2 {
		t.Fatalf("transfer rows = %d, want 2", len(tr.Rows))
	}
	out, in := tr.Rows[0], tr.Rows[1]
	if out.TransferPeer == nil || out.TransferPeer.Direction != "out" || out.TransferPeer.ID != in.ID ||
		out.TransferPeer.Account.ID != f.walletB || out.Account == nil || out.Account.ID != f.walletA {
		t.Errorf("OUT row peer = %+v", out.TransferPeer)
	}
	if in.TransferPeer == nil || in.TransferPeer.Direction != "in" || in.TransferPeer.ID != out.ID ||
		in.TransferPeer.Account.ID != f.walletA {
		t.Errorf("IN row peer = %+v", in.TransferPeer)
	}
	if out.MyShare != nil || out.CanSplit || out.CanEditCategory || out.Category == nil || !out.Category.IsSystem {
		t.Errorf("transfer row: my_share=%v can_split=%v can_edit_category=%v category=%+v",
			out.MyShare, out.CanSplit, out.CanEditCategory, out.Category)
	}
	if len(out.Tags) != 1 || len(in.Tags) != 0 {
		t.Errorf("transfer tags: out %v in %v, want the tag on the OUT row only", tagNames(out), tagNames(in))
	}
	a, okA := balanceOf(tr.AccountBalances, f.walletA)
	b, okB := balanceOf(tr.AccountBalances, f.walletB)
	if !okA || !okB || a != -350 || b != 50 {
		t.Errorf("transfer balances = %+v, want A -350 / B 50", tr.AccountBalances)
	}

	// Errors: someone else's tag (nothing is written), project_id, a
	// transfer without a destination.
	e := f.call("createTransaction", "POST", "/api/v1/transactions", map[string]any{
		"type": "expense", "account_id": f.walletA, "amount": 10, "date": "2026-10-12",
		"tag_ids": []uuid.UUID{f.otherTag},
	}, http.StatusBadRequest)
	if errCode(t, e) != "TAG_NOT_FOUND" || f.balance(f.walletA) != -350 {
		t.Errorf("foreign tag: code %s, balance %v (want TAG_NOT_FOUND, -350)", errCode(t, e), f.balance(f.walletA))
	}
	f.call("createTransaction", "POST", "/api/v1/transactions", map[string]any{
		"type": "expense", "amount": 10, "date": "2026-10-12", "project_id": uuid.New(),
	}, http.StatusBadRequest)
	f.call("createTransaction", "POST", "/api/v1/transactions", map[string]any{
		"type": "transfer", "account_id": f.walletA, "amount": 10, "date": "2026-10-12",
	}, http.StatusBadRequest)

	// List: page-ready rows, splits null, totals over the set.
	raw := f.call("listTransactions", "GET", "/api/v1/transactions?per_page=2", nil, http.StatusOK)
	var page struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 2 {
		t.Fatalf("list page rows = %d, want 2", len(page.Data))
	}
	for _, row := range page.Data {
		if string(row["splits"]) != "null" {
			t.Errorf("list row splits = %s, want null", row["splits"])
		}
	}
	list := decode[transactions.ListResponse](t, f.call("listTransactions", "GET", "/api/v1/transactions", nil, http.StatusOK))
	if list.Pagination.Total != 3 || list.Totals.Expense != 150 || list.Totals.Count != 1 {
		t.Errorf("list: total %d totals %+v, want 3 rows, expense 150 (my share), count 1", list.Pagination.Total, list.Totals)
	}
	for _, r := range list.Data {
		if r.ID == bill.ID && (r.SplitCount != 2 || r.MyShare == nil || *r.MyShare != 150 || len(r.Tags) != 1) {
			t.Errorf("list bill row = %+v", r)
		}
		if r.Type == transactions.TypeTransfer && r.TransferPeer == nil {
			t.Errorf("list transfer row %s has no transfer_peer", r.ID)
		}
	}
	tagged := decode[transactions.ListResponse](t, f.call("listTransactions", "GET",
		"/api/v1/transactions?tag_id="+f.tagB.String(), nil, http.StatusOK))
	if len(tagged.Data) != 1 || tagged.Data[0].ID != out.ID {
		t.Errorf("tag filter rows = %d, want the transfer OUT row", len(tagged.Data))
	}
	f.call("listTransactions", "GET", "/api/v1/transactions?type=bogus", nil, http.StatusBadRequest)

	// Get: 404 for unknown / someone else's row.
	f.call("getTransaction", "GET", "/api/v1/transactions/"+uuid.New().String(), nil, http.StatusNotFound)
	other := apitest.Engine(f.partner, routes(f.h))
	callAs(t, other, "getTransaction", "GET", "/api/v1/transactions/"+bill.ID.String(), nil, http.StatusNotFound)
}

// TestUpdateDeleteHandlers — updateTransaction / deleteTransaction.
func TestUpdateDeleteHandlers(t *testing.T) {
	f := newHFix(t, true)
	created := decode[writeResult](t, f.call("createTransaction", "POST", "/api/v1/transactions", map[string]any{
		"type": "expense", "account_id": f.walletA, "amount": 100, "date": "2026-10-12",
		"tag_ids": []uuid.UUID{f.tagA},
	}, http.StatusCreated))
	id := created.Rows[0].ID
	path := "/api/v1/transactions/" + id.String()

	// Move to wallet B with a new amount and a new tag set: both wallets in
	// account_balances.
	body := map[string]any{"amount": 120, "account_id": f.walletB, "tag_ids": []uuid.UUID{f.tagB, f.tagC}}
	apitest.ConformRequest(t, "updateTransaction", body)
	res := decode[writeResult](t, f.call("updateTransaction", "PUT", path, body, http.StatusOK))
	a, okA := balanceOf(res.AccountBalances, f.walletA)
	b, okB := balanceOf(res.AccountBalances, f.walletB)
	if !okA || !okB || a != 0 || b != -120 {
		t.Errorf("move balances = %+v, want A 0 / B -120", res.AccountBalances)
	}
	row := res.Rows[0]
	if row.Account == nil || row.Account.ID != f.walletB || row.Amount != 120 || row.Splits == nil {
		t.Errorf("edited row = account %+v amount %v splits %v", row.Account, row.Amount, row.Splits)
	}
	if got := tagNames(row); len(got) != 2 || got[0] != "beta" || got[1] != "gamma" {
		t.Errorf("tags after replace = %v, want [beta gamma]", got)
	}
	// tag_ids absent = unchanged; [] clears.
	res = decode[writeResult](t, f.call("updateTransaction", "PUT", path, map[string]any{"note": "n"}, http.StatusOK))
	if len(res.Rows[0].Tags) != 2 || res.Rows[0].Note == nil || *res.Rows[0].Note != "n" {
		t.Errorf("note-only edit: tags %v note %v", tagNames(res.Rows[0]), res.Rows[0].Note)
	}
	res = decode[writeResult](t, f.call("updateTransaction", "PUT", path, map[string]any{"tag_ids": []uuid.UUID{}}, http.StatusOK))
	if len(res.Rows[0].Tags) != 0 {
		t.Errorf("tag_ids [] left %v", tagNames(res.Rows[0]))
	}
	e := f.call("updateTransaction", "PUT", path, map[string]any{"tag_ids": []uuid.UUID{f.otherTag}}, http.StatusBadRequest)
	if errCode(t, e) != "TAG_NOT_FOUND" {
		t.Errorf("foreign tag on edit: %s", errCode(t, e))
	}
	f.call("updateTransaction", "PUT", "/api/v1/transactions/"+uuid.New().String(),
		map[string]any{"amount": 1}, http.StatusNotFound)

	// A transfer edited from its IN row: both rows back (OUT first), both
	// wallets' balances.
	tr := decode[writeResult](t, f.call("createTransaction", "POST", "/api/v1/transactions", map[string]any{
		"type": "transfer", "account_id": f.walletA, "transfer_to_account_id": f.walletB,
		"amount": 30, "date": "2026-10-12",
	}, http.StatusCreated))
	inPath := "/api/v1/transactions/" + tr.Rows[1].ID.String()
	res = decode[writeResult](t, f.call("updateTransaction", "PUT", inPath, map[string]any{"amount": 40}, http.StatusOK))
	if len(res.Rows) != 2 || res.Rows[0].ID != tr.Rows[0].ID || res.Rows[0].Amount != 40 || res.Rows[1].Amount != 40 {
		t.Fatalf("transfer edit rows = %+v", res.Rows)
	}
	a, _ = balanceOf(res.AccountBalances, f.walletA)
	b, _ = balanceOf(res.AccountBalances, f.walletB)
	if len(res.AccountBalances) != 2 || a != -40 || b != -80 {
		t.Errorf("transfer edit balances = %+v, want A -40 / B -80", res.AccountBalances)
	}
	// A transfer can't leave its wallets.
	f.call("updateTransaction", "PUT", inPath,
		map[string]any{"account_id": nil, "transfer_to_account_id": nil}, http.StatusBadRequest)

	// Delete: the id and the touched wallet(s) after the delete.
	del := decode[transactions.DeleteResult](t, f.call("deleteTransaction", "DELETE", path, nil, http.StatusOK))
	if del.ID != id || len(del.AccountBalances) != 1 || del.AccountBalances[0].AccountID != f.walletB ||
		del.AccountBalances[0].Balance != 40 {
		t.Errorf("delete result = %+v, want B at 40", del)
	}
	del = decode[transactions.DeleteResult](t, f.call("deleteTransaction", "DELETE", inPath, nil, http.StatusOK))
	if len(del.AccountBalances) != 2 || f.balance(f.walletA) != 0 || f.balance(f.walletB) != 0 {
		t.Errorf("transfer delete = %+v (A %v B %v), want both wallets back to 0",
			del, f.balance(f.walletA), f.balance(f.walletB))
	}
	f.call("deleteTransaction", "DELETE", path, nil, http.StatusNotFound)
}

// TestEditSplitsHandler — editSplits: a sent person_name is ignored on a
// split with a contact, free names rename in place, a new contact split
// takes the contact's name; the response is the Transaction.
func TestEditSplitsHandler(t *testing.T) {
	f := newHFix(t, true)
	created := decode[writeResult](t, f.call("createTransaction", "POST", "/api/v1/transactions", map[string]any{
		"type": "expense", "account_id": f.walletA, "amount": 300, "date": "2026-10-12",
		"splits": []map[string]any{
			{"person_name": "Typed", "contact_id": f.beeID, "owed_amount": 100},
			{"person_name": "Aom", "owed_amount": 50},
		},
	}, http.StatusCreated))
	bill := created.Rows[0]
	s := splitByName(bill)
	beeDebt, aomDebt := s["Partner Live"].DebtID, s["Aom"].DebtID
	path := "/api/v1/transactions/" + bill.ID.String() + "/splits"

	body := map[string]any{"splits": []map[string]any{
		{"debt_id": beeDebt, "person_name": "Hacked", "owed_amount": 120},
		{"debt_id": aomDebt, "person_name": "Aommy", "owed_amount": 50},
		{"contact_id": f.ceeID, "owed_amount": 30},
	}}
	apitest.ConformRequest(t, "editSplits", body)
	got := decode[transactions.TransactionDetail](t, f.call("editSplits", "PUT", path, body, http.StatusOK))
	s = splitByName(got)
	if _, hacked := s["Hacked"]; hacked || s["Partner Live"].DebtID != beeDebt || s["Partner Live"].Amount != 120 {
		t.Errorf("linked split after edit = %v, want the contact's name and 120", s)
	}
	if r, ok := s["Aommy"]; !ok || r.DebtID != aomDebt {
		t.Errorf("free-name rename = %v", s)
	}
	cee, ok := s["Cee"]
	if !ok || cee.Amount != 30 || cee.Person.ContactID == nil || *cee.Person.ContactID != f.ceeID || cee.Person.Linked {
		t.Errorf("new contact split = %+v", cee)
	}
	if got.MyShare == nil || *got.MyShare != 100 || got.SplitCount != 3 {
		t.Errorf("my_share = %v split_count = %d, want 100 / 3", got.MyShare, got.SplitCount)
	}
	var beeSnap, ceeSnap string
	_ = f.pool.QueryRow(f.ctx, `SELECT counterparty_person_name FROM personal_debts WHERE id = $1`, beeDebt).Scan(&beeSnap)
	_ = f.pool.QueryRow(f.ctx, `SELECT counterparty_person_name FROM personal_debts WHERE id = $1`, cee.DebtID).Scan(&ceeSnap)
	if beeSnap != "Typed" || ceeSnap != "Cee" {
		t.Errorf("stored snapshots = %q / %q, want Typed (unchanged) / Cee (the contact's name)", beeSnap, ceeSnap)
	}
	// The name stays live after the edit.
	f.exec(`UPDATE contacts SET nickname = 'C-nick' WHERE id = $1`, f.ceeID)
	got = decode[transactions.TransactionDetail](t,
		f.call("getTransaction", "GET", "/api/v1/transactions/"+bill.ID.String(), nil, http.StatusOK))
	if _, ok := splitByName(got)["C-nick"]; !ok {
		t.Errorf("after nickname: %v", splitByName(got))
	}

	// Back to a free name without a name: keeps the name the contact showed.
	got = decode[transactions.TransactionDetail](t, f.call("editSplits", "PUT", path, map[string]any{"splits": []map[string]any{
		{"debt_id": beeDebt, "owed_amount": 120},
		{"debt_id": aomDebt, "owed_amount": 50},
		{"debt_id": cee.DebtID, "contact_id": nil, "owed_amount": 30},
	}}, http.StatusOK))
	if r, ok := splitByName(got)["C-nick"]; !ok || r.Person.ContactID != nil || r.DebtID != cee.DebtID {
		t.Errorf("dropped contact = %v, want a free name C-nick on the same debt", splitByName(got))
	}

	// Errors.
	e := f.call("editSplits", "PUT", path, map[string]any{"splits": []map[string]any{
		{"person_name": "Big", "owed_amount": 400},
	}}, http.StatusBadRequest)
	if errCode(t, e) != "SPLITS_EXCEED_AMOUNT" {
		t.Errorf("over the amount: %s", errCode(t, e))
	}
	f.call("editSplits", "PUT", "/api/v1/transactions/"+uuid.New().String()+"/splits",
		map[string]any{"splits": []any{}}, http.StatusNotFound)
	// A co-member of the wallet sees the row (with its author) but may not
	// edit its splits.
	mid, _ := uuid.NewV7()
	f.exec(`INSERT INTO account_members (id, account_id, user_id, role, report_scope, joined_at)
		VALUES ($1, $2, $3, 'member', 'none', NOW())`, mid, f.walletA, f.partner)
	other := apitest.Engine(f.partner, routes(f.h))
	seen := decode[transactions.TransactionDetail](t, callAs(t, other, "getTransaction", "GET",
		"/api/v1/transactions/"+bill.ID.String(), nil, http.StatusOK))
	if seen.CreatedBy == nil || seen.CreatedBy.UserID != f.me || seen.CanEditSplits || seen.CanEditCategory ||
		seen.Splits == nil || len(*seen.Splits) != 0 || seen.SplitCount != 3 {
		t.Errorf("co-member view: created_by=%+v can_edit_splits=%v splits=%v split_count=%d",
			seen.CreatedBy, seen.CanEditSplits, seen.Splits, seen.SplitCount)
	}
	e = callAs(t, other, "editSplits", "PUT", path, map[string]any{"splits": []any{}}, http.StatusForbidden)
	if errCode(t, e) != "SPLITS_AUTHOR_ONLY" {
		t.Errorf("co-member edit: %s", errCode(t, e))
	}
}

// TestSetTagsHandler — setTransactionTags replaces the caller's tags only.
func TestSetTagsHandler(t *testing.T) {
	f := newHFix(t, true)
	created := decode[writeResult](t, f.call("createTransaction", "POST", "/api/v1/transactions", map[string]any{
		"type": "expense", "account_id": f.walletA, "amount": 10, "date": "2026-10-12",
		"tag_ids": []uuid.UUID{f.tagA},
	}, http.StatusCreated))
	id := created.Rows[0].ID
	path := "/api/v1/transactions/" + id.String() + "/tags"
	// The partner (a co-member) tags the same row with their own tag.
	mid, _ := uuid.NewV7()
	f.exec(`INSERT INTO account_members (id, account_id, user_id, role, report_scope, joined_at)
		VALUES ($1, $2, $3, 'member', 'none', NOW())`, mid, f.walletA, f.partner)
	other := apitest.Engine(f.partner, routes(f.h))
	theirs := decode[transactions.TransactionDetail](t, callAs(t, other, "setTransactionTags", "PUT", path,
		map[string]any{"tag_ids": []uuid.UUID{f.otherTag}}, http.StatusOK))
	if got := tagNames(theirs); len(got) != 1 || got[0] != "theirs" {
		t.Errorf("partner sees tags %v, want [theirs]", got)
	}

	body := map[string]any{"tag_ids": []uuid.UUID{f.tagC, f.tagB}}
	apitest.ConformRequest(t, "setTransactionTags", body)
	got := decode[transactions.TransactionDetail](t, f.call("setTransactionTags", "PUT", path, body, http.StatusOK))
	if names := tagNames(got); len(names) != 2 || names[0] != "beta" || names[1] != "gamma" || got.Splits == nil {
		t.Errorf("tags = %v, want [beta gamma]", names)
	}
	got = decode[transactions.TransactionDetail](t, f.call("setTransactionTags", "PUT", path,
		map[string]any{"tag_ids": []uuid.UUID{}}, http.StatusOK))
	if len(got.Tags) != 0 {
		t.Errorf("[] left %v", tagNames(got))
	}
	var n int
	_ = f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM transaction_tags WHERE transaction_id = $1 AND tag_id = $2`,
		id, f.otherTag).Scan(&n)
	if n != 1 {
		t.Errorf("the partner's tag was removed by my replace")
	}

	e := f.call("", "PUT", path, map[string]any{"tag_ids": []uuid.UUID{f.otherTag}}, http.StatusBadRequest)
	if errCode(t, e) != "TAG_NOT_FOUND" {
		t.Errorf("foreign tag: %s", errCode(t, e))
	}
	f.call("", "PUT", path, map[string]any{}, http.StatusBadRequest)
	f.call("setTransactionTags", "PUT", "/api/v1/transactions/"+uuid.New().String()+"/tags",
		map[string]any{"tag_ids": []uuid.UUID{}}, http.StatusNotFound)
}

// TestFormOptionsHandler — getTransactionFormOptions: wallets in the
// caller's order with the default, categories flat in tree order, tags by
// use, events the caller can add bills to, the fee category.
func TestFormOptionsHandler(t *testing.T) {
	f := newHFix(t, false)
	// B first in my order; an archived wallet is left out.
	f.exec(`UPDATE account_members SET sort_order = 1 WHERE account_id = $1`, f.walletA)
	f.exec(`UPDATE account_members SET sort_order = 0 WHERE account_id = $1`, f.walletB)
	archived := testdb.Account(t, f.pool, f.me)
	f.exec(`UPDATE accounts SET status = 'archived' WHERE id = $1`, archived)

	// Categories: Food (sort 1) > Lunch > Noodles; Home (sort 0); Salary
	// (income); a system one is left out.
	cat := func(name, typ string, parent *uuid.UUID, sortOrder int) uuid.UUID {
		id, _ := uuid.NewV7()
		f.exec(`INSERT INTO categories (id, user_id, name, type, is_system, parent_id, sort_order)
			VALUES ($1, $2, $3, $4, FALSE, $5, $6)`, id, f.me, name, typ, parent, sortOrder)
		return id
	}
	f.exec(`UPDATE categories SET sort_order = 1 WHERE id = $1`, f.food)
	home := cat("Home", "expense", nil, 0)
	lunch := cat("Lunch", "expense", &f.food, 0)
	noodles := cat("Noodles", "expense", &lunch, 0)
	salary := cat("Salary", "income", nil, 0)
	sysID, _ := uuid.NewV7()
	f.exec(`INSERT INTO categories (id, user_id, name, type, is_system, system_kind)
		VALUES ($1, $2, 'Adjust', 'income', TRUE, 'ADJUST_IN')`, sysID, f.me)

	// Events: an active one I contribute to; one where I'm a viewer and a
	// completed one are left out.
	event := func(name, status, role string) uuid.UUID {
		pid, _ := uuid.NewV7()
		f.exec(`INSERT INTO projects (id, owner_user_id, name, status) VALUES ($1, $2, $3, $4)`, pid, f.me, name, status)
		mid, _ := uuid.NewV7()
		f.exec(`INSERT INTO project_members (id, project_id, user_id, display_name, role, status)
			VALUES ($1, $2, $3, 'Me', $4, 'active')`, mid, pid, f.me, role)
		return pid
	}
	trip := event("Trip", "active", "owner")
	event("Watching", "active", "viewer")
	event("Done", "completed", "owner")

	// fee_category_id: unset → null.
	opts := decode[transactions.FormOptions](t,
		f.call("getTransactionFormOptions", "GET", "/api/v1/transactions/form-options", nil, http.StatusOK))
	if len(opts.Accounts) != 2 || opts.Accounts[0].ID != f.walletB || opts.Accounts[1].ID != f.walletA {
		t.Fatalf("accounts = %+v, want [B A]", opts.Accounts)
	}
	if opts.DefaultAccountID == nil || *opts.DefaultAccountID != f.walletB || !opts.Accounts[0].IsDefault || opts.Accounts[1].IsDefault {
		t.Errorf("default without history = %v, want the first wallet (B)", opts.DefaultAccountID)
	}
	if opts.FeeCategoryID != nil {
		t.Errorf("fee_category_id = %v, want null", opts.FeeCategoryID)
	}
	want := []struct {
		id     uuid.UUID
		depth  int
		parent string
	}{{home, 1, ""}, {f.food, 1, ""}, {lunch, 2, "Food"}, {noodles, 3, "Lunch"}, {salary, 1, ""}}
	if len(opts.Categories) != len(want) {
		t.Fatalf("categories = %d, want %d: %+v", len(opts.Categories), len(want), opts.Categories)
	}
	for i, w := range want {
		c := opts.Categories[i]
		parent := ""
		if c.ParentName != nil {
			parent = *c.ParentName
		}
		if c.ID != w.id || c.Depth != w.depth || parent != w.parent {
			t.Errorf("categories[%d] = %s depth %d parent %q, want %s depth %d parent %q",
				i, c.Name, c.Depth, parent, w.id, w.depth, w.parent)
		}
	}
	if len(opts.Events) != 1 || opts.Events[0].ID != trip {
		t.Errorf("events = %+v, want [Trip]", opts.Events)
	}

	// After an expense on A (with gamma), A is the default and gamma leads
	// the tags; a valid fee preference comes back.
	f.call("createTransaction", "POST", "/api/v1/transactions", map[string]any{
		"type": "expense", "account_id": f.walletA, "amount": 5, "date": "2026-10-12",
		"tag_ids": []uuid.UUID{f.tagC},
	}, http.StatusCreated)
	f.exec(`INSERT INTO user_preferences (user_id, preferences) VALUES ($1, jsonb_build_object('fee_category_id', $2::text))
		ON CONFLICT (user_id) DO UPDATE SET preferences = EXCLUDED.preferences`, f.me, home.String())
	opts = decode[transactions.FormOptions](t,
		f.call("getTransactionFormOptions", "GET", "/api/v1/transactions/form-options", nil, http.StatusOK))
	if opts.DefaultAccountID == nil || *opts.DefaultAccountID != f.walletA || !opts.Accounts[1].IsDefault {
		t.Errorf("default after use = %v, want A", opts.DefaultAccountID)
	}
	if len(opts.Tags) != 3 || opts.Tags[0].Name != "gamma" || opts.Tags[0].UsageCount != 1 || opts.Tags[1].Name != "alpha" {
		t.Errorf("tags = %+v, want gamma (used) then alpha, beta", opts.Tags)
	}
	if opts.FeeCategoryID == nil || *opts.FeeCategoryID != home {
		t.Errorf("fee_category_id = %v, want Home", opts.FeeCategoryID)
	}
	// A preference that is not one of my expense categories → null.
	f.exec(`UPDATE user_preferences SET preferences = jsonb_build_object('fee_category_id', $2::text) WHERE user_id = $1`,
		f.me, salary.String())
	opts = decode[transactions.FormOptions](t,
		f.call("getTransactionFormOptions", "GET", "/api/v1/transactions/form-options", nil, http.StatusOK))
	if opts.FeeCategoryID != nil {
		t.Errorf("fee_category_id on an income category = %v, want null", opts.FeeCategoryID)
	}
}

// TestSummaryHandler — getTransactionSummary: groups carry their refs.
func TestSummaryHandler(t *testing.T) {
	f := newHFix(t, true)
	lunch, _ := uuid.NewV7()
	f.exec(`INSERT INTO categories (id, user_id, name, type, is_system, parent_id) VALUES ($1, $2, 'Lunch', 'expense', FALSE, $3)`,
		lunch, f.me, f.food)
	for _, b := range []map[string]any{
		{"type": "expense", "account_id": f.walletA, "category_id": f.food, "amount": 100, "date": "2026-10-02"},
		{"type": "expense", "account_id": f.walletA, "category_id": lunch, "amount": 60, "date": "2026-10-03"},
		{"type": "expense", "account_id": f.walletB, "amount": 40, "date": "2026-10-04"},
		{"type": "income", "account_id": f.walletA, "amount": 500, "date": "2026-10-05"},
	} {
		f.call("createTransaction", "POST", "/api/v1/transactions", b, http.StatusCreated)
	}
	get := func(q string) transactions.SummaryResponse {
		t.Helper()
		return decode[transactions.SummaryResponse](t, f.call("getTransactionSummary", "GET",
			"/api/v1/transactions/summary?from=2026-10-01&to=2026-10-31"+q, nil, http.StatusOK))
	}

	plain := get("")
	if plain.TotalExpense != 200 || plain.TotalIncome != 500 || plain.TransactionCount != 4 ||
		plain.Groups == nil || len(plain.Groups) != 0 || plain.Currency != "THB" {
		t.Errorf("summary = %+v", plain)
	}
	byKey := func(r transactions.SummaryResponse) map[string]transactions.SummaryGroup {
		out := map[string]transactions.SummaryGroup{}
		for _, g := range r.Groups {
			out[g.Key] = g
		}
		return out
	}
	cats := byKey(get("&group_by=category&type=expense"))
	if g := cats[f.food.String()]; g.Total != 100 || g.Category == nil || g.Category.Name != "Food" || g.Account != nil {
		t.Errorf("category group Food = %+v", g)
	}
	if g := cats[lunch.String()]; g.Category == nil || g.Category.ParentID == nil || *g.Category.ParentID != f.food {
		t.Errorf("category group Lunch = %+v", g)
	}
	if g, ok := cats[""]; !ok || g.Total != 40 || g.Category != nil {
		t.Errorf("uncategorized group = %+v", g)
	}
	parents := byKey(get("&group_by=parent_category&type=expense"))
	if g := parents[f.food.String()]; g.Total != 160 || g.Category == nil || g.Category.ID != f.food {
		t.Errorf("parent group Food = %+v, want 160 with the Food ref", g)
	}
	accs := byKey(get("&group_by=account"))
	if g := accs[f.walletA.String()]; g.Account == nil || g.Account.Name != "Wallet A" || g.Expense != 160 || g.Income != 500 || g.Category != nil {
		t.Errorf("account group A = %+v", g)
	}
	months := get("&group_by=month")
	if len(months.Groups) != 1 || months.Groups[0].Key != "2026-10-01" || months.Groups[0].Account != nil {
		t.Errorf("month groups = %+v", months.Groups)
	}

	f.call("getTransactionSummary", "GET", "/api/v1/transactions/summary?from=2026-10-01&to=2026-10-31&group_by=year", nil, http.StatusBadRequest)
	f.call("getTransactionSummary", "GET", "/api/v1/transactions/summary?to=2026-10-31", nil, http.StatusBadRequest)
}
