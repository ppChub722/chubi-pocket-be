package accounts

import (
	"net/http"
	"testing"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

// A wallet without a balance ("ติดตามยอดเงิน" off): no opening balance, left
// out of the totals, no adjust; turning it back on takes the balance it has
// now in the same call.
func TestWalletTrackBalance(t *testing.T) {
	e := newAccEnv(t)
	owner := testdb.User(t, e.pool)
	e.seedCategories(owner)
	r := e.as(owner)

	e.call(r, "createAccount", "POST", "/api/v1/accounts", map[string]any{"name": "Main", "type": "bank", "balance": 500}, http.StatusCreated)
	off := dec[Account](t, e.call(r, "createAccount", "POST", "/api/v1/accounts",
		map[string]any{"name": "Pocket", "type": "cash", "balance": 300, "track_balance": false}, http.StatusCreated))
	if off.TrackBalance || off.Balance != 0 {
		t.Fatalf("untracked wallet = %+v, want track_balance false, balance 0", off)
	}

	list := dec[AccountList](t, e.call(r, "listAccounts", "GET", "/api/v1/accounts", nil, http.StatusOK))
	if list.Totals.Assets != 500 || list.Totals.UntrackedCount != 1 || list.Total != 2 {
		t.Errorf("totals = %+v (total %d), want assets 500, 1 untracked, 2 listed", list.Totals, list.Total)
	}

	path := "/api/v1/accounts/" + off.ID.String()
	if code := e.errCode(r, "POST", path+"/adjust-balance", map[string]any{"new_balance": 50}, http.StatusConflict); code != "BALANCE_NOT_TRACKED" {
		t.Errorf("adjust untracked = %s", code)
	}
	if code := e.errCode(r, "PUT", path, map[string]any{"current_balance": 50}, http.StatusBadRequest); code != "VALIDATION_ERROR" {
		t.Errorf("current_balance alone = %s", code)
	}

	on := dec[Account](t, e.call(r, "updateAccount", "PUT", path, map[string]any{"track_balance": true, "current_balance": 80}, http.StatusOK))
	if !on.TrackBalance || on.Balance != 80 {
		t.Fatalf("turned on = %+v, want balance 80", on)
	}
	list = dec[AccountList](t, e.call(r, "listAccounts", "GET", "/api/v1/accounts", nil, http.StatusOK))
	if list.Totals.Assets != 580 || list.Totals.UntrackedCount != 0 {
		t.Errorf("totals after on = %+v", list.Totals)
	}
}
