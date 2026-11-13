package accounts

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

func TestSavingsWalletSwitch(t *testing.T) {
	e := newAccEnv(t)
	ctx := context.Background()
	owner := testdb.User(t, e.pool)
	friend := testdb.User(t, e.pool)
	e.seedCategories(owner)
	r := e.as(owner)

	wallet := dec[Account](t, e.call(r, "createAccount", "POST", "/api/v1/accounts",
		map[string]any{"name": "Savings", "type": "bank", "balance": 10000}, http.StatusCreated))
	path := "/api/v1/accounts/" + wallet.ID.String()
	if wallet.AutoSave {
		t.Fatalf("a new wallet is not a savings wallet: %+v", wallet)
	}

	// 1,500 of booked unallocated saving (2,000 in, 500 out) in that wallet.
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	entry := func(kind, from, to string, amount float64) {
		exec(`INSERT INTO saving_entries (id, user_id, group_id, kind, account_id, from_bucket, to_bucket, amount, date)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, CURRENT_DATE)`,
			uuid.New(), owner, uuid.New(), kind, wallet.ID, from, to, amount)
	}
	entry("save", "available", "spare", 2000)
	entry("withdraw", "spare", "available", 500)
	booked := func() float64 {
		t.Helper()
		var n float64
		if err := e.pool.QueryRow(ctx, `SELECT COALESCE(SUM(CASE WHEN to_bucket = 'spare' THEN amount
			WHEN from_bucket = 'spare' THEN -amount ELSE 0 END), 0)::float8 FROM saving_entries WHERE user_id = $1 AND account_id = $2`,
			owner, wallet.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := dec[Account](t, e.call(r, "getAccount", "GET", path, nil, http.StatusOK)); got.Savings.Saved != 1500 {
		t.Fatalf("before = %+v", got.Savings)
	}

	// On: the booked unallocated saving is cleared (a withdrawal entry), the whole balance is saved.
	on := dec[Account](t, e.call(r, "updateAccount", "PUT", path, map[string]any{"auto_save": true}, http.StatusOK))
	if !on.AutoSave || on.Savings.Saved != 10000 || on.Savings.Spare != 10000 || on.Savings.Available == nil || *on.Savings.Available != 0 {
		t.Fatalf("on = auto_save %v, savings %+v", on.AutoSave, on.Savings)
	}
	if n := booked(); n != 0 {
		t.Errorf("booked spare after turning on = %v, want cleared", n)
	}

	// Off: the entries stay as they are (none unallocated now), the money is available again.
	off := dec[Account](t, e.call(r, "updateAccount", "PUT", path, map[string]any{"auto_save": false}, http.StatusOK))
	if off.AutoSave || off.Savings.Saved != 0 || off.Savings.Available == nil || *off.Savings.Available != 10000 {
		t.Fatalf("off = auto_save %v, savings %+v", off.AutoSave, off.Savings)
	}

	// It can't be a savings wallet while it is untracked, a card, or shared.
	card := dec[Account](t, e.call(r, "createAccount", "POST", "/api/v1/accounts",
		map[string]any{"name": "Card", "type": "credit_card", "credit_limit": 50000}, http.StatusCreated))
	if code := e.errCode(r, "PUT", "/api/v1/accounts/"+card.ID.String(), map[string]any{"auto_save": true}, http.StatusBadRequest); code != "VALIDATION_ERROR" {
		t.Errorf("card as savings wallet = %s", code)
	}
	pocket := dec[Account](t, e.call(r, "createAccount", "POST", "/api/v1/accounts",
		map[string]any{"name": "Pocket", "type": "cash", "track_balance": false}, http.StatusCreated))
	if code := e.errCode(r, "PUT", "/api/v1/accounts/"+pocket.ID.String(), map[string]any{"auto_save": true}, http.StatusBadRequest); code != "VALIDATION_ERROR" {
		t.Errorf("untracked as savings wallet = %s", code)
	}

	exec(`INSERT INTO account_members (id, account_id, user_id, role, report_scope, joined_at)
		VALUES ($1, $2, $3, 'member', 'none', now())`, uuid.New(), wallet.ID, friend)
	if code := e.errCode(r, "PUT", path, map[string]any{"auto_save": true}, http.StatusBadRequest); code != "VALIDATION_ERROR" {
		t.Errorf("shared wallet as savings wallet = %s", code)
	}
	exec(`DELETE FROM account_members WHERE account_id = $1 AND user_id = $2`, wallet.ID, friend)

	// While on: not untrackable, not convertible to a card, and nobody can be invited.
	e.call(r, "updateAccount", "PUT", path, map[string]any{"auto_save": true}, http.StatusOK)
	if code := e.errCode(r, "PUT", path, map[string]any{"track_balance": false}, http.StatusBadRequest); code != "VALIDATION_ERROR" {
		t.Errorf("untrack a savings wallet = %s", code)
	}
	if code := e.errCode(r, "PUT", path, map[string]any{"type": "credit_card", "credit_limit": 1000}, http.StatusBadRequest); code != "VALIDATION_ERROR" {
		t.Errorf("turn a savings wallet into a card = %s", code)
	}
	svc := NewService(NewStore(e.pool), nil, nil)
	svc.WithNotificationService(notifications.NewService(notifications.NewStore(e.pool)))
	email := e.email(friend)
	if _, err := svc.InviteMember(ctx, owner, wallet.ID, InviteMemberRequest{Email: &email}); !errors.Is(err, ErrSharedAutoSave) {
		t.Errorf("invite to a savings wallet: err = %v, want ErrSharedAutoSave", err)
	}
}
