package projects

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

func eventsEngine(f *qcFixture, as uuid.UUID) http.Handler {
	h := NewHandler(f.svc)
	return apitest.CountedEngine(as, func(g *gin.RouterGroup) {
		g.POST("/projects", h.Create)
		g.GET("/projects", h.List)
		legacyRoutes(g, f.svc) // V16: retired routes, test setup only
		g.POST("/projects/link-requests/:notification_id/accept", h.AcceptLinkRequest)
		g.POST("/projects/link-requests/:notification_id/reject", h.RejectLinkRequest)
		g.GET("/projects/:id", h.Get)
		g.GET("/projects/:id/detail", h.Detail)
		g.PUT("/projects/:id", h.Update)
		g.DELETE("/projects/:id", h.Delete)
		g.POST("/projects/:id/archive", h.Archive)
		g.POST("/projects/:id/restore", h.Restore)
		g.POST("/projects/:id/complete", h.Complete)
		g.GET("/projects/:id/members", h.ListMembers)
		g.POST("/projects/:id/members", h.AddMember)
		g.PUT("/projects/:id/members/:member_id", h.UpdateMember)
		g.DELETE("/projects/:id/members/:member_id", h.RemoveMember)
		g.POST("/projects/:id/members/:member_id/assign", h.AssignRemoved)
		g.POST("/projects/:id/members/:member_id/request-link", h.RequestLink)
		g.POST("/projects/:id/transfer-ownership", h.TransferOwnership)
		g.POST("/projects/:id/leave", h.Leave)
		g.GET("/projects/:id/transactions", h.ListPT)
		g.POST("/projects/:id/project-transactions", h.CreatePT)
		g.PUT("/projects/:id/project-transactions/:pt_id", h.UpdatePT)
		g.DELETE("/projects/:id/project-transactions/:pt_id", h.DeletePT)
		g.PUT("/projects/:id/project-transactions/:pt_id/mark", h.ToggleMark)
		g.GET("/projects/:id/summary", h.Summary)
		g.GET("/projects/:id/settle", h.Settle)
		g.POST("/projects/:id/people", h.ResolvePeople)
		g.GET("/projects/:id/tags", h.ListTags)
		g.POST("/projects/:id/tags", h.CreateTag)
		g.POST("/projects/:id/tags/import", h.ImportTags)
		g.PUT("/projects/:id/tags/:tag_id", h.UpdateTag)
		g.DELETE("/projects/:id/tags/:tag_id", h.DeleteTag)
	})
}

type evClient struct {
	t *testing.T
	r http.Handler
}

func (c evClient) do(method, path string, body any, op string, want int) map[string]any {
	c.t.Helper()
	status, raw := apitest.Do(c.t, c.r, method, "/api/v1"+path, body)
	if status != want {
		c.t.Fatalf("%s %s: status %d, want %d; %s", method, path, status, want, raw)
	}
	if op != "" {
		apitest.Conform(c.t, op, status, raw)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func (c evClient) code(method, path string, body any, want int, code string) {
	c.t.Helper()
	status, raw := apitest.Do(c.t, c.r, method, "/api/v1"+path, body)
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	if status != want || e.Error.Code != code {
		c.t.Errorf("%s %s: %d %s, want %d %s (%s)", method, path, status, e.Error.Code, want, code, raw)
	}
}

func m(v any, keys ...string) map[string]any {
	cur, _ := v.(map[string]any)
	for _, k := range keys {
		cur, _ = cur[k].(map[string]any)
	}
	return cur
}

func n(v map[string]any, key string) float64 {
	f, _ := v[key].(float64)
	return f
}

func list(v map[string]any, key string) []any {
	l, _ := v[key].([]any)
	return l
}

func (f *qcFixture) personalTag(t *testing.T, userID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := qcMustV7(t)
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO tags (id, user_id, name) VALUES ($1, $2, $3)`, id, userID, name); err != nil {
		t.Fatalf("tag: %v", err)
	}
	return id
}

func TestEventsAPI(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()
	me := evClient{t, eventsEngine(f, f.owner)}
	bee := evClient{t, eventsEngine(f, f.friend)}
	trip := f.personalTag(t, f.owner, "Trip")
	taxi := f.personalTag(t, f.owner, "Taxi")

	me.code("POST", "/projects", map[string]any{"name": "Bad", "start_date": "2026-10-10", "end_date": "2026-10-01"},
		http.StatusBadRequest, "INVALID_DATE_RANGE")
	created := me.do("POST", "/projects", map[string]any{
		"name": "Japan", "planned_amount": 1000, "start_date": "2026-10-01", "end_date": "2026-10-10",
		"tag_ids": []uuid.UUID{trip},
	}, "createProject", http.StatusCreated)
	ev := m(created, "event")
	id := ev["id"].(string)
	if tags := list(ev, "tags"); len(tags) != 1 || n(ev, "planned_amount") != 1000 || ev["is_owner"] != true || ev["can_add_bill"] != true {
		t.Fatalf("created event = %v", ev)
	}
	if got := me.do("GET", "/projects?tag_id="+trip.String(), nil, "listProjects", http.StatusOK); len(list(got, "data")) != 1 {
		t.Errorf("list by my tag = %v", got["data"])
	}
	if got := me.do("GET", "/projects?q=nomatch&sort=name_asc", nil, "listProjects", http.StatusOK); len(list(got, "data")) != 0 {
		t.Errorf("q=nomatch = %v", got["data"])
	}
	me.do("GET", "/projects/"+id, nil, "getProject", http.StatusOK)

	me.code("POST", "/projects/"+id+"/members", map[string]any{"role": "viewer"}, http.StatusBadRequest, "VALIDATION_ERROR")
	added := me.do("POST", "/projects/"+id+"/members", map[string]any{"ad_hoc": true, "display_name": "Grandma"},
		"addProjectMember", http.StatusCreated)
	var gran string
	for _, x := range list(added, "members") {
		if x.(map[string]any)["name"] == "Grandma" {
			gran = x.(map[string]any)["id"].(string)
		}
	}
	myMember := ev["my_member_id"].(string)

	food := m(me.do("POST", "/projects/"+id+"/tags", map[string]any{"name": "Food"}, "createProjectTag", http.StatusCreated), "tag")
	me.code("POST", "/projects/"+id+"/tags", map[string]any{"name": " food "}, http.StatusConflict, "TAG_NAME_TAKEN")
	me.do("PUT", "/projects/"+id+"/tags/"+food["id"].(string), map[string]any{"icon_code": map[string]any{"icon": "ramen", "iconColors": []string{"#FFFFFF"}, "background": "solid", "bgColors": []string{"#138F2D"}, "border": "none", "borderColors": []string{}, "shape": "circle"}},
		"updateProjectTag", http.StatusOK)

	bill := me.do("POST", "/projects/"+id+"/project-transactions", map[string]any{
		"transaction_member_id": myMember, "type": "expense", "amount": 300, "date": "2026-10-02",
		"tag_ids": []string{food["id"].(string)}, "splits": []map[string]any{{"member_id": gran, "amount": 100}},
	}, "createProjectTransaction", http.StatusCreated)
	row := m(bill, "row")
	ptID := row["id"].(string)
	if n(row, "my_share") != 200 || len(list(row, "tags")) != 1 || len(list(row, "children")) != 1 || row["linked"] != false {
		t.Errorf("bill row = %v", row)
	}
	if pos := m(bill, "summary", "my_position"); n(pos, "net") != 100 || n(pos, "share") != 200 {
		t.Errorf("my position = %v (paid 300, share 200, net 100)", pos)
	}
	if got := me.do("GET", "/projects", nil, "listProjects", http.StatusOK); true {
		e0 := list(got, "data")[0].(map[string]any)
		if n(e0, "my_paid") != 300 || n(e0, "my_share") != 200 || n(e0, "my_receivable") != 100 || n(e0, "my_payable") != 0 || n(e0, "remaining") != 700 {
			t.Errorf("event numbers = %v", e0)
		}
		sm := m(got, "summary")
		cn := m(sm, "counts")
		if n(sm, "my_share_total") != 200 || n(sm, "paid_total") != 300 || n(sm, "receivable_total") != 100 || n(sm, "payable_total") != 0 ||
			n(cn, "all") != 1 || n(cn, "active") != 1 || n(cn, "unsettled") != 1 || n(cn, "receivable") != 1 || n(cn, "payable") != 0 || n(cn, "ended") != 0 {
			t.Errorf("list summary = %v", sm)
		}
	}
	if got := me.do("GET", "/projects?status=current", nil, "listProjects", http.StatusOK); len(list(got, "data")) != 1 {
		t.Errorf("status=current = %v", got["data"])
	}
	if got := me.do("GET", "/projects?status=payable", nil, "listProjects", http.StatusOK); len(list(got, "data")) != 0 {
		t.Errorf("status=payable = %v", got["data"])
	}
	if pos := m(bill, "summary", "my_position"); n(pos, "receivable") != 100 || n(pos, "payable") != 0 {
		t.Errorf("my position split = %v", pos)
	}
	byTag := list(m(bill, "summary"), "by_tag")
	if len(byTag) != 1 || n(byTag[0].(map[string]any), "share") != 200 || m(byTag[0], "tag")["name"] != "Food" {
		t.Errorf("by_tag = %v", byTag)
	}

	// A payback (0.4.0-3): Grandma pays me back her 100 → even; it stays out of the bill totals.
	pts := "/projects/" + id + "/project-transactions"
	me.code("POST", pts, map[string]any{"transaction_member_id": gran, "type": "payback", "amount": 100, "date": "2026-10-03",
		"splits": []map[string]any{{"member_id": myMember, "amount": 50}}}, http.StatusBadRequest, "BAD_PAYBACK")
	back := me.do("POST", pts, map[string]any{"transaction_member_id": gran, "type": "payback", "amount": 100, "date": "2026-10-03",
		"splits": []map[string]any{{"member_id": myMember, "amount": 100}}}, "createProjectTransaction", http.StatusCreated)
	backID := m(back, "row")["id"].(string)
	if sm := m(back, "summary"); n(m(sm, "my_position"), "net") != 0 || n(m(sm, "my_position"), "received") != 100 ||
		n(sm, "total_expense") != 300 || n(sm, "transaction_count") != 1 {
		t.Errorf("after payback = %v", sm)
	}
	if st := me.do("GET", "/projects/"+id+"/settle", nil, "getProjectSettle", http.StatusOK); len(list(st, "pairs")) != 0 || len(list(st, "simplified")) != 0 {
		t.Errorf("settle after payback = %v", st)
	}
	me.code("POST", pts+"/"+backID+"/copy", nil, http.StatusUnprocessableEntity, "PAYBACK_NOT_COPYABLE")
	me.code("PUT", pts+"/"+ptID, map[string]any{"transaction_member_id": gran}, http.StatusBadRequest, "PAYER_FIXED")
	me.do("DELETE", pts+"/"+backID, nil, "deleteProjectTransaction", http.StatusOK)
	if st := me.do("GET", "/projects/"+id+"/settle", nil, "getProjectSettle", http.StatusOK); true {
		pairs := list(st, "pairs")
		if len(pairs) != 1 || n(pairs[0].(map[string]any), "amount") != 100 || m(pairs[0], "from")["name"] != "Grandma" {
			t.Errorf("settle = %v (Grandma owes me 100)", st)
		}
	}

	me.code("POST", "/projects/"+id+"/project-transactions", map[string]any{
		"transaction_member_id": myMember, "type": "expense", "amount": 10, "date": "2026-10-02", "tag_ids": []uuid.UUID{uuid.New()},
	}, http.StatusBadRequest, "TAG_NOT_FOUND")
	me.do("PUT", "/projects/"+id+"/project-transactions/"+ptID, map[string]any{"amount": 360, "tag_ids": []string{}},
		"updateProjectTransaction", http.StatusOK)
	marked := me.do("PUT", "/projects/"+id+"/project-transactions/"+ptID+"/mark", map[string]any{"marked": true},
		"markProjectTransaction", http.StatusOK)
	if m(marked, "row")["my_mark"] != true {
		t.Errorf("mark = %v", m(marked, "row"))
	}

	billTx, err := f.txs.Create(ctx, f.owner, transactions.CreateRequest{
		Type: transactions.TypeExpense, AccountID: &f.acctPersonal, Amount: 200, CategoryID: &f.cat, Date: "2026-10-03",
		TagIDs: []uuid.UUID{taxi},
		Splits: []transactions.SplitInput{{PersonName: "Bee", ContactID: &f.contactB, OwedAmount: 50}},
	})
	if err != nil {
		t.Fatalf("personal bill: %v", err)
	}
	billTxID := billTx.(*transactions.TransactionDetail).ID
	pulled := me.do("POST", "/projects/"+id+"/bills", map[string]any{"transaction_ids": []uuid.UUID{billTxID}},
		"", http.StatusOK) // retired route (V16), no spec op
	prow := list(pulled, "rows")[0].(map[string]any)
	pulledID := prow["id"].(string)
	if prow["linked"] != true || m(prow, "source_tx")["id"] != billTxID.String() ||
		len(list(prow, "tags")) != 1 || m(list(prow, "tags")[0])["name"] != "Taxi" {
		t.Errorf("pulled row = %v", prow)
	}
	if d, _ := f.txs.Get(ctx, f.owner, billTxID); d.EventBill == nil || !d.EventBill.Linked || d.EventBill.BillID.String() != pulledID {
		t.Errorf("tx event_bill = %+v", d.EventBill)
	}

	// Bee (now a member) sees the bill linked, without my transaction.
	beeBoard := bee.do("GET", "/projects/"+id+"/transactions?mine=true", nil, "listBoardRows", http.StatusOK)
	if rows := list(beeBoard, "data"); len(rows) != 1 || rows[0].(map[string]any)["source_tx"] != nil || rows[0].(map[string]any)["linked"] != true {
		t.Errorf("Bee's board = %v", rows)
	}
	// 0.4.1-2 rule b: another member's edit reaches my copy by MY switch; off → only the notice asks.
	if _, err := f.pool.Exec(ctx, `INSERT INTO user_notification_settings (user_id, auto_sync_events) VALUES ($1, FALSE)
		ON CONFLICT (user_id) DO UPDATE SET auto_sync_events = FALSE`, f.owner); err != nil {
		t.Fatal(err)
	}
	bee.do("PUT", "/projects/"+id+"/project-transactions/"+pulledID, map[string]any{"amount": 210, "description": "Taxi home", "also_update_link": true},
		"updateProjectTransaction", http.StatusOK)
	if got := f.countRows(t, `SELECT COUNT(*) FROM transactions WHERE id = $1 AND amount = 200`, billTxID); got != 1 {
		t.Errorf("another member's edit must not change my transaction while my switch is off")
	}
	if got := f.countRows(t, `SELECT COUNT(*) FROM notifications WHERE recipient_user_id = $1 AND type = 'project_tx_changed'
		AND payload->>'personal_transaction_id' = $2`, f.owner, billTxID.String()); got != 1 {
		t.Errorf("the tx owner should get project_tx_changed with the suggestion (got %d)", got)
	}
	if d, _ := f.txs.Get(ctx, f.owner, billTxID); d.EventBill == nil || !d.EventBill.Linked {
		t.Errorf("the link must survive different amounts: %+v", d.EventBill)
	}

	// My own edit of the bill: only this side unless I ask for both.
	before := f.accountBalance(t, f.acctPersonal)
	me.do("PUT", "/projects/"+id+"/project-transactions/"+pulledID, map[string]any{"amount": 240},
		"updateProjectTransaction", http.StatusOK)
	if f.accountBalance(t, f.acctPersonal) != before {
		t.Errorf("an edit without also_update_link moved my wallet")
	}
	up := me.do("PUT", "/projects/"+id+"/project-transactions/"+pulledID, map[string]any{"amount": 250, "also_update_link": true},
		"updateProjectTransaction", http.StatusOK)
	if len(list(up, "account_balances")) != 1 || f.accountBalance(t, f.acctPersonal) != before-50 {
		t.Errorf("linked edit: balances %v, wallet %v → %v", up["account_balances"], before, f.accountBalance(t, f.acctPersonal))
	}
	// 0.4.1-2 S9: the bill's values are edited in the bill form; the copy refuses them here.
	amt := 230.0
	if _, err := f.txs.UpdateWrite(ctx, f.owner, billTxID, transactions.UpdateRequest{Amount: &amt, AlsoUpdateLink: true}); !errors.As(err,
		new(*transactions.SharedBillFieldError)) {
		t.Fatalf("edit the source tx's amount: %v, want SHARED_BILL_FIELD", err)
	}
	if got := f.countRows(t, `SELECT COUNT(*) FROM project_transactions WHERE id = $1 AND amount = 250`, uuid.MustParse(pulledID)); got != 1 {
		t.Errorf("the bill changed from a refused edit")
	}

	imp := me.do("POST", "/projects/"+id+"/tags/import", map[string]any{}, "importProjectTags", http.StatusOK)
	if c := list(imp, "created"); len(c) != 1 || c[0].(map[string]any)["name"] != "Food" {
		t.Errorf("imported = %v", imp["created"])
	}
	me.do("GET", "/projects/"+id+"/tags", nil, "listProjectTags", http.StatusOK)

	// My tags are mine: Bee sets her own, can't edit the event.
	bee.do("PUT", "/projects/"+id, map[string]any{"tag_ids": []string{}}, "updateProject", http.StatusOK)
	bee.code("PUT", "/projects/"+id, map[string]any{"name": "Hijack"}, http.StatusForbidden, "FORBIDDEN_ROLE")
	me.do("PUT", "/projects/"+id, map[string]any{"end_date": nil, "planned_amount": nil}, "updateProject", http.StatusOK)

	detail := me.do("GET", "/projects/"+id+"/detail", nil, "getProjectDetail", http.StatusOK)
	// the event page reads each section once: members first, the summary and the board reuse them
	// (the budget excludes the auth middleware's one user lookup; +1 in 0.4.1-2 for the caller's
	// prefs / auto-sync switch read)
	if status, _, n := apitest.DoCounted(t, me.r, "GET", "/api/v1/projects/"+id+"/detail", nil); status != http.StatusOK || n > 10 {
		t.Errorf("the event detail: status %d, %d queries, budget 10", status, n)
	} else {
		t.Logf("event detail: %d queries", n)
	}
	if len(list(m(detail, "board"), "data")) != 2 || len(list(detail, "members")) != 3 {
		t.Errorf("detail board / members = %v / %v", m(detail, "board")["data"], detail["members"])
	}
	me.do("GET", "/projects/"+id+"/members", nil, "listProjectMembers", http.StatusOK)
	me.do("GET", "/projects/"+id+"/summary", nil, "getProjectSummary", http.StatusOK)

	cp := bee.do("POST", "/projects/"+id+"/project-transactions/"+pulledID+"/copy", nil, "", http.StatusCreated) // retired route (V16)
	if n(m(cp, "transaction"), "amount") != 50 {
		t.Errorf("Bee's copy = %v", m(cp, "transaction"))
	}

	me.do("DELETE", "/projects/"+id+"/tags/"+food["id"].(string), nil, "deleteProjectTag", http.StatusOK)
	var child string
	for _, r := range list(m(detail, "board"), "data") {
		for _, c := range list(r.(map[string]any), "children") {
			if r.(map[string]any)["id"] == ptID {
				child = c.(map[string]any)["id"].(string)
			}
		}
	}
	split := me.do("DELETE", "/projects/"+id+"/project-transactions/"+child, nil, "deleteProjectTransaction", http.StatusOK)
	if m(split, "row")["id"] != ptID || split["removed_row_id"] != nil {
		t.Errorf("split delete patch = row %v removed %v", m(split, "row")["id"], split["removed_row_id"])
	}
	gone := me.do("DELETE", "/projects/"+id+"/project-transactions/"+ptID, nil, "deleteProjectTransaction", http.StatusOK)
	if gone["removed_row_id"] != ptID {
		t.Errorf("removed_row_id = %v", gone["removed_row_id"])
	}
	out := me.do("DELETE", "/projects/"+id+"/bills/"+billTxID.String(), nil, "", http.StatusOK) // retired route (V16)
	if out["removed_row_id"] != pulledID {
		t.Errorf("remove bill: removed_row_id = %v", out["removed_row_id"])
	}

	var beeEmail, chaiEmail string
	_ = f.pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, f.friend).Scan(&beeEmail)
	_ = f.pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, f.mate).Scan(&chaiEmail)
	f.setAcceptInvites(t, false, f.mate) // the invite waits for Chai's answer
	inv := me.do("POST", "/projects/"+id+"/members", map[string]any{"email": chaiEmail, "display_name": "Chai"},
		"addProjectMember", http.StatusCreated)
	var pending string
	for _, x := range list(inv, "members") {
		if x.(map[string]any)["status"] == "pending" {
			pending = x.(map[string]any)["id"].(string)
		}
	}
	me.do("POST", "/projects/"+id+"/members/"+pending+"/request-link", nil, "requestProjectMemberLink", http.StatusOK)
	var nid uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT id FROM notifications WHERE recipient_user_id = $1 AND type = 'project_invite'
		ORDER BY created_at DESC LIMIT 1`, f.mate).Scan(&nid); err != nil {
		t.Fatalf("invite notification: %v", err)
	}
	chai := evClient{t, eventsEngine(f, f.mate)}
	acc := chai.do("POST", "/projects/link-requests/"+nid.String()+"/accept", nil, "acceptProjectLinkRequest", http.StatusOK)
	if m(acc, "event")["my_role"] != "contributor" {
		t.Errorf("accepted: my_role = %v", m(acc, "event")["my_role"])
	}
	_ = beeEmail
	me.do("PUT", "/projects/"+id+"/members/"+gran, map[string]any{"role": "viewer"}, "updateProjectMember", http.StatusOK)
	me.do("DELETE", "/projects/"+id+"/members/"+gran, nil, "removeProjectMember", http.StatusOK)

	me.do("POST", "/projects/"+id+"/transfer-ownership", map[string]any{"new_owner_user_id": f.friend},
		"transferProjectOwnership", http.StatusOK)
	me.do("POST", "/projects/"+id+"/leave", nil, "leaveProject", http.StatusOK)
	bee.do("DELETE", "/projects/"+id, nil, "deleteProject", http.StatusOK)

	second := me.do("POST", "/projects", map[string]any{"name": "Other"}, "createProject", http.StatusCreated)
	sid := m(second, "event")["id"].(string)
	me.do("POST", "/projects/"+sid+"/members", map[string]any{"email": chaiEmail, "display_name": "Chai"}, "addProjectMember", http.StatusCreated)
	if err := f.pool.QueryRow(ctx, `SELECT id FROM notifications WHERE recipient_user_id = $1 AND type = 'project_invite'
		ORDER BY created_at DESC LIMIT 1`, f.mate).Scan(&nid); err != nil {
		t.Fatalf("second invite: %v", err)
	}
	chai.do("POST", "/projects/link-requests/"+nid.String()+"/reject", nil, "rejectProjectLinkRequest", http.StatusOK)
}

func TestQuickCreateAndAddBillsAccountBalances(t *testing.T) {
	f := newQCFixture(t)
	me := evClient{t, eventsEngine(f, f.owner)}

	q := me.do("POST", "/projects/quick", map[string]any{
		"name": "Trip",
		"new_transaction": map[string]any{
			"type": "expense", "account_id": f.acctPersonal, "amount": 150, "category_id": f.cat, "date": "2026-09-15",
		},
		"transaction_ids": []uuid.UUID{f.bill1},
	}, "", http.StatusCreated) // retired route (V16), no spec op
	bs := list(q, "account_balances")
	if len(bs) != 1 || n(bs[0].(map[string]any), "balance") != 850 || q["new_transaction_id"] == nil {
		t.Errorf("quick create account_balances = %v", bs)
	}
	id := m(q, "detail", "event")["id"].(string)
	if pos := m(q, "detail", "summary", "my_position"); n(pos, "net") != 200 {
		t.Errorf("my net after the pull = %v, want 200 (Bee 100 + Grandma 100 owe me)", pos)
	}

	pullOnly := me.do("POST", "/projects/quick", map[string]any{"name": "Second", "transaction_ids": []uuid.UUID{f.bill2}},
		"", http.StatusCreated) // retired route (V16), no spec op
	if bs := list(pullOnly, "account_balances"); bs == nil || len(bs) != 0 {
		t.Errorf("pull-only account_balances = %v, want []", pullOnly["account_balances"])
	}

	a := me.do("POST", "/projects/"+id+"/bills", map[string]any{
		"new_transaction": map[string]any{
			"type": "expense", "account_id": f.acctPersonal, "amount": 50, "category_id": f.cat, "date": "2026-09-16",
		},
	}, "", http.StatusOK) // retired route (V16), no spec op
	if bs := list(a, "account_balances"); len(bs) != 1 || n(bs[0].(map[string]any), "balance") != 800 {
		t.Errorf("add bills account_balances = %v, want the wallet at 800", a["account_balances"])
	}
}

func TestBoardRowsCarryTransactionMember(t *testing.T) {
	f := newQCFixture(t)
	me := evClient{t, eventsEngine(f, f.owner)}
	q := me.do("POST", "/projects/quick", map[string]any{"name": "Trip", "transaction_ids": []uuid.UUID{f.bill1}},
		"", http.StatusCreated) // retired route (V16), no spec op
	id := m(q, "detail", "event")["id"].(string)
	if _, err := f.pool.Exec(context.Background(), `UPDATE users SET display_name = 'Bee Live' WHERE id = $1`, f.friend); err != nil {
		t.Fatal(err)
	}
	board := me.do("GET", "/projects/"+id+"/transactions", nil, "listBoardRows", http.StatusOK)
	rows := list(board, "data")
	if len(rows) != 1 {
		t.Fatalf("board = %v", rows)
	}
	row := rows[0].(map[string]any)
	if m(row, "transaction_member")["is_me"] != true {
		t.Errorf("payer = %v, want me", row["transaction_member"])
	}
	names := map[string]bool{}
	for _, c := range list(row, "children") {
		names[m(c, "transaction_member")["name"].(string)] = true
	}
	if !names["Bee Live"] || !names["Grandma"] {
		t.Errorf("split members = %v", names)
	}
}

func TestDeleteEventWithLinkedTxs(t *testing.T) {
	f := newQCFixture(t)
	me := evClient{t, eventsEngine(f, f.owner)}
	start := f.accountBalance(t, f.acctPersonal)
	mk := func(name string) (string, float64) {
		t.Helper()
		q := me.do("POST", "/projects/quick", map[string]any{
			"name": name,
			"new_transaction": map[string]any{
				"type": "expense", "account_id": f.acctPersonal, "amount": 100, "category_id": f.cat, "date": "2026-09-15",
			},
		}, "", http.StatusCreated) // retired route (V16), no spec op
		ev := m(q, "detail", "event")
		return ev["id"].(string), n(ev, "linked_tx_count")
	}
	keep, count := mk("Keep")
	if count != 1 {
		t.Errorf("linked_tx_count = %v, want 1", count)
	}
	// owner 2026-10-28: the bill stays as a standalone split bill, my tx still connected to it
	out := me.do("DELETE", "/projects/"+keep, nil, "deleteProject", http.StatusOK)
	if n(out, "split_bill_count") != 1 || f.accountBalance(t, f.acctPersonal) != start-100 {
		t.Errorf("delete: %v (wallet %v)", out, f.accountBalance(t, f.acctPersonal))
	}
	if got := f.countRows(t, `SELECT COUNT(*) FROM transactions t JOIN projects p ON p.id = t.project_id
		WHERE t.user_id = $1 AND t.amount = 100 AND p.kind = 'split'`, f.owner); got != 1 {
		t.Errorf("my tx should stay, connected to the split bill (%d)", got)
	}
	if got := f.countRows(t, `SELECT COUNT(*) FROM projects WHERE id = $1`, keep); got != 0 {
		t.Errorf("the event is still there")
	}
}

// DELETE /transactions ?also_delete_link: the bill made from the row goes with it; without it the
// bill stays, unlinked.
func TestDeleteTxWithLinkedBill(t *testing.T) {
	f := newQCFixture(t)
	me := evClient{t, eventsEngine(f, f.owner)}
	mk := func(name string) (string, uuid.UUID) {
		t.Helper()
		q := me.do("POST", "/projects/quick", map[string]any{
			"name": name,
			"new_transaction": map[string]any{
				"type": "expense", "account_id": f.acctPersonal, "amount": 100, "category_id": f.cat, "date": "2026-09-15",
			},
		}, "", http.StatusCreated) // retired route (V16), no spec op
		evID := m(q, "detail", "event")["id"].(string)
		var txID uuid.UUID
		if err := f.pool.QueryRow(context.Background(), `SELECT id FROM transactions WHERE user_id = $1 AND project_id = $2`,
			f.owner, evID).Scan(&txID); err != nil {
			t.Fatal(err)
		}
		return evID, txID
	}
	bills := func(ev string) int {
		return f.countRows(t, `SELECT COUNT(*) FROM project_transactions WHERE project_id = $1 AND parent_project_transaction_id IS NULL`, ev)
	}
	keep, tx := mk("Keep")
	res, err := f.txs.DeleteWrite(context.Background(), f.owner, tx, false)
	if err != nil || res.BillID != nil || bills(keep) != 1 {
		t.Fatalf("plain delete: %v %+v bills %d", err, res, bills(keep))
	}
	// 0.4.1-2 rule f: deleting my copy only disconnects it; also_delete_link no longer reaches a
	// shared bill (DELETE /event-bills/{pt_id} does).
	drop, tx := mk("Drop")
	res, err = f.txs.DeleteWrite(context.Background(), f.owner, tx, true)
	if err != nil || res.BillID != nil || bills(drop) != 1 {
		t.Fatalf("also_delete_link: %v %+v bills %d", err, res, bills(drop))
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM shared_bill_opt_outs o JOIN project_transactions b ON b.id = o.project_transaction_id
		WHERE b.project_id = $1 AND o.user_id = $2`, drop, f.owner); n != 1 {
		t.Errorf("deleting my copy must disconnect it (opt-outs %d)", n)
	}
}

func TestEventArchiveRestore(t *testing.T) {
	f := newQCFixture(t)
	me := evClient{t, eventsEngine(f, f.owner)}
	bee := evClient{t, eventsEngine(f, f.friend)}
	created := me.do("POST", "/projects", map[string]any{"name": "Old trip"}, "createProject", http.StatusCreated)
	id := m(created, "event")["id"].(string)

	bee.code("POST", "/projects/"+id+"/archive", nil, http.StatusNotFound, "NOT_FOUND") // not a member
	got := me.do("POST", "/projects/"+id+"/archive", nil, "archiveProject", http.StatusOK)
	if m(got, "event")["status"] != "archived" {
		t.Fatalf("archive = %v", m(got, "event"))
	}
	if l := me.do("GET", "/projects", nil, "listProjects", http.StatusOK); len(list(l, "data")) != 0 {
		t.Errorf("default list after archive = %v", l["data"])
	}
	if l := me.do("GET", "/projects?status=archived", nil, "listProjects", http.StatusOK); len(list(l, "data")) != 1 {
		t.Errorf("archived list = %v", l["data"])
	}
	got = me.do("POST", "/projects/"+id+"/restore", nil, "restoreProject", http.StatusOK)
	if m(got, "event")["status"] != "active" {
		t.Errorf("restore = %v", m(got, "event"))
	}
}
