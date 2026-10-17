package accounts

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
	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

type accEnv struct {
	t    *testing.T
	pool *pgxpool.Pool
	svc  *Service
	h    *Handler
}

func newAccEnv(t *testing.T) *accEnv {
	t.Helper()
	pool := testdb.Pool(t)
	cats := categories.NewService(categories.NewStore(pool))
	svc := NewService(NewStore(pool), transactions.NewService(transactions.NewStore(pool), cats), cats)
	svc.WithNotificationService(notifications.NewService(notifications.NewStore(pool)))
	return &accEnv{t: t, pool: pool, svc: svc, h: NewHandler(svc)}
}

// seedCategories gives uid the system categories (adjust-balance needs them).
func (e *accEnv) seedCategories(uid uuid.UUID) {
	e.t.Helper()
	ctx := context.Background()
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	cats := categories.NewService(categories.NewStore(e.pool))
	if err := cats.SeedForUser(ctx, tx, uid); err != nil {
		e.t.Fatalf("seed categories: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		e.t.Fatal(err)
	}
}

func (e *accEnv) as(uid uuid.UUID) *gin.Engine {
	h := e.h
	return apitest.Engine(uid, func(g *gin.RouterGroup) {
		g.GET("/accounts", h.List)
		g.POST("/accounts", h.Create)
		g.PATCH("/accounts/reorder", h.Reorder)
		g.POST("/accounts/invites/:notification_id/accept", h.AcceptInvite)
		g.POST("/accounts/invites/:notification_id/reject", h.RejectInvite)
		g.GET("/accounts/:id", h.Get)
		g.PUT("/accounts/:id", h.Update)
		g.DELETE("/accounts/:id", h.Delete)
		g.POST("/accounts/:id/archive", h.Archive)
		g.POST("/accounts/:id/restore", h.Restore)
		g.POST("/accounts/:id/adjust-balance", h.AdjustBalance)
		g.POST("/accounts/:id/identifiers", h.AddIdentifier)
		g.POST("/accounts/:id/members", h.InviteMember)
		g.DELETE("/accounts/:id/members/:member_id", h.RemoveMember)
		g.POST("/accounts/:id/transfer-ownership", h.TransferOwnership)
		g.PUT("/accounts/:id/report-scope", h.UpdateReportScope)
	})
}

type aJSON struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	Type          string    `json:"type"`
	Group         string    `json:"group"`
	Balance       float64   `json:"balance"`
	Currency      string    `json:"currency"`
	Status        string    `json:"status"`
	SortOrder     int       `json:"sort_order"`
	MyRole        string    `json:"my_role"`
	CanEdit       bool      `json:"can_edit"`
	IsShared      bool      `json:"is_shared"`
	MyReportScope string    `json:"my_report_scope"`
	Identifiers   []struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	} `json:"identifiers"`
	Members []struct {
		ID     uuid.UUID `json:"id"`
		UserID uuid.UUID `json:"user_id"`
		Name   string    `json:"name"`
		Role   string    `json:"role"`
		IsMe   bool      `json:"is_me"`
	} `json:"members"`
}

type mhJSON struct {
	ID     uuid.UUID `json:"id"`
	UserID uuid.UUID `json:"user_id"`
	Name   string    `json:"name"`
	Role   string    `json:"role"`
	Status string    `json:"status"`
	IsMe   bool      `json:"is_me"`
}

func dec[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return out
}

// call sends one request and conforms the answer to opID (skip = "" to not
// conform), failing unless the status is want.
func (e *accEnv) call(r *gin.Engine, opID, method, path string, body any, want int) []byte {
	e.t.Helper()
	status, raw := apitest.Do(e.t, r, method, path, body)
	if opID != "" {
		apitest.Conform(e.t, opID, status, raw)
	}
	if status != want {
		e.t.Fatalf("%s %s: status %d, want %d: %s", method, path, status, want, raw)
	}
	return raw
}

// errCode returns the error code of a failed call.
func (e *accEnv) errCode(r *gin.Engine, method, path string, body any, wantStatus int) string {
	e.t.Helper()
	status, raw := apitest.Do(e.t, r, method, path, body)
	if status != wantStatus {
		e.t.Errorf("%s %s: status %d, want %d: %s", method, path, status, wantStatus, raw)
	}
	return dec[struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}](e.t, raw).Error.Code
}

func (e *accEnv) email(uid uuid.UUID) string {
	e.t.Helper()
	var s string
	if err := e.pool.QueryRow(context.Background(), `SELECT email FROM users WHERE id = $1`, uid).Scan(&s); err != nil {
		e.t.Fatal(err)
	}
	return s
}

func (e *accEnv) inviteNotification(uid uuid.UUID) uuid.UUID {
	e.t.Helper()
	var id uuid.UUID
	if err := e.pool.QueryRow(context.Background(), `SELECT id FROM notifications WHERE recipient_user_id = $1
		AND type = 'account_invite' ORDER BY created_at DESC LIMIT 1`, uid).Scan(&id); err != nil {
		e.t.Fatalf("invite notification: %v", err)
	}
	return id
}

// TestAccountCRUD: create / list (totals, archived_count) / get (include) /
// update / identifiers / adjust-balance / archive / restore / reorder.
func TestAccountCRUD(t *testing.T) {
	e := newAccEnv(t)
	ctx := context.Background()
	owner := testdb.User(t, e.pool)
	stranger := testdb.User(t, e.pool)
	e.seedCategories(owner)
	r, rs := e.as(owner), e.as(stranger)

	// createAccount: the server sets the user's currency (body ignored).
	raw := e.call(r, "createAccount", "POST", "/api/v1/accounts",
		map[string]any{"name": "Main", "type": "bank", "balance": 500, "currency": "USD"}, http.StatusCreated)
	main := dec[aJSON](t, raw)
	if main.Currency != "THB" || main.Balance != 500 || main.Group != "asset" || main.MyRole != "owner" || !main.CanEdit ||
		main.IsShared || len(main.Members) != 1 || !main.Members[0].IsMe || main.Members[0].Role != "owner" {
		t.Errorf("created = %+v, want THB 500 asset, owner, one member (me)", main)
	}
	if code := e.errCode(r, "POST", "/api/v1/accounts", map[string]any{"name": "Card", "type": "credit_card"}, http.StatusBadRequest); code != "VALIDATION_ERROR" {
		t.Errorf("credit card without limit: code %q", code)
	}
	card := dec[aJSON](t, e.call(r, "createAccount", "POST", "/api/v1/accounts",
		map[string]any{"name": "Card", "type": "credit_card", "credit_limit": 5000, "balance": -1200}, http.StatusCreated))
	if card.Group != "liability" || card.Balance != -1200 {
		t.Errorf("card = %+v, want liability -1200", card)
	}
	cash := dec[aJSON](t, e.call(r, "createAccount", "POST", "/api/v1/accounts",
		map[string]any{"name": "Cash", "type": "cash"}, http.StatusCreated))
	if cash.SortOrder <= card.SortOrder || card.SortOrder <= main.SortOrder {
		t.Errorf("sort_order of new wallets should append: %d %d %d", main.SortOrder, card.SortOrder, cash.SortOrder)
	}
	// A legacy wallet in another currency is counted, not summed.
	usd := testdb.Account(t, e.pool, owner)
	if _, err := e.pool.Exec(ctx, `UPDATE accounts SET currency = 'USD', balance = 99, name = 'Dollars' WHERE id = $1`, usd); err != nil {
		t.Fatal(err)
	}
	old := testdb.Account(t, e.pool, owner)
	if _, err := e.pool.Exec(ctx, `UPDATE accounts SET name = 'Old' WHERE id = $1`, old); err != nil {
		t.Fatal(err)
	}

	// listAccounts.
	raw = e.call(r, "listAccounts", "GET", "/api/v1/accounts", nil, http.StatusOK)
	list := dec[struct {
		Data   []aJSON `json:"data"`
		Total  int     `json:"total"`
		Totals struct {
			Assets      float64 `json:"assets"`
			Liabilities float64 `json:"liabilities"`
			Net         float64 `json:"net"`
			Currency    string  `json:"currency"`
			CreditUsed  float64 `json:"credit_used"`
			CreditLimit float64 `json:"credit_limit"`
			SharedCount int     `json:"shared_count"`
			ByType      []struct {
				Type    string  `json:"type"`
				Count   int     `json:"count"`
				Balance float64 `json:"balance"`
			} `json:"by_type"`
		} `json:"totals"`
		ArchivedCount int `json:"archived_count"`
	}](t, raw)
	tt := list.Totals
	if list.Total != 5 || tt.Currency != "THB" || tt.Assets != 599 || tt.Liabilities != 1200 || tt.Net != -601 ||
		tt.CreditUsed != 1200 || tt.CreditLimit != 5000 || tt.SharedCount != 0 {
		t.Errorf("list = total %d totals %+v, want 5 / assets 599 (USD wallet counted as-is), liabilities 1200, net -601", list.Total, tt)
	}
	if len(tt.ByType) < 3 || tt.ByType[0].Type != "cash" {
		t.Errorf("by_type = %+v", tt.ByType)
	}
	if list.ArchivedCount != 0 {
		t.Errorf("archived_count = %d, want 0", list.ArchivedCount)
	}
	if code := e.errCode(r, "GET", "/api/v1/accounts?status=bogus", nil, http.StatusBadRequest); code != "VALIDATION_ERROR" {
		t.Errorf("bad status code %q", code)
	}

	// getAccount: include is opt-in; archived works.
	raw = e.call(r, "getAccount", "GET", "/api/v1/accounts/"+main.ID.String(), nil, http.StatusOK)
	plain := dec[struct {
		Summary        json.RawMessage `json:"summary"`
		MembersHistory json.RawMessage `json:"members_history"`
	}](t, raw)
	if string(plain.Summary) != "null" || string(plain.MembersHistory) != "null" {
		t.Errorf("without include: summary %s, members_history %s, want both null", plain.Summary, plain.MembersHistory)
	}
	raw = e.call(r, "getAccount", "GET", "/api/v1/accounts/"+main.ID.String()+"?include=summary,members&from=2000-01-01&to=2100-01-01", nil, http.StatusOK)
	full := dec[struct {
		Summary struct {
			From             string  `json:"from"`
			To               string  `json:"to"`
			TotalIncome      float64 `json:"total_income"`
			TransactionCount int     `json:"transaction_count"`
		} `json:"summary"`
		MembersHistory []mhJSON `json:"members_history"`
	}](t, raw)
	if full.Summary.From != "2000-01-01" || full.Summary.To != "2100-01-01" || len(full.MembersHistory) != 1 ||
		full.MembersHistory[0].Status != "active" || !full.MembersHistory[0].IsMe {
		t.Errorf("include = %+v", full)
	}
	raw = e.call(r, "getAccount", "GET", "/api/v1/accounts/"+main.ID.String()+"?include=summary", nil, http.StatusOK)
	def := dec[struct {
		Summary struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"summary"`
		MembersHistory json.RawMessage `json:"members_history"`
	}](t, raw)
	if len(def.Summary.From) != 10 || def.Summary.From[8:] != "01" || def.Summary.From[:7] != def.Summary.To[:7] || string(def.MembersHistory) != "null" {
		t.Errorf("default summary period = %+v, want the current month", def)
	}
	if code := e.errCode(r, "GET", "/api/v1/accounts/"+main.ID.String()+"?include=nope", nil, http.StatusBadRequest); code != "VALIDATION_ERROR" {
		t.Errorf("bad include code %q", code)
	}
	if code := e.errCode(r, "GET", "/api/v1/accounts/"+main.ID.String()+"?include=summary&from=yesterday", nil, http.StatusBadRequest); code != "VALIDATION_ERROR" {
		t.Errorf("bad date code %q", code)
	}
	if code := e.errCode(rs, "GET", "/api/v1/accounts/"+main.ID.String(), nil, http.StatusNotFound); code != "NOT_FOUND" {
		t.Errorf("non-member get code %q", code)
	}

	// updateAccount: no balance, currency and status are not editable.
	raw = e.call(r, "updateAccount", "PUT", "/api/v1/accounts/"+main.ID.String(),
		map[string]any{"name": "Main renamed", "currency": "USD", "status": "archived", "note": "n"}, http.StatusOK)
	upd := dec[aJSON](t, raw)
	if upd.Name != "Main renamed" || upd.Currency != "THB" || upd.Status != "active" {
		t.Errorf("update = %+v, want renamed, currency and status unchanged", upd)
	}
	if code := e.errCode(r, "PUT", "/api/v1/accounts/"+main.ID.String(), map[string]any{"balance": 1}, http.StatusBadRequest); code != "BALANCE_NOT_EDITABLE" {
		t.Errorf("balance code %q", code)
	}
	if code := e.errCode(r, "PUT", "/api/v1/accounts/"+main.ID.String(), map[string]any{"name": ""}, http.StatusBadRequest); code != "VALIDATION_ERROR" {
		t.Errorf("empty name code %q", code)
	}
	if code := e.errCode(r, "PUT", "/api/v1/accounts/"+uuid.NewString(), map[string]any{"name": "x"}, http.StatusNotFound); code != "NOT_FOUND" {
		t.Errorf("unknown code %q", code)
	}
	// sort_order in the body moves the caller's own position.
	upd = dec[aJSON](t, e.call(r, "updateAccount", "PUT", "/api/v1/accounts/"+cash.ID.String(), map[string]any{"sort_order": 0}, http.StatusOK))
	if upd.SortOrder != 0 {
		t.Errorf("sort_order after update = %d, want 0", upd.SortOrder)
	}

	// addAccountIdentifier.
	raw = e.call(r, "addAccountIdentifier", "POST", "/api/v1/accounts/"+main.ID.String()+"/identifiers",
		map[string]any{"kind": "bank_account", "value": "123-4-56789-0", "bank_code": "004"}, http.StatusOK)
	idAcc := dec[aJSON](t, raw)
	if len(idAcc.Identifiers) != 1 || idAcc.Identifiers[0].Value != "1234567890" {
		t.Errorf("identifiers = %+v, want one normalized", idAcc.Identifiers)
	}
	e.call(r, "addAccountIdentifier", "POST", "/api/v1/accounts/"+main.ID.String()+"/identifiers",
		map[string]any{"kind": "bank_account", "value": "1234567890", "bank_code": "004"}, http.StatusOK)
	if got := dec[aJSON](t, e.call(r, "", "GET", "/api/v1/accounts/"+main.ID.String(), nil, http.StatusOK)); len(got.Identifiers) != 1 {
		t.Errorf("an identical identifier was added twice: %+v", got.Identifiers)
	}
	if code := e.errCode(r, "POST", "/api/v1/accounts/"+main.ID.String()+"/identifiers", map[string]any{"kind": "iban", "value": "123456"}, http.StatusBadRequest); code != "INVALID_IDENTIFIER" {
		t.Errorf("invalid identifier code %q", code)
	}

	// adjustAccountBalance: conformed in full (the adjustment row is a Transaction).
	raw = e.call(r, "adjustAccountBalance", "POST", "/api/v1/accounts/"+main.ID.String()+"/adjust-balance", map[string]any{"new_balance": 450, "note": "count"}, http.StatusOK)
	adj := dec[struct {
		Account     aJSON `json:"account"`
		Transaction struct {
			ID uuid.UUID `json:"id"`
		} `json:"transaction"`
		AccountBalances []struct {
			AccountID uuid.UUID `json:"account_id"`
			Balance   float64   `json:"balance"`
		} `json:"account_balances"`
	}](t, raw)
	if adj.Account.ID != main.ID || adj.Account.Balance != 450 || adj.Transaction.ID == uuid.Nil ||
		len(adj.AccountBalances) != 1 || adj.AccountBalances[0].AccountID != main.ID || adj.AccountBalances[0].Balance != 450 {
		t.Errorf("adjust = %s, want account + adjustment row + balance 450", raw)
	}
	if code := e.errCode(r, "POST", "/api/v1/accounts/"+main.ID.String()+"/adjust-balance", map[string]any{"new_balance": 450}, http.StatusBadRequest); code != "NO_OP" {
		t.Errorf("same balance code %q", code)
	}
	if code := e.errCode(r, "POST", "/api/v1/accounts/"+main.ID.String()+"/adjust-balance", map[string]any{}, http.StatusBadRequest); code != "VALIDATION_ERROR" {
		t.Errorf("missing new_balance code %q", code)
	}
	if code := e.errCode(rs, "POST", "/api/v1/accounts/"+main.ID.String()+"/adjust-balance", map[string]any{"new_balance": 1}, http.StatusNotFound); code != "NOT_FOUND" {
		t.Errorf("non-member adjust code %q", code)
	}

	// archiveAccount / restoreAccount: owner only, state in the answer.
	if code := e.errCode(rs, "POST", "/api/v1/accounts/"+old.String()+"/archive", nil, http.StatusNotFound); code != "NOT_FOUND" {
		t.Errorf("stranger archive code %q", code)
	}
	arch := dec[aJSON](t, e.call(r, "archiveAccount", "POST", "/api/v1/accounts/"+old.String()+"/archive", nil, http.StatusOK))
	if arch.Status != "archived" || arch.ID != old {
		t.Errorf("archived = %+v", arch)
	}
	raw = e.call(r, "listAccounts", "GET", "/api/v1/accounts", nil, http.StatusOK)
	after := dec[struct {
		Total         int `json:"total"`
		ArchivedCount int `json:"archived_count"`
	}](t, raw)
	if after.Total != 4 || after.ArchivedCount != 1 {
		t.Errorf("after archive: total %d archived_count %d, want 4 / 1", after.Total, after.ArchivedCount)
	}
	raw = e.call(r, "listAccounts", "GET", "/api/v1/accounts?status=archived", nil, http.StatusOK)
	if got := dec[struct{ Data []aJSON }](t, raw).Data; len(got) != 1 || got[0].ID != old {
		t.Errorf("archived list = %+v", got)
	}
	e.call(r, "getAccount", "GET", "/api/v1/accounts/"+old.String(), nil, http.StatusOK)
	rest := dec[aJSON](t, e.call(r, "restoreAccount", "POST", "/api/v1/accounts/"+old.String()+"/restore", nil, http.StatusOK))
	if rest.Status != "active" {
		t.Errorf("restored = %+v", rest)
	}
	// POST /archive again is idempotent, then restore.
	if a := dec[aJSON](t, e.call(r, "archiveAccount", "POST", "/api/v1/accounts/"+old.String()+"/archive", nil, http.StatusOK)); a.Status != "archived" {
		t.Errorf("POST archive = %+v", a)
	}
	e.call(r, "restoreAccount", "POST", "/api/v1/accounts/"+old.String()+"/restore", nil, http.StatusOK)
	if code := e.errCode(rs, "POST", "/api/v1/accounts/"+old.String()+"/restore", nil, http.StatusNotFound); code != "NOT_FOUND" {
		t.Errorf("stranger restore code %q", code)
	}

	// reorderAccounts: the list comes back in the new order.
	raw = e.call(r, "reorderAccounts", "PATCH", "/api/v1/accounts/reorder", map[string]any{"items": []map[string]any{
		{"id": cash.ID, "sort_order": 2}, {"id": main.ID, "sort_order": 0}, {"id": card.ID, "sort_order": 1},
		{"id": usd, "sort_order": 3}, {"id": old, "sort_order": 4},
	}}, http.StatusOK)
	var order []string
	for _, a := range dec[struct{ Data []aJSON }](t, raw).Data {
		order = append(order, a.Name)
	}
	if len(order) != 5 || order[0] != "Main renamed" || order[1] != "Card" || order[2] != "Cash" {
		t.Errorf("reordered = %v, want Main renamed, Card, Cash, ...", order)
	}
	if code := e.errCode(r, "PATCH", "/api/v1/accounts/reorder", map[string]any{"items": []map[string]any{{"id": uuid.New(), "sort_order": 0}}}, http.StatusForbidden); code != "FORBIDDEN" {
		t.Errorf("reorder foreign wallet code %q", code)
	}
}

// TestAccountMembers: invite -> accept / reject -> report scope -> transfer
// -> remove / leave, every answer conformed, roles and owner-only rules.
func TestAccountMembers(t *testing.T) {
	e := newAccEnv(t)
	ctx := context.Background()
	owner, friend, third, outsider := testdb.User(t, e.pool), testdb.User(t, e.pool), testdb.User(t, e.pool), testdb.User(t, e.pool)
	for id, name := range map[uuid.UUID]string{owner: "Olive Owner", friend: "Fern Friend", third: "Theo Third"} {
		if _, err := e.pool.Exec(ctx, `UPDATE users SET display_name = $2 WHERE id = $1`, id, name); err != nil {
			t.Fatal(err)
		}
	}
	// A contact of the owner linked to friend: invite by contact works too.
	contact := uuid.New()
	if _, err := e.pool.Exec(ctx, `INSERT INTO contacts (id, user_id, display_name, linked_user_id) VALUES ($1, $2, 'F', $3)`, contact, owner, third); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(ctx, `DELETE FROM contacts WHERE user_id = $1`, owner)
		_, _ = e.pool.Exec(ctx, `DELETE FROM notifications WHERE recipient_user_id = ANY($1)`, []uuid.UUID{owner, friend, third, outsider})
	})
	ro, rf, rt, rx := e.as(owner), e.as(friend), e.as(third), e.as(outsider)

	wallet := dec[aJSON](t, e.call(ro, "createAccount", "POST", "/api/v1/accounts", map[string]any{"name": "Shared", "type": "bank"}, http.StatusCreated)).ID
	path := "/api/v1/accounts/" + wallet.String()

	// inviteAccountMember: owner only; pending row in the history.
	if code := e.errCode(rx, "POST", path+"/members", map[string]any{"email": e.email(friend)}, http.StatusForbidden); code != "NOT_MEMBER" {
		t.Errorf("outsider invite code %q", code)
	}
	if code := e.errCode(ro, "POST", path+"/members", map[string]any{}, http.StatusBadRequest); code != "VALIDATION_ERROR" {
		t.Errorf("neither contact nor email code %q", code)
	}
	if code := e.errCode(ro, "POST", path+"/members", map[string]any{"email": "nobody@test.local"}, http.StatusNotFound); code != "USER_NOT_FOUND" {
		t.Errorf("unknown email code %q", code)
	}
	raw := e.call(ro, "inviteAccountMember", "POST", path+"/members", map[string]any{"email": e.email(friend)}, http.StatusCreated)
	inv := dec[struct {
		Account aJSON    `json:"account"`
		Members []mhJSON `json:"members"`
	}](t, raw)
	if inv.Account.ID != wallet || inv.Account.IsShared || len(inv.Account.Members) != 1 || len(inv.Members) != 2 {
		t.Errorf("invite result = %s, want wallet (still one active member) + 2 history rows", raw)
	}
	if inv.Members[0].Role != "owner" || !inv.Members[0].IsMe || inv.Members[1].Status != "pending" || inv.Members[1].UserID != friend || inv.Members[1].Name != "Fern Friend" {
		t.Errorf("members = %+v, want me/owner first, friend pending", inv.Members)
	}
	if code := e.errCode(ro, "POST", path+"/members", map[string]any{"email": e.email(friend)}, http.StatusConflict); code != "USER_ALREADY_MEMBER" {
		t.Errorf("invite twice code %q", code)
	}

	// acceptAccountInvite: the friend joins; the wallet is shared now.
	notif := e.inviteNotification(friend)
	raw = e.call(rf, "acceptAccountInvite", "POST", "/api/v1/accounts/invites/"+notif.String()+"/accept", nil, http.StatusOK)
	acc := dec[struct {
		Account      aJSON `json:"account"`
		Notification struct {
			State string `json:"state"`
		} `json:"notification"`
	}](t, raw)
	if acc.Account.ID != wallet || !acc.Account.IsShared || len(acc.Account.Members) != 2 || acc.Account.MyRole != "member" ||
		acc.Account.CanEdit || acc.Notification.State != "actioned" || acc.Account.MyReportScope != "none" {
		t.Errorf("accept = %s", raw)
	}
	for _, m := range acc.Account.Members {
		if m.IsMe != (m.UserID == friend) {
			t.Errorf("is_me wrong for %+v (caller is the friend)", m)
		}
	}
	if code := e.errCode(rx, "POST", "/api/v1/accounts/invites/"+notif.String()+"/accept", nil, http.StatusNotFound); code != "NOT_FOUND" {
		t.Errorf("accept by someone else code %q", code)
	}

	// A member cannot invite, edit, archive or restore; a member sees it all.
	if code := e.errCode(rf, "POST", path+"/members", map[string]any{"email": e.email(third)}, http.StatusForbidden); code != "NOT_OWNER" {
		t.Errorf("member invite code %q", code)
	}
	if code := e.errCode(rf, "PUT", path, map[string]any{"name": "mine"}, http.StatusForbidden); code != "NOT_OWNER" {
		t.Errorf("member update code %q", code)
	}
	if code := e.errCode(rf, "DELETE", path, nil, http.StatusForbidden); code != "NOT_OWNER" {
		t.Errorf("member archive code %q", code)
	}
	if code := e.errCode(ro, "DELETE", path, nil, http.StatusConflict); code != "ACCOUNT_HAS_MEMBERS" {
		t.Errorf("archive with members code %q", code)
	}

	// rejectAccountInvite: by contact; the notification is dismissed.
	e.call(ro, "inviteAccountMember", "POST", path+"/members", map[string]any{"contact_id": contact}, http.StatusCreated)
	notif3 := e.inviteNotification(third)
	raw = e.call(rt, "rejectAccountInvite", "POST", "/api/v1/accounts/invites/"+notif3.String()+"/reject", nil, http.StatusOK)
	rej := dec[struct {
		Notification struct {
			State string `json:"state"`
		} `json:"notification"`
	}](t, raw)
	if rej.Notification.State != "dismissed" {
		t.Errorf("reject = %s, want dismissed", raw)
	}
	if code := e.errCode(rt, "GET", path, nil, http.StatusNotFound); code != "NOT_FOUND" {
		t.Errorf("rejected invitee sees the wallet: %q", code)
	}

	// updateAccountReportScope: the member's own scope, returned as Account.
	acct := dec[aJSON](t, e.call(rf, "updateAccountReportScope", "PUT", path+"/report-scope", map[string]any{"report_scope": "own"}, http.StatusOK))
	if acct.MyReportScope != "own" || acct.ID != wallet {
		t.Errorf("scope = %+v", acct)
	}
	if code := e.errCode(rx, "PUT", path+"/report-scope", map[string]any{"report_scope": "own"}, http.StatusForbidden); code != "NOT_MEMBER" {
		t.Errorf("outsider scope code %q", code)
	}
	if code := e.errCode(rf, "PUT", path+"/report-scope", map[string]any{"report_scope": "bogus"}, http.StatusBadRequest); code != "VALIDATION_ERROR" {
		t.Errorf("bad scope code %q", code)
	}

	// transferAccountOwnership: owner only; the new owner must be a member.
	if code := e.errCode(rf, "POST", path+"/transfer-ownership", map[string]any{"new_owner_user_id": friend}, http.StatusForbidden); code != "NOT_OWNER" {
		t.Errorf("member transfer code %q", code)
	}
	if code := e.errCode(ro, "POST", path+"/transfer-ownership", map[string]any{"new_owner_user_id": outsider}, http.StatusNotFound); code != "NOT_FOUND" {
		t.Errorf("transfer to non-member code %q", code)
	}
	if code := e.errCode(ro, "POST", path+"/transfer-ownership", map[string]any{}, http.StatusBadRequest); code != "VALIDATION_ERROR" {
		t.Errorf("transfer without target code %q", code)
	}
	raw = e.call(ro, "transferAccountOwnership", "POST", path+"/transfer-ownership", map[string]any{"new_owner_user_id": friend}, http.StatusOK)
	tr := dec[struct {
		Account aJSON    `json:"account"`
		Members []mhJSON `json:"members"`
	}](t, raw)
	if tr.Account.MyRole != "member" || tr.Account.CanEdit || len(tr.Members) != 2 || tr.Members[0].UserID != friend || tr.Members[0].Role != "owner" {
		t.Errorf("transfer = %s, want me demoted, friend first as owner", raw)
	}

	// removeAccountMember: the owner can't leave; others are removed by the
	// owner; leaving yourself answers {account: null, members: []}.
	friendRow := tr.Members[0].ID
	ownerRow := tr.Members[1].ID
	if code := e.errCode(rf, "DELETE", path+"/members/"+friendRow.String(), nil, http.StatusBadRequest); code != "OWNER_MUST_TRANSFER" {
		t.Errorf("owner leaving code %q", code)
	}
	e.call(rf, "inviteAccountMember", "POST", path+"/members", map[string]any{"contact_id": nil, "email": e.email(third)}, http.StatusCreated)
	notifT := e.inviteNotification(third)
	e.call(rt, "acceptAccountInvite", "POST", "/api/v1/accounts/invites/"+notifT.String()+"/accept", nil, http.StatusOK)
	var thirdRow uuid.UUID
	for _, m := range dec[struct {
		Members []mhJSON `json:"members"`
	}](t, e.call(rf, "", "POST", path+"/transfer-ownership", map[string]any{"new_owner_user_id": friend}, http.StatusOK)).Members {
		if m.UserID == third {
			thirdRow = m.ID
		}
	}
	if code := e.errCode(rt, "DELETE", path+"/members/"+ownerRow.String(), nil, http.StatusForbidden); code != "NOT_OWNER" {
		t.Errorf("member removing someone else code %q", code)
	}
	raw = e.call(rf, "removeAccountMember", "DELETE", path+"/members/"+thirdRow.String(), nil, http.StatusOK)
	rem := dec[struct {
		Account *aJSON   `json:"account"`
		Members []mhJSON `json:"members"`
	}](t, raw)
	left := 0
	for _, m := range rem.Members {
		if m.UserID == third && m.Status == "left" {
			left++
		}
	}
	if rem.Account == nil || rem.Account.ID != wallet || len(rem.Account.Members) != 2 || left != 1 {
		t.Errorf("remove = %s, want the wallet and third marked left", raw)
	}
	// The original owner (now a member) leaves.
	raw = e.call(ro, "removeAccountMember", "DELETE", path+"/members/"+ownerRow.String(), nil, http.StatusOK)
	if got := dec[struct {
		Account json.RawMessage `json:"account"`
		Members []mhJSON        `json:"members"`
	}](t, raw); string(got.Account) != "null" || got.Members == nil || len(got.Members) != 0 {
		t.Errorf("leave = %s, want {account: null, members: []}", raw)
	}
	if code := e.errCode(ro, "GET", path, nil, http.StatusNotFound); code != "NOT_FOUND" {
		t.Errorf("ex-member still sees the wallet: %q", code)
	}
	if code := e.errCode(rf, "DELETE", path+"/members/"+uuid.NewString(), nil, http.StatusNotFound); code != "NOT_FOUND" {
		t.Errorf("unknown member code %q", code)
	}
	// Now the friend is the only active member again: the delete works.
	if out := dec[struct {
		ID uuid.UUID `json:"id"`
	}](t, e.call(rf, "deleteAccount", "DELETE", path, nil, http.StatusOK)); out.ID.String() != strings.TrimPrefix(path, "/api/v1/accounts/") {
		t.Errorf("delete = %+v", out)
	}
	if code := e.errCode(rf, "GET", path, nil, http.StatusNotFound); code != "NOT_FOUND" {
		t.Errorf("deleted wallet still readable: %q", code)
	}
}

// TestAccountHardDelete — DELETE is real: the opening row goes, the other
// rows (and the transfer twin) stay with no wallet, goal money is kept with
// no wallet, the preview matches what the delete did.
func TestAccountHardDelete(t *testing.T) {
	e := newAccEnv(t)
	ctx := context.Background()
	owner := testdb.User(t, e.pool)
	e.seedCategories(owner)
	r := e.as(owner)
	txs := transactions.NewService(transactions.NewStore(e.pool), categories.NewService(categories.NewStore(e.pool)))

	mk := func(name string, balance float64) uuid.UUID {
		return dec[aJSON](t, e.call(r, "createAccount", "POST", "/api/v1/accounts",
			map[string]any{"name": name, "type": "bank", "balance": balance}, http.StatusCreated)).ID
	}
	doomed, keep := mk("Doomed", 500), mk("Keep", 500)
	if _, err := txs.Create(ctx, owner, transactions.CreateRequest{
		Type: transactions.TypeExpense, AccountID: &doomed, Amount: 100, Date: "2026-10-05"}); err != nil {
		t.Fatal(err)
	}
	res, err := txs.Create(ctx, owner, transactions.CreateRequest{
		Type: transactions.TypeTransfer, AccountID: &doomed, TransferToAccountID: &keep, Amount: 50, Date: "2026-10-06"})
	if err != nil {
		t.Fatal(err)
	}
	group := res.(*transactions.TransferResponse).TransferGroupID
	goal := uuid.New()
	for _, q := range []string{
		`INSERT INTO saving_goals (id, user_id, name, target_amount) VALUES ($1, $2, 'Japan', 1000)`,
	} {
		if _, err := e.pool.Exec(ctx, q, goal, owner); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO saving_entries (id, user_id, group_id, kind, account_id, from_bucket, to_bucket, to_goal_id, amount, date)
		VALUES ($1, $2, $1, 'save', $3, 'available', 'goal', $4, 70, '2026-10-07')`, uuid.New(), owner, doomed, goal); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE user_notification_settings SET default_account_id = $2 WHERE user_id = $1`, owner, doomed); err != nil {
		t.Fatal(err)
	}
	var recurringID uuid.UUID = uuid.New()
	if _, err := e.pool.Exec(ctx, `INSERT INTO recurring_rules (id, user_id, name, type, amount, account_id, freq, start_date)
		VALUES ($1, $2, 'Rent', 'expense', 10, $3, 'monthly', '2026-10-01')`, recurringID, owner, doomed); err != nil {
		t.Fatal(err)
	}

	var detail struct {
		DeletePreview *struct {
			Transactions      int `json:"transactions"`
			Transfers         int `json:"transfers"`
			SystemRowsDeleted int `json:"system_rows_deleted"`
			Recurring         int `json:"recurring"`
			SavingEntries     struct {
				Count  int     `json:"count"`
				Amount float64 `json:"amount"`
			} `json:"saving_entries"`
		} `json:"delete_preview"`
	}
	raw := e.call(r, "getAccount", "GET", "/api/v1/accounts/"+doomed.String()+"?include=delete_preview", nil, http.StatusOK)
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatal(err)
	}
	got := detail.DeletePreview
	if got == nil || got.Transactions != 1 || got.Transfers != 1 || got.SystemRowsDeleted != 1 || got.Recurring != 1 ||
		got.SavingEntries.Count != 1 || got.SavingEntries.Amount != 70 {
		t.Errorf("delete_preview = %s", raw)
	}

	e.call(r, "deleteAccount", "DELETE", "/api/v1/accounts/"+doomed.String(), nil, http.StatusOK)
	count := func(q string, args ...any) int {
		var n int
		if err := e.pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT COUNT(*) FROM accounts WHERE id = $1`, doomed); n != 0 {
		t.Errorf("wallet still there")
	}
	if n := count(`SELECT COUNT(*) FROM transactions WHERE user_id = $1 AND account_id IS NULL AND type = 'expense'`, owner); n != 1 {
		t.Errorf("wallet-less expense rows = %d, want 1", n)
	}
	if n := count(`SELECT COUNT(*) FROM transactions WHERE transfer_group_id = $1`, group); n != 2 {
		t.Errorf("transfer rows = %d, want both legs kept", n)
	}
	if n := count(`SELECT COUNT(*) FROM transactions WHERE transfer_group_id = $1 AND account_id IS NULL`, group); n != 1 {
		t.Errorf("wallet-less transfer legs = %d, want 1", n)
	}
	if n := count(`SELECT COUNT(*) FROM transactions t JOIN categories c ON c.id = t.category_id
		WHERE t.user_id = $1 AND c.system_kind IN ('OPENING_IN', 'OPENING_OUT') AND t.account_id IS NULL`, owner); n != 0 {
		t.Errorf("opening row of the deleted wallet survived")
	}
	if n := count(`SELECT COUNT(*) FROM saving_entries WHERE user_id = $1 AND account_id IS NULL AND amount = 70`, owner); n != 1 {
		t.Errorf("goal money not kept without a wallet")
	}
	if n := count(`SELECT COUNT(*) FROM recurring_rules WHERE id = $1 AND account_id IS NULL`, recurringID); n != 1 {
		t.Errorf("recurring rule not unlinked")
	}
	var bal float64
	if err := e.pool.QueryRow(ctx, `SELECT balance FROM accounts WHERE id = $1`, keep).Scan(&bal); err != nil || bal != 550 {
		t.Errorf("twin wallet balance = %v (%v), want 550 unchanged", bal, err)
	}
	e.call(r, "", "DELETE", "/api/v1/accounts/"+doomed.String(), nil, http.StatusNotFound)
}

// TestAccountPageNumbers — the wallets page numbers (0.4.0): this month's
// in / out (transfers and the opening row don't count), a card's next due
// date, and the summary totals.
func TestAccountPageNumbers(t *testing.T) {
	e := newAccEnv(t)
	ctx := context.Background()
	owner := testdb.User(t, e.pool)
	e.seedCategories(owner)
	r := e.as(owner)
	txs := transactions.NewService(transactions.NewStore(e.pool), categories.NewService(categories.NewStore(e.pool)))

	bank := dec[aJSON](t, e.call(r, "createAccount", "POST", "/api/v1/accounts",
		map[string]any{"name": "Bank", "type": "bank", "balance": 1000}, http.StatusCreated)).ID
	card := dec[aJSON](t, e.call(r, "createAccount", "POST", "/api/v1/accounts",
		map[string]any{"name": "Card", "type": "credit_card", "credit_limit": 10000, "balance": -2500,
			"statement_date": 5, "payment_due_date": 20}, http.StatusCreated)).ID
	today := time.Now().UTC().Format("2006-01-02")
	mk := func(typ transactions.TxType, acc uuid.UUID, amount float64) {
		if _, err := txs.Create(ctx, owner, transactions.CreateRequest{Type: typ, AccountID: &acc, Amount: amount, Date: today}); err != nil {
			t.Fatal(err)
		}
	}
	mk(transactions.TypeIncome, bank, 400)
	mk(transactions.TypeExpense, bank, 150)
	mk(transactions.TypeExpense, bank, 50)
	if _, err := txs.Create(ctx, owner, transactions.CreateRequest{
		Type: transactions.TypeTransfer, AccountID: &bank, TransferToAccountID: &card, Amount: 100, Date: today}); err != nil {
		t.Fatal(err)
	}

	var list struct {
		Data []struct {
			ID              uuid.UUID `json:"id"`
			MonthIn         float64   `json:"month_in"`
			MonthOut        float64   `json:"month_out"`
			DueDateNext     *string   `json:"due_date_next"`
			DaysUntilDue    *int      `json:"days_until_due"`
			CreditUsedPct   *int      `json:"credit_used_pct"`
			CreditAvailable *float64  `json:"credit_available"`
		} `json:"data"`
		Totals struct {
			Total, CashTotal, CardDebt float64
			CardUsedPct                int     `json:"card_used_pct"`
			SavedTotal                 float64 `json:"saved_total"`
			AvailableTotal             float64 `json:"available_total"`
		} `json:"totals"`
	}
	raw := e.call(r, "listAccounts", "GET", "/api/v1/accounts", nil, http.StatusOK)
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	var tot struct {
		Totals map[string]any `json:"totals"`
	}
	_ = json.Unmarshal(raw, &tot)
	for _, a := range list.Data {
		switch a.ID {
		case bank:
			if a.MonthIn != 400 || a.MonthOut != 200 || a.DueDateNext != nil {
				t.Errorf("bank = %+v, want in 400 out 200 (the transfer is neither), no due date", a)
			}
		case card:
			if a.CreditUsedPct == nil || *a.CreditUsedPct != 24 || a.CreditAvailable == nil || *a.CreditAvailable != 7600 || a.MonthIn != 0 || a.MonthOut != 0 || a.DueDateNext == nil || a.DaysUntilDue == nil || *a.DaysUntilDue < 0 || *a.DaysUntilDue > 31 {
				t.Errorf("card = %+v", a)
			}
		}
	}
	// bank 1000 + 400 − 200 − 100 = 1100; card −2500 + 100 = −2400.
	tt := tot.Totals
	fv := func(k string) float64 { v, _ := tt[k].(float64); return v }
	split, _ := tt["cash_split"].([]any)
	if fv("cash_total") != 1100 || fv("card_debt") != 2400 || fv("total") != -1300 || fv("card_used_pct") != 24 ||
		fv("saved_total") != 0 || fv("available_total") != 1100 || fv("card_remaining") != 7600 ||
		len(split) != 1 || split[0].(map[string]any)["type"] != "bank" || split[0].(map[string]any)["pct"] != float64(100) {
		t.Errorf("totals = %v", tt)
	}
}
