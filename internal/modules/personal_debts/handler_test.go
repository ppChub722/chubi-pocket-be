package personal_debts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

type debtHFix struct {
	t       *testing.T
	ctx     context.Context
	pool    *pgxpool.Pool
	me      uuid.UUID
	partner uuid.UUID
	contact uuid.UUID // my contact for partner (linked)
	wallet  uuid.UUID
	tag     uuid.UUID
	txs     *transactions.Service
	svc     *Service
	r       http.Handler
	exec    func(q string, args ...any)
}

func newDebtHFix(t *testing.T) *debtHFix {
	t.Helper()
	pool := testdb.Pool(t)
	ctx := context.Background()
	f := &debtHFix{t: t, ctx: ctx, pool: pool}
	f.me = testdb.User(t, pool)
	f.partner = testdb.User(t, pool)
	f.exec = func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	cats := categories.NewService(categories.NewStore(pool))
	seed, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := cats.SeedForUser(ctx, seed, f.me); err != nil {
		t.Fatal(err)
	}
	if err := seed.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	f.txs = transactions.NewService(transactions.NewStore(pool), cats)
	f.svc = NewService(NewStore(pool), f.txs)
	f.txs.WithDebtsCreator(f.svc.CreateForTransactionTx)
	f.txs.WithSplitsEditor(f.svc.EditForTransactionTx)
	f.txs.WithDebtLinks(f.svc)
	h := NewHandler(f.svc)
	f.r = apitest.Engine(f.me, func(g *gin.RouterGroup) {
		g.GET("/personal-debts/people", h.People)
		g.GET("/personal-debts/people/:key", h.Person)
		g.GET("/personal-debts/people/:key/entries", h.Entries)
		g.POST("/personal-debts/people/:key/entries", h.CreateEntry)
		g.GET("/personal-debts/:id", h.Get)
		g.PUT("/personal-debts/:id", h.Update)
		g.DELETE("/personal-debts/:id", h.Delete)
	})
	f.exec(`UPDATE users SET display_name = 'Bee Live' WHERE id = $1`, f.partner)
	f.wallet = testdb.Account(t, pool, f.me)
	f.exec(`UPDATE accounts SET balance = 1000 WHERE id = $1`, f.wallet)
	f.contact = uuid.New()
	f.exec(`INSERT INTO contacts (id, user_id, display_name, linked_user_id, status)
		VALUES ($1, $2, 'Bee old', $3, 'active')`, f.contact, f.me, f.partner)
	f.tag = uuid.New()
	f.exec(`INSERT INTO tags (id, user_id, name) VALUES ($1, $2, 'family')`, f.tag, f.me)
	// Runs before the users' own cleanups (my contact links the partner).
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM personal_debts WHERE user_id = ANY($1)`, []uuid.UUID{f.me, f.partner})
		_, _ = pool.Exec(ctx, `DELETE FROM contacts WHERE user_id = ANY($1)`, []uuid.UUID{f.me, f.partner})
	})
	return f
}

// call sends a request as me, checks the status and (with op) the spec.
func (f *debtHFix) call(method, path string, body any, op string, want int) map[string]any {
	f.t.Helper()
	status, raw := apitest.Do(f.t, f.r, method, path, body)
	if status != want {
		f.t.Fatalf("%s %s: status %d, want %d; body %s", method, path, status, want, raw)
	}
	if op != "" {
		apitest.Conform(f.t, op, status, raw)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func (f *debtHFix) code(method, path string, body any, want int, code string) {
	f.t.Helper()
	status, raw := apitest.Do(f.t, f.r, method, path, body)
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	if status != want || e.Error.Code != code {
		f.t.Errorf("%s %s: %d %s, want %d %s (%s)", method, path, status, e.Error.Code, want, code, raw)
	}
}

func (f *debtHFix) balance() float64 {
	f.t.Helper()
	var b float64
	if err := f.pool.QueryRow(f.ctx, `SELECT balance FROM accounts WHERE id = $1`, f.wallet).Scan(&b); err != nil {
		f.t.Fatal(err)
	}
	return b
}

func obj(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		m, _ = m[k].(map[string]any)
	}
	return m
}

func num(m map[string]any, keys ...string) float64 {
	v, _ := obj(m, keys[:len(keys)-1]...)[keys[len(keys)-1]].(float64)
	return v
}

func entriesPath(key string) string {
	return "/api/v1/personal-debts/people/" + url.PathEscape(key) + "/entries"
}

// TestDebtLedgerFlow — the person ledger end to end: entries with and
// without a wallet, the people list, the person page, edits and deletes
// keeping the entry and its wallet transaction one record.
func TestDebtLedgerFlow(t *testing.T) {
	f := newDebtHFix(t)

	// I paid Mom 200 from the wallet, tagged: the wallet tx is written.
	lend := f.call("POST", entriesPath("n:Mom"), map[string]any{
		"direction": "paid", "amount": 200, "account_id": f.wallet, "date": "2026-10-01",
		"description": "Lunch money", "tag_ids": []uuid.UUID{f.tag},
	}, "createDebtEntry", http.StatusCreated)
	lendID := obj(lend, "entry")["id"].(string)
	if k := obj(lend, "entry")["key"]; k != "n:mom" {
		t.Errorf("key = %v, want n:mom", k)
	}
	if num(lend, "person_balance", "net") != 200 || obj(lend, "person_balance", "person")["name"] != "Mom" {
		t.Errorf("person_balance = %v", lend["person_balance"])
	}
	tx := obj(lend, "transaction")
	if tx == nil || tx["type"] != "expense" || obj(tx, "counterparty")["key"] != "n:mom" ||
		obj(tx, "counterparty")["entry_id"] != lendID || obj(tx, "category")["system_kind"] != "DEBT_PAID" {
		t.Fatalf("linked tx = %v", tx)
	}
	if tags, _ := tx["tags"].([]any); len(tags) != 1 {
		t.Errorf("tx tags = %v, want family", tx["tags"])
	}
	if f.balance() != 800 {
		t.Errorf("wallet = %v, want 800", f.balance())
	}

	// Tags need a wallet.
	f.code("POST", entriesPath("n:mom"), map[string]any{
		"direction": "received", "amount": 50, "tag_ids": []uuid.UUID{f.tag},
	}, http.StatusBadRequest, "TAGS_NEED_WALLET")

	// Mom paid me 50 back, ledger only, and the loan is labelled paid.
	back := f.call("POST", entriesPath("n:mom"), map[string]any{
		"direction": "received", "amount": 50, "date": "2026-10-02", "mark_paid_ids": []string{lendID},
	}, "createDebtEntry", http.StatusCreated)
	backID := obj(back, "entry")["id"].(string)
	if back["transaction"] != nil || num(back, "person_balance", "net") != 150 || num(back, "person_balance", "open_count") != 1 {
		t.Errorf("ledger-only entry = %v", back)
	}
	f.code("POST", entriesPath("n:mom"), map[string]any{
		"direction": "received", "amount": 1, "mark_paid_ids": []uuid.UUID{uuid.New()},
	}, http.StatusBadRequest, "ENTRY_NOT_FOUND")

	// A contact person: the linked user's live name.
	bee := f.call("POST", entriesPath("c:"+f.contact.String()), map[string]any{
		"direction": "paid", "amount": 100, "date": "2026-10-03",
	}, "createDebtEntry", http.StatusCreated)
	if p := obj(bee, "person_balance", "person"); p["name"] != "Bee Live" || p["linked"] != true {
		t.Errorf("contact person = %v", p)
	}
	f.code("POST", entriesPath("c:"+uuid.NewString()), map[string]any{"direction": "paid", "amount": 1},
		http.StatusBadRequest, "CONTACT_NOT_FOUND")
	f.code("POST", entriesPath("x:nope"), map[string]any{"direction": "paid", "amount": 1},
		http.StatusBadRequest, "INVALID_PERSON_KEY")

	// People: one list, |net| first; the totals ignore the filters.
	people := f.call("GET", "/api/v1/personal-debts/people", nil, "listDebtPeople", http.StatusOK)
	rows, _ := people["data"].([]any)
	if len(rows) != 2 || rows[0].(map[string]any)["key"] != "n:mom" {
		t.Fatalf("people = %v", rows)
	}
	if num(people, "totals", "owed_to_me") != 250 || num(people, "totals", "i_owe") != 0 ||
		num(people, "totals", "net") != 250 || num(people, "totals", "open_count") != 2 {
		t.Errorf("totals = %v", people["totals"])
	}
	if got := f.call("GET", "/api/v1/personal-debts/people?q=bee", nil, "listDebtPeople", http.StatusOK); len(got["data"].([]any)) != 1 {
		t.Errorf("q=bee = %v", got["data"])
	}
	if got := f.call("GET", "/api/v1/personal-debts/people?side=i_owe", nil, "listDebtPeople", http.StatusOK); len(got["data"].([]any)) != 0 ||
		num(got, "totals", "owed_to_me") != 250 {
		t.Errorf("side=i_owe = %v", got)
	}

	// The person page.
	mom := f.call("GET", "/api/v1/personal-debts/people/n:mom", nil, "getDebtPerson", http.StatusOK)
	if num(mom, "net") != 150 || num(mom, "counts", "entries") != 2 || num(mom, "counts", "money") != 1 || len(mom["items"].([]any)) != 2 {
		t.Errorf("person page = %v", mom)
	}
	f.code("GET", "/api/v1/personal-debts/people/n:nobody", nil, http.StatusNotFound, "NOT_FOUND")
	f.code("GET", "/api/v1/personal-debts/people/bad", nil, http.StatusBadRequest, "INVALID_PERSON_KEY")
	empty := uuid.New()
	f.exec(`INSERT INTO contacts (id, user_id, display_name, status) VALUES ($1, $2, 'Fresh', 'active')`, empty, f.me)
	if p := f.call("GET", "/api/v1/personal-debts/people/c:"+empty.String(), nil, "getDebtPerson", http.StatusOK); num(p, "net") != 0 ||
		obj(p, "person")["name"] != "Fresh" {
		t.Errorf("contact without entries = %v", p)
	}

	// Entries: filters and sort.
	for q, want := range map[string]int{"money=true": 1, "direction=received": 1, "paid=false": 1, "q=lunch": 1} {
		got := f.call("GET", entriesPath("n:mom")+"?"+q, nil, "listDebtEntries", http.StatusOK)
		if n := len(got["data"].([]any)); n != want {
			t.Errorf("entries?%s = %d, want %d", q, n, want)
		}
	}
	asc := f.call("GET", entriesPath("n:mom")+"?sort=amount_asc", nil, "listDebtEntries", http.StatusOK)
	if first := asc["data"].([]any)[0].(map[string]any); first["id"] != backID {
		t.Errorf("amount_asc first = %v", first)
	}

	// Entry detail = the tx-detail page in debt mode.
	d := f.call("GET", "/api/v1/personal-debts/"+lendID, nil, "getDebtEntry", http.StatusOK)
	if obj(d, "entry")["paid"] != true || obj(d, "transaction")["id"] == nil || num(d, "person_balance", "net") != 150 {
		t.Errorf("entry detail = %v", d)
	}
	txID := uuid.MustParse(obj(d, "transaction")["id"].(string))
	f.code("GET", "/api/v1/personal-debts/"+uuid.NewString(), nil, http.StatusNotFound, "NOT_FOUND")

	// Edit the entry: the wallet tx follows (one record).
	up := f.call("PUT", "/api/v1/personal-debts/"+lendID, map[string]any{
		"amount": 300, "description": "Rent help",
	}, "updateDebtEntry", http.StatusOK)
	if num(up, "transaction", "amount") != 300 || obj(up, "transaction")["description"] != "Rent help" ||
		num(up, "person_balance", "net") != 250 || f.balance() != 700 {
		t.Errorf("edit entry: %v (wallet %v)", up, f.balance())
	}
	if bs, _ := up["account_balances"].([]any); len(bs) != 1 {
		t.Errorf("account_balances = %v", up["account_balances"])
	}

	// Edit the tx: the entry follows, and the write carries the person's balance.
	amt := 250.0
	w, err := f.txs.UpdateWrite(f.ctx, f.me, txID, transactions.UpdateRequest{Amount: &amt})
	if err != nil {
		t.Fatalf("edit the wallet side: %v", err)
	}
	if pb, ok := w.PersonBalance.(DebtPerson); !ok || pb.Net != 200 {
		t.Errorf("tx write person_balance = %+v", w.PersonBalance)
	}
	if e, _ := f.svc.GetEntry(f.ctx, f.me, uuid.MustParse(lendID)); e.Entry.Amount != 250 {
		t.Errorf("entry after a tx edit = %v, want 250", e.Entry.Amount)
	}
	cat := testdb.Category(t, f.pool, f.me, "Food")
	if _, err := f.txs.UpdateWrite(f.ctx, f.me, txID, transactions.UpdateRequest{CategoryID: &cat}); err == nil {
		t.Errorf("a debt tx's category must stay")
	}

	// Ledger-only: the tx goes, the money comes back.
	lo := f.call("PUT", "/api/v1/personal-debts/"+lendID, map[string]any{"account_id": nil}, "updateDebtEntry", http.StatusOK)
	if lo["transaction"] != nil || f.balance() != 1000 {
		t.Errorf("ledger-only: %v (wallet %v)", lo["transaction"], f.balance())
	}
	// ... and back onto the wallet.
	re := f.call("PUT", "/api/v1/personal-debts/"+lendID, map[string]any{"account_id": f.wallet}, "updateDebtEntry", http.StatusOK)
	if re["transaction"] == nil || f.balance() != 750 {
		t.Errorf("wallet again: %v (wallet %v)", re["transaction"], f.balance())
	}

	// Move the entry to another person: both balances come back.
	mv := f.call("PUT", "/api/v1/personal-debts/"+backID, map[string]any{"key": "c:" + f.contact.String()}, "updateDebtEntry", http.StatusOK)
	if num(mv, "person_balance", "net") != 50 || num(mv, "previous_person_balance", "net") != 250 {
		t.Errorf("moved: now %v, before %v", mv["person_balance"], mv["previous_person_balance"])
	}

	// Delete: the entry and its tx.
	del := f.call("DELETE", "/api/v1/personal-debts/"+lendID, nil, "deleteDebtEntry", http.StatusOK)
	if del["transaction_id"] == nil || f.balance() != 1000 || num(del, "person_balance", "net") != 0 {
		t.Errorf("delete: %v (wallet %v)", del, f.balance())
	}
	var txLeft int
	_ = f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM transactions WHERE source_personal_debt_id IS NOT NULL AND user_id = $1`, f.me).Scan(&txLeft)
	if txLeft != 0 {
		t.Errorf("%d debt txs left after the delete", txLeft)
	}

	// Deleting the tx side deletes the entry too.
	again := f.call("POST", entriesPath("n:mom"), map[string]any{
		"direction": "paid", "amount": 40, "account_id": f.wallet,
	}, "createDebtEntry", http.StatusCreated)
	againTx := uuid.MustParse(obj(again, "transaction")["id"].(string))
	dr, err := f.txs.DeleteWrite(f.ctx, f.me, againTx, false)
	if err != nil || dr.DebtEntryID == nil || dr.DebtEntryID.String() != obj(again, "entry")["id"] {
		t.Fatalf("delete the wallet side: %+v, %v", dr, err)
	}
	f.code("GET", "/api/v1/personal-debts/"+dr.DebtEntryID.String(), nil, http.StatusNotFound, "NOT_FOUND")
}

// TestSplitSourcedEntries — a split on my bill is an entry the bill owns:
// only description / note / paid change here.
func TestSplitSourcedEntries(t *testing.T) {
	f := newDebtHFix(t)
	created, err := f.txs.Create(f.ctx, f.me, transactions.CreateRequest{
		Type: transactions.TypeExpense, AccountID: &f.wallet, Amount: 300, Date: "2026-10-05",
		Description: testdb.Str("Dinner"),
		Splits:      []transactions.SplitInput{{PersonName: "Grandma", OwedAmount: 100}},
	})
	if err != nil {
		t.Fatalf("split bill: %v", err)
	}
	bill := created.(*transactions.TransactionDetail)
	split := (*bill.Splits)[0]
	if split.Key != "n:grandma" || split.Direction != DirectionPaid {
		t.Fatalf("split = %+v", split)
	}
	id := split.DebtID.String()
	d := f.call("GET", "/api/v1/personal-debts/"+id, nil, "getDebtEntry", http.StatusOK)
	src := obj(d, "entry", "source")
	if src["kind"] != "transaction" || src["id"] != bill.ID.String() || src["title"] != "Dinner" || d["transaction"] != nil {
		t.Errorf("split entry = %v", d)
	}
	f.code("PUT", "/api/v1/personal-debts/"+id, map[string]any{"amount": 50}, http.StatusConflict, "ENTRY_MANAGED_BY_SOURCE")
	f.code("PUT", "/api/v1/personal-debts/"+id, map[string]any{"account_id": f.wallet}, http.StatusConflict, "ENTRY_MANAGED_BY_SOURCE")
	ok := f.call("PUT", "/api/v1/personal-debts/"+id, map[string]any{"paid": true, "note": "cash at home"}, "updateDebtEntry", http.StatusOK)
	if obj(ok, "entry")["paid"] != true || obj(ok, "entry")["note"] != "cash at home" || num(ok, "entry", "amount") != 100 {
		t.Errorf("label + note = %v", ok["entry"])
	}
	// Deleting it takes the split off the bill.
	f.call("DELETE", "/api/v1/personal-debts/"+id, nil, "deleteDebtEntry", http.StatusOK)
	after, _ := f.txs.Get(f.ctx, f.me, bill.ID)
	if after.SplitCount != 0 || after.MyShare == nil || *after.MyShare != 300 {
		t.Errorf("bill after deleting its split: count %d share %v", after.SplitCount, after.MyShare)
	}
}

// The split-identity rule is enforced by the hook itself.
func TestSplitIdentityRuleInHook(t *testing.T) {
	f := newSplitEditFixture(t)
	s := f.splits(t)
	beam := s["Beam"].DebtID
	partner := s["Partner"]
	if partner.PersonName != "Partner" {
		t.Fatalf("a linked split shows the contact's resolved name, got %q", partner.PersonName)
	}
	storedName := func(id uuid.UUID) string {
		t.Helper()
		var n string
		if err := f.pool.QueryRow(f.ctx, `SELECT counterparty_person_name FROM personal_debts WHERE id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Same contact + a different name: ignored (no in-place rename).
	e := keep(partner, 100)
	e.PersonName, e.ContactID, e.ContactIDSet = "Hacked", &f.partnerContact, true
	if err := f.edit(t, keep(s["Beam"], 100), e); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if n := storedName(partner.DebtID); n != "Partner" {
		t.Errorf("stored name = %q, want it unchanged", n)
	}

	// New contact: its resolved name wins over any sent name.
	zed := uuid.New()
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO contacts (id, user_id, display_name, status) VALUES ($1, $2, 'Zed', 'active')`, zed, f.me); err != nil {
		t.Fatal(err)
	}
	be := keep(s["Beam"], 100)
	be.PersonName, be.ContactID, be.ContactIDSet = "Other", &zed, true
	if err := f.edit(t, be, keep(partner, 100)); err != nil {
		t.Fatalf("switch contact: %v", err)
	}
	if n := storedName(beam); n != "Zed" {
		t.Errorf("new contact's name = %q, want Zed (the sent name is ignored)", n)
	}

	// A free-name split still renames in place (Beam was dropped back to a
	// free name first).
	back := keep(s["Beam"], 100)
	back.PersonName, back.ContactID, back.ContactIDSet = "Beam", nil, true
	if err := f.edit(t, back, keep(partner, 100)); err != nil {
		t.Fatalf("drop contact: %v", err)
	}
	ren := keep(s["Beam"], 100)
	ren.PersonName = "Beamy"
	if err := f.edit(t, ren, keep(partner, 100)); err != nil {
		t.Fatalf("free rename: %v", err)
	}
	if n := storedName(beam); n != "Beamy" {
		t.Errorf("free-name rename = %q, want Beamy", n)
	}
}

// TestTransactionFilters — GET /transactions?counterparty_key (a person's
// wallet sides) and ?can_join_event (the event bill picker).
func TestTransactionFilters(t *testing.T) {
	f := newDebtHFix(t)
	entry, err := f.svc.CreateEntry(f.ctx, f.me, "n:mom", CreateEntryRequest{
		Direction: DirectionPaid, Amount: 40, AccountID: &f.wallet,
	})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := f.txs.Create(f.ctx, f.me, transactions.CreateRequest{
		Type: transactions.TypeExpense, AccountID: &f.wallet, Amount: 10, Date: "2026-10-05",
	})
	if err != nil {
		t.Fatal(err)
	}
	split, err := f.txs.Create(f.ctx, f.me, transactions.CreateRequest{
		Type: transactions.TypeExpense, AccountID: &f.wallet, Amount: 90, Date: "2026-10-05",
		Splits: []transactions.SplitInput{{PersonName: "Aom", OwedAmount: 30}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := func(fl transactions.ListFilter) map[uuid.UUID]bool {
		t.Helper()
		fl.Page, fl.PerPage = 1, 50
		res, err := f.txs.List(f.ctx, f.me, fl)
		if err != nil {
			t.Fatal(err)
		}
		out := map[uuid.UUID]bool{}
		for _, r := range res.Data {
			out[r.ID] = true
		}
		return out
	}
	if got := ids(transactions.ListFilter{CounterpartyKey: "n:mom"}); len(got) != 1 || !got[entry.Transaction.ID] {
		t.Errorf("counterparty_key=n:mom = %v, want only the entry's tx", got)
	}
	plainID := plain.(*transactions.TransactionDetail).ID
	splitBill := split.(*transactions.TransactionDetail)
	got := ids(transactions.ListFilter{CanJoinEvent: true})
	if !got[plainID] || !got[splitBill.ID] || got[entry.Transaction.ID] {
		t.Errorf("can_join_event = %v, want the plain + split bills, not the debt tx", got)
	}
	f.exec(`UPDATE personal_debts SET paid = TRUE WHERE id = $1`, (*splitBill.Splits)[0].DebtID)
	if got := ids(transactions.ListFilter{CanJoinEvent: true}); got[splitBill.ID] {
		t.Errorf("a bill with a split labelled paid can't join an event")
	}
}

// TestContactsAndDebtsAreIndependent — archiving a contact only hides it:
// new entries (a debt and a repayment) with it still work and still bump
// last_used_at; deleting the contact drops every record back to a typed
// name (name kept, contact id NULL, amounts unchanged).
func TestContactsAndDebtsAreIndependent(t *testing.T) {
	f := newDebtHFix(t)
	key := "c:" + f.contact.String()
	f.call("POST", entriesPath(key), map[string]any{"direction": "paid", "amount": 100, "date": "2026-10-01"},
		"createDebtEntry", http.StatusCreated)

	f.exec(`UPDATE contacts SET status = 'archived', last_used_at = NULL WHERE id = $1`, f.contact)
	f.exec(`UPDATE contacts SET nickname = 'Beebee', show_nickname = TRUE WHERE id = $1`, f.contact)
	f.call("POST", entriesPath(key), map[string]any{"direction": "paid", "amount": 50, "date": "2026-10-02"},
		"createDebtEntry", http.StatusCreated)
	f.call("POST", entriesPath(key), map[string]any{"direction": "received", "amount": 30, "date": "2026-10-03"},
		"createDebtEntry", http.StatusCreated)
	var used *time.Time
	if err := f.pool.QueryRow(f.ctx, `SELECT last_used_at FROM contacts WHERE id = $1`, f.contact).Scan(&used); err != nil || used == nil {
		t.Errorf("last_used_at = %v (%v), want bumped", used, err)
	}

	// Delete the contact (as the contacts module does: snapshot, then delete).
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RestoreOnContactDeleteTx(f.ctx, tx, f.me, f.contact); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.ctx, `DELETE FROM contacts WHERE id = $1`, f.contact); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	var n, linked int
	var sum float64
	var names string
	if err := f.pool.QueryRow(f.ctx, `SELECT COUNT(*), COUNT(counterparty_contact_id), SUM(amount)::float8,
		MIN(counterparty_person_name) || '/' || MAX(counterparty_person_name) FROM personal_debts WHERE user_id = $1`, f.me).
		Scan(&n, &linked, &sum, &names); err != nil {
		t.Fatal(err)
	}
	if n != 3 || linked != 0 || sum != 180 || names != "Beebee/Beebee" {
		t.Errorf("after contact delete: rows %d linked %d sum %v names %q", n, linked, sum, names)
	}
	people := f.call("GET", "/api/v1/personal-debts/people", nil, "listDebtPeople", http.StatusOK)
	if d := people["data"].([]any); len(d) != 1 {
		t.Errorf("people = %v, want one typed-name person", people)
	}
}

// TestDebtCyclesAndSummary — a person's cycle ends where the balance hit 0:
// entries split into current / earlier, days_outstanding counts the current
// cycle's oldest open entry, the totals count people per side and the ones
// open for over 30 days, and the summary message lists the current cycle.
func TestDebtCyclesAndSummary(t *testing.T) {
	f := newDebtHFix(t)
	day := func(n int) string { return time.Now().AddDate(0, 0, n).Format("2006-01-02") }
	add := func(key, dir string, amount float64, date string) {
		f.call("POST", entriesPath(key), map[string]any{"direction": dir, "amount": amount, "date": date},
			"createDebtEntry", http.StatusCreated)
	}
	add("n:Mom", "paid", 100, day(-40))
	add("n:Mom", "received", 100, day(-30)) // balance 0 → a cycle ends here
	add("n:Mom", "paid", 50, day(-5))
	add("n:Mom", "paid", 20, day(-2))
	add("n:Dad", "paid", 10, day(-40)) // open for over 30 days

	page := f.call("GET", "/api/v1/personal-debts/people/n:mom", nil, "getDebtPerson", http.StatusOK)
	if page["cleared_at"] != day(-30) || num(page, "net") != 70 || num(page, "days_outstanding") < 4 || num(page, "days_outstanding") > 6 {
		t.Errorf("person = cleared %v days %v net %v", page["cleared_at"], page["days_outstanding"], page["net"])
	}
	if num(page, "counts", "entries") != 4 || num(page, "counts", "current") != 2 || num(page, "counts", "earlier") != 2 {
		t.Errorf("counts = %v", page["counts"])
	}
	cur := 0
	for _, it := range page["items"].([]any) {
		if b, ok := it.(map[string]any)["in_current_cycle"].(bool); ok && b {
			cur++
		}
	}
	if cur != 2 || len(page["items"].([]any)) != 2 {
		t.Errorf("person page items (default = current cycle) = %d, flagged %d, want 2 / 2", len(page["items"].([]any)), cur)
	}
	all := f.call("GET", "/api/v1/personal-debts/people/n:mom?cycle=all", nil, "getDebtPerson", http.StatusOK)
	if len(all["items"].([]any)) != 4 {
		t.Errorf("cycle=all items = %d, want 4", len(all["items"].([]any)))
	}
	el := f.call("GET", "/api/v1/personal-debts/people/n:mom/entries?cycle=earlier", nil, "listDebtEntries", http.StatusOK)
	if len(el["data"].([]any)) != 2 {
		t.Errorf("earlier entries = %v", el["data"])
	}
	f.call("GET", "/api/v1/personal-debts/people/n:mom/entries?cycle=nope", nil, "", http.StatusBadRequest)

	people := f.call("GET", "/api/v1/personal-debts/people", nil, "listDebtPeople", http.StatusOK)
	if num(people, "totals", "owed_to_me_people") != 2 || num(people, "totals", "i_owe_people") != 0 || num(people, "totals", "over_30_days_people") != 1 {
		t.Errorf("totals = %v", people["totals"])
	}

	msg, _ := page["summary_message"].(map[string]any)
	text, _ := msg["text"].(string)
	if num(msg, "entries") != 2 || num(msg, "net") != 70 || !strings.Contains(text, "รวม Mom ค้างจ่ายฉัน 70 บาท") ||
		!strings.Contains(text, "+50") || strings.Contains(text, "100") {
		t.Errorf("message = %q", text)
	}
}
