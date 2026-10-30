package projects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// 0.4.1-2 V12 (Q-E2): "ลงในสมุดฉัน" on the fewest-transfers lines, after completion, once per member.

var (
	ErrSettleNotCompleted = errors.New("the fewest-transfers lines can be booked only after the event is completed")
	ErrSettleBooked       = errors.New("you already booked this event's settle into your book")
	ErrSettleLines        = errors.New("lines must list each of your fewest-transfers lines once (by the other member)")
)

type MySettleBooking struct {
	BookedAt time.Time `json:"booked_at"`
}

type SettleBookLine struct {
	MemberID          uuid.UUID  `json:"member_id" binding:"required"`
	AccountID         *uuid.UUID `json:"account_id"`
	CategoryID        *uuid.UUID `json:"category_id"`
	LinkTransactionID *uuid.UUID `json:"link_transaction_id"`
}

type SettleBookRequest struct {
	Lines   []SettleBookLine `json:"lines"`
	Preview bool             `json:"preview"`
}

type SettleBookResult struct {
	Settle  *EventSettle   `json:"settle"`
	Effects shared.Effects `json:"effects"`
}

func mySettleBooking(ctx context.Context, q rowQuerier, projectID, userID uuid.UUID) (*MySettleBooking, error) {
	var b MySettleBooking
	err := q.QueryRow(ctx, `SELECT booked_at FROM event_settle_bookings WHERE project_id = $1 AND user_id = $2`,
		projectID, userID).Scan(&b.BookedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("settle booking: %w", err)
	}
	return &b, nil
}

// BookSettle — POST /projects/{id}/settle/book: one DB transaction (preview = rolled back).
func (s *Service) BookSettle(ctx context.Context, userID, id uuid.UUID, req SettleBookRequest) (*SettleBookResult, error) {
	if s.debts == nil || s.txs == nil {
		return nil, errors.New("settle booking needs the debts and transactions modules")
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	var (
		me        uuid.UUID
		name      string
		completed *time.Time
	)
	err = tx.QueryRow(ctx, `SELECT pm.id, p.name, p.completed_at FROM project_members pm JOIN projects p ON p.id = pm.project_id
		WHERE pm.scope_id = $1 AND pm.user_id = $2 AND pm.status = 'active' FOR SHARE OF p`, id, userID).Scan(&me, &name, &completed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProjectNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("settle member: %w", err)
	}
	if completed == nil {
		return nil, ErrSettleNotCompleted
	}
	if b, err := mySettleBooking(ctx, tx, id, userID); err != nil || b != nil {
		if err == nil {
			err = ErrSettleBooked
		}
		return nil, err
	}
	pairs, simplified, err := settleMoves(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	want := map[uuid.UUID]settleMove{}
	for _, l := range simplified {
		switch me {
		case l.from:
			want[l.to] = l
		case l.to:
			want[l.from] = l
		}
	}
	if len(req.Lines) != len(want) {
		return nil, ErrSettleLines
	}
	loc, _ := time.LoadLocation("Asia/Bangkok")
	today := time.Now().In(loc).Format("2006-01-02")
	eff := shared.NewEffects()
	type booked struct {
		MemberID      uuid.UUID `json:"member_id"`
		Amount        float64   `json:"amount"`
		Pay           bool      `json:"pay"`
		TransactionID uuid.UUID `json:"transaction_id"`
	}
	var (
		done    []booked
		wallets []uuid.UUID
	)
	seen := map[uuid.UUID]bool{}
	for _, in := range req.Lines {
		l, ok := want[in.MemberID]
		if !ok || seen[in.MemberID] {
			return nil, ErrSettleLines
		}
		seen[in.MemberID] = true
		pay := l.from == me
		typ := transactions.TypeIncome
		if pay {
			typ = transactions.TypeExpense
		}
		var txID uuid.UUID
		if in.LinkTransactionID != nil {
			if err := linkSettleTxTx(ctx, tx, userID, *in.LinkTransactionID, string(typ)); err != nil {
				return nil, err
			}
			txID = *in.LinkTransactionID
		} else {
			created, err := s.txs.CreateInTx(ctx, tx, userID, transactions.CreateRequest{
				Type: typ, Amount: l.amount, Date: today, AccountID: in.AccountID, CategoryID: in.CategoryID,
				Description: &name,
			})
			if err != nil {
				return nil, err
			}
			d, ok := created.(*transactions.TransactionDetail)
			if !ok {
				return nil, ErrQuickTxNotBillable
			}
			txID = d.ID // a plain transaction (the booking row remembers it)
			if in.AccountID != nil {
				wallets = append(wallets, *in.AccountID)
			}
		}
		done = append(done, booked{in.MemberID, l.amount, pay, txID})
	}
	// clear the real pairs I had claimed: one repayment row without a transaction per person
	others := map[uuid.UUID]bool{}
	for _, p := range pairs {
		if p.from == me {
			others[p.to] = true
		}
		if p.to == me {
			others[p.from] = true
		}
	}
	for x := range others {
		net, err := claimedNetTx(ctx, tx, userID, id, x)
		if err != nil {
			return nil, err
		}
		if math.Abs(net) <= 0.005 {
			continue
		}
		cid, person, err := personOfTx(ctx, tx, userID, x)
		if err != nil {
			return nil, err
		}
		dir := personal_debts.DirectionReceived // they owed me: they paid me back
		if net < 0 {
			dir = personal_debts.DirectionPaid
		}
		_, ef, err := s.debts.CreateEntryTx(ctx, tx, userID, personal_debts.EntryInput{
			Kind: personal_debts.KindRepayment, Direction: dir, ContactID: cid, PersonName: person,
			Amount: round2(math.Abs(net)), Date: today, Description: &name, EventID: &id,
		})
		if err != nil {
			return nil, err
		}
		eff.Merge(ef)
	}
	lines, _ := json.Marshal(done)
	if _, err := tx.Exec(ctx, `INSERT INTO event_settle_bookings (project_id, user_id, lines) VALUES ($1, $2, $3)`,
		id, userID, lines); err != nil {
		return nil, fmt.Errorf("settle booking: %w", err)
	}
	out := &SettleBookResult{Effects: eff}
	if len(wallets) > 0 {
		if out.Effects.AccountBalances, err = shared.LoadWalletBalances(ctx, tx, uniqueIDs(wallets)); err != nil {
			return nil, err
		}
	}
	if out.Settle, err = s.settleView(ctx, tx, userID, id, pairs, simplified); err != nil {
		return nil, err
	}
	out.Settle.SimplifiedAvailable = true
	if out.Settle.MySettleBooking, err = mySettleBooking(ctx, tx, id, userID); err != nil {
		return nil, err
	}
	if req.Preview {
		return out, nil // rolled back; the new transactions aren't listed
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	for _, d := range done {
		if t, err := s.txs.Get(ctx, userID, d.TransactionID); err == nil {
			out.Effects.Transactions = append(out.Effects.Transactions, *t)
		}
	}
	return out, nil
}

// claimedNetTx — my claimed records of the event with one member, signed (> 0 = they owe me): debt
// parts of its bills and rows from its debt-type bills.
func claimedNetTx(ctx context.Context, tx pgx.Tx, userID, projectID, memberID uuid.UUID) (float64, error) {
	var v float64
	err := tx.QueryRow(ctx, `SELECT COALESCE((
			SELECT SUM(CASE WHEN d.direction = 'paid' THEN b.amount ELSE -b.amount END)
			FROM personal_debt_bills b JOIN personal_debts d ON d.id = b.debt_id
			JOIN project_transactions p ON p.id = b.pt_id
			WHERE b.user_id = $1 AND p.project_id = $2 AND b.member_id = $3), 0)
		+ COALESCE((
			SELECT SUM(CASE WHEN d.direction = 'paid' THEN d.amount ELSE -d.amount END)
			FROM personal_debts d JOIN project_transactions p ON p.id = d.source_project_transaction_id
			JOIN split_transaction_people c ON c.split_transaction_id = p.id AND c.share > 0
			WHERE d.user_id = $1 AND p.project_id = $2 AND p.type = 'payback'
			  AND $3 IN (p.transaction_member_id, c.event_member_id)
			  AND NOT EXISTS (SELECT 1 FROM personal_debt_bills b2 WHERE b2.debt_id = d.id)), 0)`,
		userID, projectID, memberID).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("claimed net: %w", err)
	}
	return round2(v), nil
}

func linkSettleTxTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID, typ string) error {
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM transactions WHERE id = $1 AND user_id = $2 AND type = $3
		AND project_id IS NULL AND source_project_transaction_id IS NULL AND source_personal_debt_id IS NULL
		AND transfer_group_id IS NULL)`, txID, userID, typ).Scan(&ok); err != nil {
		return fmt.Errorf("settle link tx: %w", err)
	}
	if !ok {
		return ErrClaimTxNotLinkable
	}
	return nil
}
