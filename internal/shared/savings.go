package shared

// Savings v2 (0.3.1-10): a saving entry moves an amount of ONE wallet
// between buckets — the wallet's available money, the spare bucket
// ("ยังไม่จัดสรร") or a goal. A (bucket, wallet) balance is the sum of what
// came in minus what went out; saved money always knows its wallet. Read
// here because wallets, the dashboard, /me and the goals module all show it.

import (
	"context"
	"fmt"
	"math"

	"github.com/google/uuid"
)

// SavingGoalRef — spec schema SavingGoalRef.
type SavingGoalRef struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	IconCode *IconCode `json:"icon_code"`
}

// SavingBucket — one non-zero bucket of a wallet (WalletSavings.buckets).
type SavingBucket struct {
	Type     string     `json:"type"` // goal | spare
	ID       *uuid.UUID `json:"id"`
	Name     *string    `json:"name"`
	IconCode *IconCode  `json:"icon_code"`
	Amount   float64    `json:"amount"`
}

// WalletSavings — spec schema WalletSavings.
type WalletSavings struct {
	Balance             float64        `json:"balance"`
	Saved               float64        `json:"saved"`
	Spare               float64        `json:"spare"`
	Available           *float64       `json:"available"`
	SavedExceedsBalance bool           `json:"saved_exceeds_balance"`
	OverBy              float64        `json:"over_by"`
	Buckets             []SavingBucket `json:"buckets"`
}

// SavingTotals — spec schema SavingTotals (Home / More).
type SavingTotals struct {
	Count     int     `json:"count"`
	Saved     float64 `json:"saved"`
	InGoals   float64 `json:"in_goals"`
	Spare     float64 `json:"spare"`
	Available float64 `json:"available"`
}

// SavingBalance — one (wallet, bucket) balance (AccountID uuid.Nil = money with no wallet).
type SavingBalance struct {
	AccountID uuid.UUID // uuid.Nil = no wallet (unallocated)
	Bucket    string     // spare | goal
	GoalID    *uuid.UUID // nil for spare (and a deleted goal)
	Amount    float64
}

// SavingBalancesSQL — the (wallet, bucket, goal) balances of user $1 (non-
// zero), optionally narrowed by `where` on alias m (account_id, bucket,
// goal_id).
func SavingBalancesSQL(where string) string {
	if where == "" {
		where = "TRUE"
	}
	return `WITH m AS (
		SELECT account_id, to_bucket AS bucket, to_goal_id AS goal_id, amount FROM saving_entries
		WHERE user_id = $1 AND to_bucket <> 'available'
		UNION ALL
		SELECT account_id, from_bucket, from_goal_id, -amount FROM saving_entries
		WHERE user_id = $1 AND from_bucket <> 'available')
	SELECT m.account_id, m.bucket, m.goal_id, SUM(m.amount)::float8 FROM m WHERE ` + where + `
	GROUP BY 1, 2, 3 HAVING ABS(SUM(m.amount)) > 0.004`
}

// CountedSavings — the SavingBalancesSQL filter that leaves out the money of
// archived goals (0.4.0-1): an archived goal keeps its money, but it counts
// in no total (savings, wallets' saved / available) until it is restored.
const CountedSavings = `NOT EXISTS (SELECT 1 FROM saving_goals sg WHERE sg.id = m.goal_id AND sg.status = 'archived')`

// LoadSavingBalances runs SavingBalancesSQL.
func LoadSavingBalances(ctx context.Context, q Querier, userID uuid.UUID, where string, args ...any) ([]SavingBalance, error) {
	rows, err := q.Query(ctx, SavingBalancesSQL(where), append([]any{userID}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("saving balances: %w", err)
	}
	defer rows.Close()
	var out []SavingBalance
	for rows.Next() {
		var b SavingBalance
		var acc *uuid.UUID
		if err := rows.Scan(&acc, &b.Bucket, &b.GoalID, &b.Amount); err != nil {
			return nil, err
		}
		if acc != nil {
			b.AccountID = *acc
		}
		b.Amount = math.Round(b.Amount*100) / 100
		out = append(out, b)
	}
	return out, rows.Err()
}

// LoadSavingGoalRefs — goal refs by id.
func LoadSavingGoalRefs(ctx context.Context, q Querier, ids []uuid.UUID) (map[uuid.UUID]SavingGoalRef, error) {
	out := map[uuid.UUID]SavingGoalRef{}
	ids = uniqueIDs(ids)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT id, name, icon_code FROM saving_goals WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("goal refs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			r    SavingGoalRef
			icon []byte
		)
		if err := rows.Scan(&r.ID, &r.Name, &icon); err != nil {
			return nil, err
		}
		r.IconCode = IconFromJSON(icon)
		out[r.ID] = r
	}
	return out, rows.Err()
}

// LoadWalletSavings — userID's savings in each of the wallets (every id
// gets an entry, zeros included). Archived goals' money is left out.
func LoadWalletSavings(ctx context.Context, q Querier, userID uuid.UUID, accountIDs []uuid.UUID) (map[uuid.UUID]WalletSavings, error) {
	out := map[uuid.UUID]WalletSavings{}
	accountIDs = uniqueIDs(accountIDs)
	if len(accountIDs) == 0 {
		return out, nil
	}
	type acct struct {
		balance float64
		tracked bool
	}
	accts := map[uuid.UUID]acct{}
	rows, err := q.Query(ctx, `SELECT id, balance::float8, track_balance FROM accounts WHERE id = ANY($1)`, accountIDs)
	if err != nil {
		return nil, fmt.Errorf("wallet balances: %w", err)
	}
	for rows.Next() {
		var (
			id uuid.UUID
			a  acct
		)
		if err := rows.Scan(&id, &a.balance, &a.tracked); err != nil {
			rows.Close()
			return nil, err
		}
		accts[id] = a
	}
	rows.Close()
	bals, err := LoadSavingBalances(ctx, q, userID, `m.account_id = ANY($2) AND `+CountedSavings, accountIDs)
	if err != nil {
		return nil, err
	}
	var goalIDs []uuid.UUID
	for _, b := range bals {
		if b.GoalID != nil {
			goalIDs = append(goalIDs, *b.GoalID)
		}
	}
	goals, err := LoadSavingGoalRefs(ctx, q, goalIDs)
	if err != nil {
		return nil, err
	}
	byWallet := map[uuid.UUID][]SavingBalance{}
	for _, b := range bals {
		byWallet[b.AccountID] = append(byWallet[b.AccountID], b)
	}
	for _, id := range accountIDs {
		a := accts[id]
		ws := WalletSavings{Balance: round2(a.balance), Buckets: []SavingBucket{}}
		var spare *SavingBucket
		for _, b := range byWallet[id] {
			ws.Saved += b.Amount
			if b.Bucket == "spare" {
				ws.Spare += b.Amount
				if spare == nil {
					spare = &SavingBucket{Type: "spare"}
				}
				spare.Amount += b.Amount
				continue
			}
			sb := SavingBucket{Type: "goal", ID: b.GoalID, Amount: b.Amount}
			if b.GoalID != nil {
				if g, ok := goals[*b.GoalID]; ok {
					name := g.Name
					sb.Name, sb.IconCode = &name, g.IconCode
				}
			}
			ws.Buckets = append(ws.Buckets, sb)
		}
		if spare != nil {
			spare.Amount = round2(spare.Amount)
			ws.Buckets = append(ws.Buckets, *spare)
		}
		ws.Saved, ws.Spare = round2(ws.Saved), round2(ws.Spare)
		if a.tracked {
			av := round2(a.balance - ws.Saved)
			ws.Available = &av
			if ws.Saved > a.balance+0.004 {
				ws.SavedExceedsBalance, ws.OverBy = true, round2(ws.Saved-a.balance)
			}
		}
		out[id] = ws
	}
	return out, nil
}

// LoadSavingTotals — the Home / More numbers: active goals, everything
// saved (goals + spare; archived goals' money left out) and the money left available in the wallets that
// track their balance (the caller's active ones).
func LoadSavingTotals(ctx context.Context, q Querier, userID uuid.UUID) (*SavingTotals, error) {
	out := &SavingTotals{}
	bals, err := LoadSavingBalances(ctx, q, userID, CountedSavings)
	if err != nil {
		return nil, err
	}
	savedIn := map[uuid.UUID]float64{}
	for _, b := range bals {
		out.Saved += b.Amount
		if b.Bucket == "spare" {
			out.Spare += b.Amount
		} else {
			out.InGoals += b.Amount
		}
		savedIn[b.AccountID] += b.Amount
	}
	rows, err := q.Query(ctx, `SELECT COUNT(*) FROM saving_goals WHERE user_id = $1 AND status = 'active'`, userID)
	if err != nil {
		return nil, fmt.Errorf("saving goal count: %w", err)
	}
	for rows.Next() {
		if err := rows.Scan(&out.Count); err != nil {
			rows.Close()
			return nil, err
		}
	}
	rows.Close()
	rows, err = q.Query(ctx, `SELECT a.id, a.balance::float8 FROM accounts a
		WHERE a.status = 'active' AND a.track_balance AND `+ActiveMembershipPredicate("a.id", "$1"), userID)
	if err != nil {
		return nil, fmt.Errorf("saving totals: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id  uuid.UUID
			bal float64
		)
		if err := rows.Scan(&id, &bal); err != nil {
			return nil, err
		}
		out.Available += bal - savedIn[id]
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out.Saved, out.Spare, out.InGoals, out.Available = round2(out.Saved), round2(out.Spare), round2(out.InGoals), round2(out.Available)
	return out, nil
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }
