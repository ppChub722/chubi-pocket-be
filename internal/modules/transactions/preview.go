package transactions

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

func unionKeys(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range append(append([]string{}, a...), b...) {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// previewResult — read the edit's result in the savepoint (sp), then the "before" in the outer DB
// transaction once the savepoint is rolled back.
func (s *Service) previewResult(ctx context.Context, outer, sp pgx.Tx, userID uuid.UUID, u *updated) (*PreviewResult, error) {
	rows, err := buildDetails(ctx, sp, userID, u.rows, true)
	if err != nil {
		return nil, err
	}
	out := &PreviewResult{Preview: true, Rows: rows, People: []PersonDelta{}, Wallets: []WalletDelta{}}
	afterNet := map[string]float64{}
	persons := map[string]shared.PersonRef{}
	if s.debtLinks != nil {
		for _, k := range u.personKeys {
			p, n, err := s.debtLinks.PersonNetTx(ctx, sp, userID, k)
			if err != nil {
				return nil, err
			}
			afterNet[k], persons[k] = n, p
		}
	}
	walletIDs := uniqueUUIDs(u.touched)
	afterBal, err := shared.LoadWalletBalances(ctx, sp, walletIDs)
	if err != nil {
		return nil, err
	}
	if err := sp.Rollback(ctx); err != nil {
		return nil, fmt.Errorf("rollback savepoint: %w", err)
	}
	for _, k := range u.personKeys {
		p, before, err := s.debtLinks.PersonNetTx(ctx, outer, userID, k)
		if err != nil {
			return nil, err
		}
		if persons[k].Name == "" {
			persons[k] = p
		}
		out.People = append(out.People, NewPersonDelta(k, persons[k], before, afterNet[k]))
	}
	beforeBal, err := shared.LoadWalletBalances(ctx, outer, walletIDs)
	if err != nil {
		return nil, err
	}
	before := map[uuid.UUID]float64{}
	for _, b := range beforeBal {
		before[b.AccountID] = b.Balance
	}
	for _, a := range afterBal {
		out.Wallets = append(out.Wallets, WalletDelta{AccountID: a.AccountID, Before: before[a.AccountID], After: a.Balance,
			Delta: round2(a.Balance - before[a.AccountID])})
	}
	return out, nil
}
