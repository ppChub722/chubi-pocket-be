package pending

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/projects"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/saving_goals"
)

func (e *apiEnv) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *apiEnv) goal(t *testing.T, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	e.exec(t, `INSERT INTO saving_goals (id, user_id, name, status) VALUES ($1, $2, $3, 'active')`, id, e.uid, name)
	return id
}

// ออม / จัดสรร / ย้าย / ถอนออม booked from drafts: every saving action reaches the saving service.
func TestChatSavingActions(t *testing.T) {
	e := newAPIEnv(t)
	e.svc.WithSavings(saving_goals.NewService(saving_goals.NewStore(e.pool)))
	ctx := context.Background()
	ref := json.RawMessage(`{"channel":"telegram"}`)
	day := ptr("2026-11-05")
	trip, car := e.goal(t, "trip"), e.goal(t, "car")
	sv := func(action string, amount float64, d Draft) Draft {
		d.Type, d.SavingAction, d.Amount, d.Date = ptr("saving"), action, &amount, day
		return d
	}
	book := func(d Draft) {
		t.Helper()
		res, err := e.svc.Book(ctx, e.uid, SourceTelegram, ref, []Draft{d})
		if err != nil || res.Booked != 1 {
			t.Fatalf("Book %s = %+v, %v", d.SavingAction, res, err)
		}
	}
	rows := func() int { return e.count(t, `SELECT COUNT(*) FROM saving_entries WHERE user_id = $1`, e.uid) }

	book(sv("save", 1000, Draft{AccountID: &e.acct}))                      // wallet -> spare
	book(sv("save", 300, Draft{AccountID: &e.acct, GoalID: &trip}))        // wallet -> a goal
	book(sv("allocate", 400, Draft{GoalID: &car}))                         // spare -> a goal
	book(sv("move", 100, Draft{FromGoalID: &car, GoalID: &trip}))          // goal -> goal
	book(sv("move", 50, Draft{FromGoalID: &car}))                          // goal -> spare (GoalID nil = spare)
	book(sv("withdraw", 60, Draft{FromGoalID: &trip, AccountID: &e.acct})) // goal -> back to the wallet
	if n := rows(); n != 6 {
		t.Fatalf("saving entries = %d, want 6", n)
	}
	if n := e.pendingCount(t); n != 0 {
		t.Fatalf("rows left pending = %d", n)
	}
	// not a saving action
	if _, err := e.svc.CreateFrom(ctx, e.uid, SourceTelegram, ref, []Draft{sv("gift", 10, Draft{})}); err == nil {
		t.Error("an unknown saving_action was accepted")
	}
}

// A debt draft: the person book with or without money, a repayment, a person who is only a typed name.
func TestChatDebtDrafts(t *testing.T) {
	e := newAPIEnv(t)
	ctx := context.Background()
	ref := json.RawMessage(`{"channel":"telegram"}`)
	day := ptr("2026-11-05")
	for _, c := range [][2]string{{"LOAN_GIVEN", "expense"}, {"LOAN_TAKEN", "income"}, {"DEBT_PAID", "expense"}, {"DEBT_RECEIVED", "income"}} {
		e.exec(t, `INSERT INTO categories (id, user_id, name, type, is_system, system_kind) VALUES ($1, $2, $3, $4, TRUE, $3)`, uuid.New(), e.uid, c[0], c[1])
	}
	contact := uuid.New()
	e.exec(t, `INSERT INTO contacts (id, user_id, display_name, status) VALUES ($1, $2, 'Boy', 'active')`, contact, e.uid)
	debt := func(side string, money bool, amount float64, c *uuid.UUID, name *string) Draft {
		return Draft{Amount: &amount, Date: day, AccountID: &e.acct, Debt: &DraftDebt{ContactID: c, PersonName: name, Side: side, WithMoney: money}}
	}
	book := func(d Draft) {
		t.Helper()
		res, err := e.svc.Book(ctx, e.uid, SourceTelegram, ref, []Draft{d})
		if err != nil || res.Booked != 1 {
			t.Fatalf("Book debt %s = %+v, %v", d.Debt.Side, res, err)
		}
	}
	entries := func() int { return e.count(t, `SELECT COUNT(*) FROM personal_debts WHERE user_id = $1`, e.uid) }
	txs := func() int { return e.count(t, `SELECT COUNT(*) FROM transactions WHERE user_id = $1`, e.uid) }

	book(debt(SideTheyOweMe, true, 500, &contact, nil)) // "ให้บอยยืม 500": money out + the debt
	if entries() != 1 || txs() != 1 {
		t.Fatalf("lend with money: entries %d txs %d, want 1 / 1", entries(), txs())
	}
	book(debt(SideTheyOweMe, false, 200, &contact, nil)) // "บอยค้างฉัน 200": the debt only
	if entries() != 2 || txs() != 1 {
		t.Fatalf("lend ledger-only: entries %d txs %d, want 2 / 1", entries(), txs())
	}
	book(debt(SideTheyRepay, true, 100, &contact, nil)) // "บอยคืน 100": money in
	if entries() != 3 || txs() != 2 {
		t.Fatalf("repayment: entries %d txs %d, want 3 / 2", entries(), txs())
	}
	name := "Ling"
	book(debt(SideIOweThem, false, 80, nil, &name)) // a person who is only a name
	if entries() != 4 {
		t.Fatalf("named person: entries %d, want 4", entries())
	}
	// readiness: a debt needs a person, and a wallet when money moves
	missing := e.svc.Check([]Draft{
		debt(SideTheyOweMe, true, 10, nil, nil),
		{Amount: ptr(10.0), Date: day, Debt: &DraftDebt{ContactID: &contact, Side: SideTheyOweMe, WithMoney: true}},
		debt(SideTheyOweMe, false, 10, &contact, nil),
	})
	if len(missing[0]) != 1 || missing[0][0] != NeedPerson || len(missing[1]) != 1 || missing[1][0] != NeedWallet || len(missing[2]) != 0 {
		t.Errorf("Check = %v", missing)
	}
}

// A bill that makes its event: the event, the paper and my books in one go.
func TestChatNewEventBill(t *testing.T) {
	e := newAPIEnv(t)
	ctx := context.Background()
	ref := json.RawMessage(`{"channel":"telegram"}`)
	d := Draft{Type: ptr("expense"), Amount: ptr(600.0), Date: ptr("2026-11-05"), Description: ptr("hotel"),
		AccountID: &e.acct, CategoryID: &e.cat,
		Paper: &DraftPaper{
			NewEvent:  &projects.NewEventInput{Name: "Phuket trip"},
			SplitMode: "equal",
			People:    []projects.PaperPersonInput{{Me: true, Paid: 600}, {PersonName: ptr("Lee")}},
			Pay:       "wallet",
		}}
	res, err := e.svc.Book(ctx, e.uid, SourceTelegram, ref, []Draft{d})
	if err != nil || res.Booked != 1 {
		t.Fatalf("Book = %+v, %v", res, err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM projects WHERE owner_user_id = $1 AND name = 'Phuket trip'`, e.uid); n != 1 {
		t.Fatalf("events named Phuket trip = %d, want 1", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM transactions WHERE user_id = $1`, e.uid); n != 1 {
		t.Fatalf("my transactions = %d, want 1", n)
	}
}

// A move or withdrawal above what the source holds is refused (409 INSUFFICIENT_SAVING): nothing is booked, nothing
// is left pending, and the error says which code.
func TestChatSavingAboveWhatIsHeld(t *testing.T) {
	e := newAPIEnv(t)
	e.svc.WithSavings(saving_goals.NewService(saving_goals.NewStore(e.pool)))
	goal := e.goal(t, "trip")
	amount := 500.0
	d := Draft{Type: ptr("saving"), SavingAction: ActionWithdraw, Amount: &amount, Date: ptr("2026-11-05"), FromGoalID: &goal, AccountID: &e.acct}
	res, err := e.svc.Book(context.Background(), e.uid, SourceTelegram, nil, []Draft{d})
	var be *BookError
	if !errors.As(err, &be) || be.Code != "INSUFFICIENT_SAVING" || res.Booked != 0 {
		t.Fatalf("Book = %+v, %v", res, err)
	}
	if n := e.pendingCount(t); n != 0 {
		t.Fatalf("rows left pending = %d", n)
	}
}
