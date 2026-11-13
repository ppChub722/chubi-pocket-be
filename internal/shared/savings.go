package shared

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"
)

type SavingGoalRef struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	IconCode *IconCode `json:"icon_code"`
}

type SavingBucket struct {
	Type     string     `json:"type"` // goal | spare
	ID       *uuid.UUID `json:"id"`
	Name     *string    `json:"name"`
	IconCode *IconCode  `json:"icon_code"`
	Amount   float64    `json:"amount"`
}

type WalletSavings struct {
	Balance             float64        `json:"balance"`
	Saved               float64        `json:"saved"`
	Spare               float64        `json:"spare"`
	Available           *float64       `json:"available"`
	SavedExceedsBalance bool           `json:"saved_exceeds_balance"`
	OverBy              float64        `json:"over_by"`
	Buckets             []SavingBucket `json:"buckets"`
}

type SavingTotals struct {
	Count     int     `json:"count"`
	Saved     float64 `json:"saved"`
	InGoals   float64 `json:"in_goals"`
	Spare     float64 `json:"spare"`
	Available float64 `json:"available"`
}

type SavingBalance struct {
	AccountID uuid.UUID // uuid.Nil = no wallet (unallocated)
	Bucket    string     // spare | goal
	GoalID    *uuid.UUID // nil for spare (and a deleted goal)
	Amount    float64
}

const savingMovesCTE = `WITH m AS (
		SELECT account_id, to_bucket AS bucket, to_goal_id AS goal_id, amount FROM saving_entries
		WHERE user_id = $1 AND to_bucket <> 'available'
		UNION ALL
		SELECT account_id, from_bucket, from_goal_id, -amount FROM saving_entries
		WHERE user_id = $1 AND from_bucket <> 'available')
	`

func SavingBalancesSQL(where string) string {
	if where == "" {
		where = "TRUE"
	}
	return savingMovesCTE + `SELECT m.account_id, m.bucket, m.goal_id, SUM(m.amount)::float8 FROM m WHERE ` + where + `
	GROUP BY 1, 2, 3 HAVING ABS(SUM(m.amount)) > 0.004`
}

const CountedSavings = `NOT EXISTS (SELECT 1 FROM saving_goals sg WHERE sg.id = m.goal_id AND sg.status = 'archived')`

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

// SavingBalanceRow is a saving balance with whether it counts (not a goal that was archived).
type SavingBalanceRow struct {
	SavingBalance
	Counted bool
}

// LoadAllSavingBalances reads every saving balance of the user in ONE statement: a page
// that needs the balances of goals, the spare pool and the totals derives them all from it.
func LoadAllSavingBalances(ctx context.Context, q Querier, userID uuid.UUID) ([]SavingBalanceRow, error) {
	rows, err := q.Query(ctx, savingMovesCTE+`SELECT m.account_id, m.bucket, m.goal_id, SUM(m.amount)::float8,
		NOT EXISTS (SELECT 1 FROM saving_goals sg WHERE sg.id = m.goal_id AND sg.status = 'archived')
		FROM m GROUP BY 1, 2, 3 HAVING ABS(SUM(m.amount)) > 0.004`, userID)
	if err != nil {
		return nil, fmt.Errorf("saving balances: %w", err)
	}
	defer rows.Close()
	var out []SavingBalanceRow
	for rows.Next() {
		var r SavingBalanceRow
		var acc *uuid.UUID
		if err := rows.Scan(&acc, &r.Bucket, &r.GoalID, &r.Amount, &r.Counted); err != nil {
			return nil, err
		}
		if acc != nil {
			r.AccountID = *acc
		}
		r.Amount = math.Round(r.Amount*100) / 100
		out = append(out, r)
	}
	return out, rows.Err()
}

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

func LoadWalletSavings(ctx context.Context, q Querier, userID uuid.UUID, accountIDs []uuid.UUID) (map[uuid.UUID]WalletSavings, error) {
	out := map[uuid.UUID]WalletSavings{}
	accountIDs = uniqueIDs(accountIDs)
	if len(accountIDs) == 0 {
		return out, nil
	}
	type acct struct {
		balance float64
		tracked bool
		credit  bool // a card / pay later: it owes, it holds no savings
		auto    bool // the caller's savings wallet (accounts.auto_save)
	}
	accts := map[uuid.UUID]acct{}
	rows, err := q.Query(ctx, `SELECT a.id, `+EffectiveBalanceSQL("a.balance", "a.id", "$2")+`::float8, a.track_balance,
		a.type IN ('credit_card', 'pay_later'),
		a.auto_save AND a.user_id = $2 AND a.track_balance AND a.status = 'active'
		FROM accounts a WHERE a.id = ANY($1)`, accountIDs, userID)
	if err != nil {
		return nil, fmt.Errorf("wallet balances: %w", err)
	}
	for rows.Next() {
		var (
			id uuid.UUID
			a  acct
		)
		if err := rows.Scan(&id, &a.balance, &a.tracked, &a.credit, &a.auto); err != nil {
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
		var goalsIn float64
		for _, b := range byWallet[id] {
			if a.auto && b.Bucket == "spare" {
				continue // a savings wallet's unallocated part is derived below, never booked
			}
			if b.Bucket != "spare" {
				goalsIn += b.Amount
			}
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
		if a.auto {
			if d := AutoSaveSpare(a.balance, goalsIn); d > 0.004 {
				ws.Saved += d
				ws.Spare += d
				spare = &SavingBucket{Type: "spare", Amount: d}
			}
		}
		if spare != nil {
			spare.Amount = round2(spare.Amount)
			ws.Buckets = append(ws.Buckets, *spare)
		}
		ws.Saved, ws.Spare = round2(ws.Saved), round2(ws.Spare)
		if a.tracked && !a.credit {
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

// LoadSavingTotals is the user's saving totals in ONE statement: what is saved, how it splits
// into goals and the spare pool (a goal that was archived does not count), how many goals are
// active, and what is left of the tracked wallets' balances.
func LoadSavingTotals(ctx context.Context, q Querier, userID uuid.UUID) (*SavingTotals, error) {
	out := &SavingTotals{}
	rows, err := q.Query(ctx, savingMovesCTE+`,
		b AS (SELECT m.account_id, m.bucket, m.goal_id, SUM(m.amount) AS amt FROM m
		      GROUP BY 1, 2, 3 HAVING ABS(SUM(m.amount)) > 0.004),
		c AS (SELECT b.account_id, b.bucket, ROUND(b.amt::float8::numeric, 2)::float8 AS amt FROM b
		      WHERE `+strings.ReplaceAll(CountedSavings, "m.goal_id", "b.goal_id")+`),
		si AS (SELECT c.account_id, SUM(c.amt) AS s FROM c GROUP BY c.account_id)
		SELECT COALESCE((SELECT SUM(amt) FROM c), 0)::float8,
		       COALESCE((SELECT SUM(amt) FROM c WHERE bucket = 'spare'), 0)::float8,
		       COALESCE((SELECT SUM(amt) FROM c WHERE bucket <> 'spare'), 0)::float8,
		       (SELECT COUNT(*) FROM saving_goals WHERE user_id = $1 AND status = 'active'),
		       COALESCE((SELECT SUM(w.eff - COALESCE(si.s, 0)) FROM (
		           SELECT a.id, `+EffectiveBalanceSQL("a.balance", "a.id", "$1")+`::float8 AS eff FROM accounts a
		           WHERE a.status = 'active' AND a.track_balance AND `+ActiveMembershipPredicate("a.id", "$1")+`) w
		         LEFT JOIN si ON si.account_id = w.id), 0)::float8,
		       COALESCE((SELECT SUM(GREATEST(0, ROUND((w.eff - COALESCE(gi.s, 0))::numeric, 2))) FROM (
		           SELECT a.id, `+EffectiveBalanceSQL("a.balance", "a.id", "$1")+`::float8 AS eff FROM accounts a
		           WHERE a.user_id = $1 AND a.auto_save AND a.track_balance AND a.status = 'active') w
		         LEFT JOIN (SELECT c.account_id, SUM(c.amt) AS s FROM c WHERE c.bucket <> 'spare' GROUP BY c.account_id) gi
		                ON gi.account_id = w.id), 0)::float8`, userID)
	if err != nil {
		return nil, fmt.Errorf("saving totals: %w", err)
	}
	defer rows.Close()
	var derived float64 // the savings wallets' unallocated part (derived, never booked)
	if rows.Next() {
		if err := rows.Scan(&out.Saved, &out.Spare, &out.InGoals, &out.Count, &out.Available, &derived); err != nil {
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// It is saved and spare, and no longer available (a savings wallet's goals are in the ledger totals).
	out.Saved, out.Spare, out.Available = out.Saved+derived, out.Spare+derived, out.Available-derived
	out.Saved, out.Spare, out.InGoals, out.Available = round2(out.Saved), round2(out.Spare), round2(out.InGoals), round2(out.Available)
	return out, nil
}

// AutoSaveSpare is a savings wallet's unallocated saving: what its balance holds beyond the goals.
func AutoSaveSpare(balance, inGoals float64) float64 {
	return math.Max(0, round2(round2(balance)-round2(inGoals)))
}

// AutoSaveWallet is one savings wallet (accounts.auto_save): the whole balance counts as saved,
// so what the goals do not hold is the unallocated saving, derived here and never booked.
type AutoSaveWallet struct {
	Balance float64
	Goals   float64 // in goals (counted: not an archived goal)
	Spare   float64 // AutoSaveSpare(Balance, Goals)
}

// LoadAutoSaveWallets reads the user's savings wallets (tracked, active, owned) in ONE statement.
func LoadAutoSaveWallets(ctx context.Context, q Querier, userID uuid.UUID) (map[uuid.UUID]AutoSaveWallet, error) {
	rows, err := q.Query(ctx, savingMovesCTE+`,
		g AS (SELECT m.account_id, SUM(m.amount) AS s FROM m
		      WHERE m.bucket = 'goal' AND `+CountedSavings+` GROUP BY m.account_id)
		SELECT a.id, `+EffectiveBalanceSQL("a.balance", "a.id", "$1")+`::float8, COALESCE(g.s, 0)::float8
		  FROM accounts a LEFT JOIN g ON g.account_id = a.id
		 WHERE a.user_id = $1 AND a.auto_save AND a.track_balance AND a.status = 'active'`, userID)
	if err != nil {
		return nil, fmt.Errorf("savings wallets: %w", err)
	}
	defer rows.Close()
	out := map[uuid.UUID]AutoSaveWallet{}
	for rows.Next() {
		var (
			id uuid.UUID
			w  AutoSaveWallet
		)
		if err := rows.Scan(&id, &w.Balance, &w.Goals); err != nil {
			return nil, err
		}
		w.Balance, w.Goals = round2(w.Balance), round2(w.Goals)
		w.Spare = AutoSaveSpare(w.Balance, w.Goals)
		out[id] = w
	}
	return out, rows.Err()
}

// IsAutoSaveWallet reports whether the wallet is one of the user's savings wallets.
func IsAutoSaveWallet(ctx context.Context, q Querier, accountID uuid.UUID) (bool, error) {
	rows, err := q.Query(ctx, `SELECT COALESCE((SELECT auto_save FROM accounts WHERE id = $1), false)`, accountID)
	if err != nil {
		return false, fmt.Errorf("savings wallet: %w", err)
	}
	defer rows.Close()
	var on bool
	if rows.Next() {
		if err := rows.Scan(&on); err != nil {
			return false, err
		}
	}
	return on, rows.Err()
}

// SavingTotalsOf is LoadSavingTotals over balances already read (LoadAllSavingBalances).
func SavingTotalsOf(ctx context.Context, q Querier, userID uuid.UUID, all []SavingBalanceRow) (*SavingTotals, error) {
	t, _, err := SavingTotalsAuto(ctx, q, userID, all)
	return t, err
}

// SavingTotalsAuto is SavingTotalsOf plus the savings wallets' derived unallocated part, per wallet.
func SavingTotalsAuto(ctx context.Context, q Querier, userID uuid.UUID, all []SavingBalanceRow) (*SavingTotals, map[uuid.UUID]float64, error) {
	counted := make([]SavingBalance, 0, len(all))
	for _, r := range all {
		if r.Counted {
			counted = append(counted, r.SavingBalance)
		}
	}
	return savingTotalsOf(ctx, q, userID, counted)
}

func savingTotalsOf(ctx context.Context, q Querier, userID uuid.UUID, bals []SavingBalance) (*SavingTotals, map[uuid.UUID]float64, error) {
	out := &SavingTotals{}
	autoSpare := map[uuid.UUID]float64{}
	savedIn, goalsIn := map[uuid.UUID]float64{}, map[uuid.UUID]float64{}
	for _, b := range bals {
		out.Saved += b.Amount
		if b.Bucket == "spare" {
			out.Spare += b.Amount
		} else {
			out.InGoals += b.Amount
			goalsIn[b.AccountID] += b.Amount
		}
		savedIn[b.AccountID] += b.Amount
	}
	rows, err := q.Query(ctx, `SELECT COUNT(*) FROM saving_goals WHERE user_id = $1 AND status = 'active'`, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("saving goal count: %w", err)
	}
	for rows.Next() {
		if err := rows.Scan(&out.Count); err != nil {
			rows.Close()
			return nil, nil, err
		}
	}
	rows.Close()
	rows, err = q.Query(ctx, `SELECT a.id, `+EffectiveBalanceSQL("a.balance", "a.id", "$1")+`::float8, a.auto_save AND a.user_id = $1
		FROM accounts a WHERE a.status = 'active' AND a.track_balance AND `+ActiveMembershipPredicate("a.id", "$1"), userID)
	if err != nil {
		return nil, nil, fmt.Errorf("saving totals: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id   uuid.UUID
			bal  float64
			auto bool
		)
		if err := rows.Scan(&id, &bal, &auto); err != nil {
			return nil, nil, err
		}
		out.Available += bal - savedIn[id]
		if auto {
			// A savings wallet: what its goals don't hold is saved and spare, not available.
			d := AutoSaveSpare(bal, goalsIn[id])
			autoSpare[id] = d
			out.Saved, out.Spare, out.Available = out.Saved+d, out.Spare+d, out.Available-d
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	out.Saved, out.Spare, out.InGoals, out.Available = round2(out.Saved), round2(out.Spare), round2(out.InGoals), round2(out.Available)
	return out, autoSpare, nil
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }
