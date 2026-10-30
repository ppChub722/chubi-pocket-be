package projects

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Multi-payer bills (0.4.1-2, owner 2026-10-28): what each person paid is on their people row; a
// bill paid by one person has the whole amount on its transaction_member_id.
// The real pairs of every bill come from the view bill_pairs (shares split over the payers in
// proportion to what they paid).

// BillPayer — one payer of a bill, page-ready.
type BillPayer struct {
	Member shared.MemberRef `json:"member"`
	Amount float64          `json:"amount"`
}

// BillPair — on one bill, Debtor owes Creditor Amount (event members).
type BillPair struct {
	Debtor, Creditor uuid.UUID
	Amount           float64
}

type payerRow struct {
	member uuid.UUID
	amount float64
}

// billPayersOf — the payers of bills (parent ids), biggest first: the payers rows, else the bill's
// transaction_member_id for the whole bill.
func billPayersOf(ctx context.Context, q shared.Querier, ptIDs []uuid.UUID) (map[uuid.UUID][]payerRow, error) {
	out := map[uuid.UUID][]payerRow{}
	if len(ptIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
		SELECT sp.split_transaction_id, sp.event_member_id, sp.paid::float8 FROM split_transaction_people sp
		WHERE sp.split_transaction_id = ANY($1) AND sp.paid > 0 AND sp.event_member_id IS NOT NULL`,
		ptIDs)
	if err != nil {
		return nil, fmt.Errorf("bill payers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			pt uuid.UUID
			r  payerRow
		)
		if err := rows.Scan(&pt, &r.member, &r.amount); err != nil {
			return nil, err
		}
		out[pt] = append(out[pt], r)
	}
	for pt := range out {
		ps := out[pt]
		sort.SliceStable(ps, func(i, j int) bool {
			if ps[i].amount != ps[j].amount {
				return ps[i].amount > ps[j].amount
			}
			return ps[i].member.String() < ps[j].member.String()
		})
	}
	return out, rows.Err()
}

// BillPairsTx — the real pairs of one bill (who owes whom on it), read through tx.
func BillPairsTx(ctx context.Context, tx pgx.Tx, ptID uuid.UUID) ([]BillPair, error) {
	rows, err := tx.Query(ctx, `SELECT debtor, creditor, amount::float8 FROM bill_pairs WHERE pt_id = $1
		ORDER BY debtor, creditor`, ptID)
	if err != nil {
		return nil, fmt.Errorf("bill pairs: %w", err)
	}
	defer rows.Close()
	var out []BillPair
	for rows.Next() {
		var p BillPair
		if err := rows.Scan(&p.Debtor, &p.Creditor, &p.Amount); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// BillPayersTx — one bill's payers (biggest first), read through tx.
func BillPayersTx(ctx context.Context, tx pgx.Tx, ptID uuid.UUID) ([]BillPair, error) {
	m, err := billPayersOf(ctx, tx, []uuid.UUID{ptID})
	if err != nil {
		return nil, err
	}
	out := make([]BillPair, 0, len(m[ptID]))
	for _, r := range m[ptID] {
		out = append(out, BillPair{Creditor: r.member, Amount: r.amount})
	}
	return out, nil
}

// SetBillPayersTx writes a bill's payers (as given; Σ = the bill, checked by the caller) on its
// people, and the bill's transaction_member_id = the biggest; one payer pays the whole bill.
func SetBillPayersTx(ctx context.Context, tx pgx.Tx, ptID uuid.UUID, payers map[uuid.UUID]float64) error {
	var (
		main uuid.UUID
		best float64
	)
	for m, a := range payers {
		if a > best || (a == best && m.String() < main.String()) {
			main, best = m, a
		}
	}
	// the splits as they are, read before the main payer (who carries the rest) changes
	people, err := billPeopleNowTx(ctx, tx, ptID)
	if err != nil {
		return err
	}
	w := billPeopleWrite{Splits: map[uuid.UUID]float64{}, Payers: map[uuid.UUID]float64{}}
	for _, p := range people {
		if !p.main && p.share > 0 {
			w.Splits[p.member] = p.share
		}
	}
	if main != uuid.Nil {
		if _, err := tx.Exec(ctx, `UPDATE project_transactions SET transaction_member_id = $2 WHERE id = $1
			AND transaction_member_id IS DISTINCT FROM $2`, ptID, main); err != nil {
			return fmt.Errorf("main payer: %w", err)
		}
	}
	if len(payers) > 1 {
		w.Payers = payers
	}
	return putBillPeopleTx(ctx, tx, ptID, w)
}
