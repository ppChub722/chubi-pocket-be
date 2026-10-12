package projects

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
)

type walletBalances struct {
	AccountBalances []struct {
		AccountID string  `json:"account_id"`
		Balance   float64 `json:"balance"`
	} `json:"account_balances"`
	ID string `json:"id"`
}

func touchEngine(f *qcFixture) http.Handler {
	h := NewHandler(f.svc)
	return apitest.Engine(f.owner, func(g *gin.RouterGroup) {
		g.POST("/projects/quick", h.QuickCreate)
		g.POST("/projects/:id/bills", h.AddBills)
		g.GET("/projects/:id/transactions", h.ListPT)
	})
}

func TestQuickCreateAndAddBillsAccountBalances(t *testing.T) {
	f := newQCFixture(t)
	r := touchEngine(f)

	// A new bill moves money: the wallet's post-commit balance comes back.
	status, body := apitest.Do(t, r, "POST", "/api/v1/projects/quick", map[string]any{
		"name": "Trip",
		"new_transaction": map[string]any{
			"type": "expense", "account_id": f.acctPersonal, "amount": 150, "category_id": f.cat, "date": "2026-09-15",
		},
		"transaction_ids": []uuid.UUID{f.bill1},
	})
	if status != http.StatusOK {
		t.Fatalf("quick create: %d %s", status, body)
	}
	apitest.Conform(t, "quickCreateEvent", status, body)
	var q walletBalances
	_ = json.Unmarshal(body, &q)
	if len(q.AccountBalances) != 1 || q.AccountBalances[0].AccountID != f.acctPersonal.String() ||
		q.AccountBalances[0].Balance != f.accountBalance(t, f.acctPersonal) || q.AccountBalances[0].Balance != 850 {
		t.Errorf("quick create account_balances = %+v, want the wallet at 850", q.AccountBalances)
	}

	// Pulling only an existing bill moves no money.
	status, body = apitest.Do(t, r, "POST", "/api/v1/projects/quick", map[string]any{
		"name": "Second", "transaction_ids": []uuid.UUID{f.bill2},
	})
	if status != http.StatusOK {
		t.Fatalf("quick create (pull only): %d %s", status, body)
	}
	apitest.Conform(t, "quickCreateEvent", status, body)
	q = walletBalances{}
	_ = json.Unmarshal(body, &q)
	if q.AccountBalances == nil || len(q.AccountBalances) != 0 {
		t.Errorf("pull-only account_balances = %v, want []", q.AccountBalances)
	}

	// addBills with a new bill on the first event.
	status, body = apitest.Do(t, r, "POST", "/api/v1/projects/"+q0ID(t, f)+"/bills", map[string]any{
		"new_transaction": map[string]any{
			"type": "expense", "account_id": f.acctPersonal, "amount": 50, "category_id": f.cat, "date": "2026-09-16",
		},
	})
	if status != http.StatusOK {
		t.Fatalf("add bills: %d %s", status, body)
	}
	apitest.Conform(t, "addBillsToEvent", status, body)
	var a walletBalances
	_ = json.Unmarshal(body, &a)
	if len(a.AccountBalances) != 1 || a.AccountBalances[0].Balance != 800 {
		t.Errorf("add bills account_balances = %+v, want the wallet at 800", a.AccountBalances)
	}
}

// q0ID — the id of the owner's first project.
func q0ID(t *testing.T, f *qcFixture) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT id::text FROM projects WHERE owner_user_id = $1 ORDER BY created_at LIMIT 1`, f.owner).Scan(&id); err != nil {
		t.Fatalf("first project: %v", err)
	}
	return id
}

func TestBoardRowsCarryTransactionMember(t *testing.T) {
	f := newQCFixture(t)
	r := touchEngine(f)
	status, body := apitest.Do(t, r, "POST", "/api/v1/projects/quick", map[string]any{
		"name": "Trip", "transaction_ids": []uuid.UUID{f.bill1},
	})
	if status != http.StatusOK {
		t.Fatalf("quick create: %d %s", status, body)
	}
	var q walletBalances
	_ = json.Unmarshal(body, &q)

	// The linked friend renames themself: the board shows the live name.
	if _, err := f.pool.Exec(context.Background(), `UPDATE users SET display_name = 'Bee Live' WHERE id = $1`, f.friend); err != nil {
		t.Fatal(err)
	}
	status, body = apitest.Do(t, r, "GET", "/api/v1/projects/"+q.ID+"/transactions", nil)
	if status != http.StatusOK {
		t.Fatalf("board: %d %s", status, body)
	}
	apitest.Conform(t, "listBoardRows", status, body)
	var rows struct {
		Data []struct {
			ID                  string  `json:"id"`
			TransactionMemberID string  `json:"transaction_member_id"`
			Parent              *string `json:"parent_project_transaction_id"`
			TransactionMember   struct {
				ID     string  `json:"id"`
				UserID *string `json:"user_id"`
				Name   string  `json:"name"`
				IsMe   bool    `json:"is_me"`
			} `json:"transaction_member"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &rows)
	if len(rows.Data) < 3 {
		t.Fatalf("board rows = %d, want a parent and 2 split children: %s", len(rows.Data), body)
	}
	sawMe, sawLive, sawAdHoc := false, false, false
	for _, row := range rows.Data {
		m := row.TransactionMember
		if m.ID != row.TransactionMemberID {
			t.Errorf("transaction_member.id = %s, want %s", m.ID, row.TransactionMemberID)
		}
		switch {
		case m.UserID != nil && *m.UserID == f.owner.String():
			sawMe = sawMe || m.IsMe
			if !m.IsMe {
				t.Errorf("the caller's own member must have is_me")
			}
		case m.UserID != nil && *m.UserID == f.friend.String():
			sawLive = sawLive || m.Name == "Bee Live"
			if m.IsMe {
				t.Errorf("another user's member has is_me")
			}
		default:
			sawAdHoc = sawAdHoc || m.Name == "Grandma"
		}
	}
	if !sawMe || !sawLive || !sawAdHoc {
		t.Errorf("member refs: me=%v live-name=%v ad-hoc=%v in %s", sawMe, sawLive, sawAdHoc, body)
	}
}
