package personal_debts

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

type debtHFix struct {
	t       *testing.T
	me      uuid.UUID
	partner uuid.UUID
	contact uuid.UUID // my contact for partner (linked)
	wallet  uuid.UUID
	r       http.Handler
	exec    func(q string, args ...any)
}

func newDebtHFix(t *testing.T) *debtHFix {
	t.Helper()
	pool := testdb.Pool(t)
	ctx := context.Background()
	f := &debtHFix{t: t}
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
	txs := transactions.NewService(transactions.NewStore(pool), cats)
	svc := NewService(NewStore(pool), txs)
	txs.WithDebtsCreator(svc.CreateForTransactionTx)
	txs.WithSplitsEditor(svc.EditForTransactionTx)
	txs.WithDebtValidator(svc.ValidateOwnership)
	txs.WithDebtAutoBumper(svc.AutoBumpInTx)
	txs.WithDebtSettledAdjuster(svc.AdjustSettledInTx)
	h := NewHandler(svc)
	f.r = apitest.Engine(f.me, func(g *gin.RouterGroup) {
		g.GET("/personal-debts/people", h.People)
		g.GET("/personal-debts", h.List)
		g.POST("/personal-debts", h.Create)
		g.GET("/personal-debts/:id", h.Get)
		g.PUT("/personal-debts/:id", h.Update)
		g.POST("/personal-debts/:id/cancel", h.Cancel)
		g.POST("/personal-debts/:id/settle", h.Settle)
	})
	f.exec(`UPDATE users SET display_name = 'Bee Live' WHERE id = $1`, f.partner)
	f.wallet = testdb.Account(t, pool, f.me)
	f.exec(`UPDATE accounts SET balance = 1000 WHERE id = $1`, f.wallet)
	f.contact = uuid.New()
	f.exec(`INSERT INTO contacts (id, user_id, display_name, linked_user_id, status)
		VALUES ($1, $2, 'Bee old', $3, 'active')`, f.contact, f.me, f.partner)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM personal_debts WHERE user_id = ANY($1)`, []uuid.UUID{f.me, f.partner})
		_, _ = pool.Exec(ctx, `DELETE FROM contacts WHERE user_id = ANY($1)`, []uuid.UUID{f.me, f.partner})
	})
	return f
}

func (f *debtHFix) call(method, path string, body any) (int, []byte) {
	f.t.Helper()
	return apitest.Do(f.t, f.r, method, path, body)
}

func personOf(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, old := m["counterparty_person_name"]; old {
		t.Errorf("counterparty_person_name must not be returned: %s", raw)
	}
	p, _ := m["person"].(map[string]any)
	if p == nil {
		t.Fatalf("no person in %s", raw)
	}
	return p
}

func TestDebtsPersonRefAndPeople(t *testing.T) {
	f := newDebtHFix(t)

	status, body := f.call("POST", "/api/v1/personal-debts", map[string]any{
		"direction": "owed_to_me", "counterparty_contact_id": f.contact, "counterparty_person_name": "typed",
		"amount": 100, "currency": "THB",
	})
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	p := personOf(t, body)
	if p["name"] != "Bee Live" || p["contact_id"] != f.contact.String() || p["linked"] != true || p["linked_user_id"] != f.partner.String() {
		t.Errorf("create: person = %v, want the contact resolved to the linked user's live name", p)
	}
	var created map[string]any
	_ = json.Unmarshal(body, &created)
	id := created["id"].(string)

	// A free-name debt: the snapshot is the name.
	status, body = f.call("POST", "/api/v1/personal-debts", map[string]any{
		"direction": "i_owe", "counterparty_person_name": "Grandma", "amount": 30, "currency": "THB",
	})
	if status != http.StatusCreated {
		t.Fatalf("create free: %d %s", status, body)
	}
	if p := personOf(t, body); p["name"] != "Grandma" || p["contact_id"] != nil || p["linked"] != false {
		t.Errorf("free person = %v", p)
	}

	// Live name: the partner renames themself, then a nickname wins.
	f.exec(`UPDATE users SET display_name = 'Bee Renamed' WHERE id = $1`, f.partner)
	status, body = f.call("GET", "/api/v1/personal-debts/"+id, nil)
	apitest.Conform(t, "getDebt", status, body)
	if p := personOf(t, body); p["name"] != "Bee Renamed" {
		t.Errorf("get: name = %v, want the live profile name", p["name"])
	}
	f.exec(`UPDATE contacts SET nickname = 'Beebee', show_nickname = TRUE WHERE id = $1`, f.contact)

	status, body = f.call("GET", "/api/v1/personal-debts", nil)
	apitest.Conform(t, "listDebts", status, body)
	var list struct {
		Data []json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(body, &list)
	if len(list.Data) != 2 {
		t.Fatalf("list = %d, want 2", len(list.Data))
	}
	names := map[string]bool{}
	for _, d := range list.Data {
		names[personOf(t, d)["name"].(string)] = true
	}
	if !names["Beebee"] || !names["Grandma"] {
		t.Errorf("list names = %v, want nickname + snapshot", names)
	}

	status, body = f.call("GET", "/api/v1/personal-debts/people", nil)
	apitest.Conform(t, "listDebtPeople", status, body)
	var people struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(body, &people)
	if len(people.Data) != 2 {
		t.Fatalf("people = %d, want 2: %s", len(people.Data), body)
	}
	for _, row := range people.Data {
		pp := row["person"].(map[string]any)
		if _, old := row["display_name"]; old {
			t.Errorf("display_name must be gone: %v", row)
		}
		if _, old := row["icon_code"]; old {
			t.Errorf("icon_code must be gone: %v", row)
		}
		switch pp["name"] {
		case "Beebee":
			if row["contact_id"] != f.contact.String() || row["owed_to_me_open"] != float64(100) {
				t.Errorf("Bee row = %v", row)
			}
		case "Grandma":
			if row["contact_id"] != nil || row["i_owe_open"] != float64(30) {
				t.Errorf("Grandma row = %v", row)
			}
		default:
			t.Errorf("unexpected person %v", pp)
		}
	}

	// update / cancel carry person too.
	status, body = f.call("PUT", "/api/v1/personal-debts/"+id, map[string]any{"note": "n"})
	if status != http.StatusOK {
		t.Fatalf("update: %d %s", status, body)
	}
	if p := personOf(t, body); p["name"] != "Beebee" {
		t.Errorf("update: person = %v", p)
	}
	status, body = f.call("POST", "/api/v1/personal-debts/"+id+"/cancel", nil)
	if status != http.StatusOK {
		t.Fatalf("cancel: %d %s", status, body)
	}
	personOf(t, body)
}

func TestSettleDebtAccountBalances(t *testing.T) {
	f := newDebtHFix(t)
	status, body := f.call("POST", "/api/v1/personal-debts", map[string]any{
		"direction": "owed_to_me", "counterparty_contact_id": f.contact, "counterparty_person_name": "x",
		"amount": 100, "currency": "THB",
	})
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	var d map[string]any
	_ = json.Unmarshal(body, &d)
	id := d["id"].(string)

	type balances struct {
		AccountBalances []struct {
			AccountID string  `json:"account_id"`
			Balance   float64 `json:"balance"`
		} `json:"account_balances"`
		Debt map[string]any `json:"debt"`
	}
	// With a wallet: owed_to_me -> income lands in it.
	status, body = f.call("POST", "/api/v1/personal-debts/"+id+"/settle", map[string]any{"account_id": f.wallet, "amount": 40})
	if status != http.StatusOK {
		t.Fatalf("settle: %d %s", status, body)
	}
	apitest.Conform(t, "settleDebt", status, body)
	var b balances
	_ = json.Unmarshal(body, &b)
	if len(b.AccountBalances) != 1 || b.AccountBalances[0].AccountID != f.wallet.String() || b.AccountBalances[0].Balance != 1040 {
		t.Errorf("account_balances = %+v, want the wallet at 1040", b.AccountBalances)
	}
	if p, _ := b.Debt["person"].(map[string]any); p == nil || p["name"] != "Bee Live" {
		t.Errorf("settle result debt person = %v", b.Debt["person"])
	}
	if _, old := b.Debt["counterparty_person_name"]; old {
		t.Errorf("counterparty_person_name still in the settle result")
	}

	// Without a wallet: nothing touched.
	status, body = f.call("POST", "/api/v1/personal-debts/"+id+"/settle", map[string]any{"amount": 10})
	if status != http.StatusOK {
		t.Fatalf("settle floating: %d %s", status, body)
	}
	apitest.Conform(t, "settleDebt", status, body)
	b = balances{}
	_ = json.Unmarshal(body, &b)
	if b.AccountBalances == nil || len(b.AccountBalances) != 0 {
		t.Errorf("floating settle account_balances = %v, want []", b.AccountBalances)
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
