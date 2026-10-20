package shared

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

func LoadWalletBalances(ctx context.Context, q Querier, ids []uuid.UUID) ([]WalletBalance, error) {
	ids = uniqueIDs(ids)
	out := make([]WalletBalance, 0, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	// As of today in the owner's time zone: a future transaction is already in
	// the cached balance but not in the number shown.
	rows, err := q.Query(ctx, `SELECT a.id, `+EffectiveBalanceSQL("a.balance", "a.id", "a.user_id")+` FROM accounts a WHERE a.id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("wallet balances: %w", err)
	}
	defer rows.Close()
	byID := map[uuid.UUID]float64{}
	for rows.Next() {
		var (
			id  uuid.UUID
			bal float64
		)
		if err := rows.Scan(&id, &bal); err != nil {
			return nil, err
		}
		byID[id] = bal
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if bal, ok := byID[id]; ok {
			out = append(out, WalletBalance{AccountID: id, Balance: bal})
		}
	}
	return out, nil
}

func LoadBadges(ctx context.Context, q Querier, userID uuid.UUID) (Badges, error) {
	var b Badges
	rows, err := q.Query(ctx, `SELECT
		(SELECT COUNT(*) FROM notifications WHERE recipient_user_id = $1 AND read_at IS NULL),
		(SELECT COUNT(*) FROM pending_transactions WHERE user_id = $1)`, userID)
	if err != nil {
		return b, fmt.Errorf("badges: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&b.UnreadNotifications, &b.PendingDrafts); err != nil {
			return b, err
		}
	}
	return b, rows.Err()
}
