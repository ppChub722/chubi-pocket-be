package projects

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The writers' one door to a bill's people (split phase A): split_transaction_people is the only
// record of who is in a bill, their share, what they paid, ticks and opt-outs. The bill row keeps the
// amount and its main payer (transaction_member_id); the main payer's share is the rest of the bill.

// billPeopleWrite — what a writer changes; a nil map keeps what the bill has.
type billPeopleWrite struct {
	// Splits — the shares of everyone but the main payer, by event member.
	Splits map[uuid.UUID]float64
	// Payers — who paid what, by event member. Kept as nil, a bill paid by its main payer alone
	// follows the bill (the new main payer, the new amount).
	Payers map[uuid.UUID]float64
}

type personNow struct {
	member         uuid.UUID
	user           *uuid.UUID
	share, paid    float64
	main           bool
	disconnectedAt *time.Time
}

func billPeopleNowTx(ctx context.Context, tx pgx.Tx, billID uuid.UUID) ([]personNow, error) {
	rows, err := tx.Query(ctx, `SELECT event_member_id, user_id, share::float8, paid::float8, sort = 0, disconnected_at
		FROM split_transaction_people WHERE split_transaction_id = $1 AND event_member_id IS NOT NULL
		ORDER BY sort FOR UPDATE`, billID)
	if err != nil {
		return nil, fmt.Errorf("bill people: %w", err)
	}
	defer rows.Close()
	var out []personNow
	for rows.Next() {
		var p personNow
		if err := rows.Scan(&p.member, &p.user, &p.share, &p.paid, &p.main, &p.disconnectedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// putBillPeopleTx makes the bill's people what the bill row and w say: involved = the main payer,
// every split member and every payer; share = the split (+ the rest of the bill for the main payer);
// a person who stays keeps their row (its id, ticks, opt-out), one who is no longer involved goes.
func putBillPeopleTx(ctx context.Context, tx pgx.Tx, billID uuid.UUID, w billPeopleWrite) error {
	var (
		amount float64
		main   *uuid.UUID
	)
	if err := tx.QueryRow(ctx, `SELECT amount::float8, transaction_member_id FROM project_transactions WHERE id = $1`,
		billID).Scan(&amount, &main); err != nil {
		return fmt.Errorf("bill: %w", err)
	}
	now, err := billPeopleNowTx(ctx, tx, billID)
	if err != nil {
		return err
	}
	splits := w.Splits
	if splits == nil {
		splits = map[uuid.UUID]float64{}
		for _, p := range now {
			if !p.main && p.share > 0 {
				splits[p.member] = p.share
			}
		}
	}
	payers := w.Payers
	if payers == nil {
		payers = map[uuid.UUID]float64{}
		for _, p := range now {
			if p.paid > 0 {
				payers[p.member] = p.paid
			}
		}
		if len(payers) < 2 { // paid by one person: the main payer pays the whole bill
			payers = map[uuid.UUID]float64{}
		}
	}
	if len(payers) == 0 && main != nil {
		payers[*main] = amount
	}

	share := map[uuid.UUID]float64{}
	var sum float64
	for m, a := range splits {
		share[m] += a
		sum += a
	}
	if main != nil {
		share[*main] += amount - sum
	}
	for m := range payers {
		if _, ok := share[m]; !ok {
			share[m] = 0
		}
	}
	members := make([]uuid.UUID, 0, len(share))
	shares := make([]float64, 0, len(share))
	paid := make([]float64, 0, len(share))
	for m, s := range share {
		members = append(members, m)
		shares = append(shares, round2(max(s, 0)))
		paid = append(paid, round2(payers[m]))
	}

	// Gone first, so a member swapped for another of the same user never meets the old row.
	gone := map[uuid.UUID]*time.Time{}
	for _, p := range now {
		if _, stays := share[p.member]; !stays && p.user != nil && p.disconnectedAt != nil {
			gone[*p.user] = p.disconnectedAt
		}
	}
	// A copy of a share that goes is unlinked (the bill is remembered, as "ลงบัญชี" unlink does),
	// unless its owner stays in the bill as another member.
	if _, err := tx.Exec(ctx, `UPDATE transactions t
		SET project_id = NULL, source_project_transaction_id = NULL, unlinked_project_transaction_id = $1
		WHERE t.source_project_transaction_id = $1
		  AND t.split_person_id IN (SELECT id FROM split_transaction_people WHERE split_transaction_id = $1
		       AND event_member_id IS NOT NULL AND NOT (event_member_id = ANY($2::uuid[])))
		  AND NOT EXISTS (SELECT 1 FROM project_members pm WHERE pm.id = ANY($2::uuid[]) AND pm.user_id = t.user_id)`,
		billID, members); err != nil {
		return fmt.Errorf("unlink copy of a share: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM split_transaction_people WHERE split_transaction_id = $1
		AND event_member_id IS NOT NULL AND NOT (event_member_id = ANY($2::uuid[]))`, billID, members); err != nil {
		return fmt.Errorf("people gone: %w", err)
	}
	goneUsers := make([]uuid.UUID, 0, len(gone))
	goneAt := make([]time.Time, 0, len(gone))
	for u, at := range gone {
		goneUsers = append(goneUsers, u)
		goneAt = append(goneAt, *at)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO split_transaction_people
			(split_transaction_id, user_id, event_member_id, name, share, paid, paid_back, disconnected_at, sort)
		SELECT $1, pm.user_id, pm.id, LEFT(pm.display_name, 100), w.share, w.paid,
		       EXISTS (SELECT 1 FROM project_transactions pb WHERE pb.paid_back_for_id = $1
		                AND pb.transaction_member_id = pm.id AND pb.parent_project_transaction_id IS NULL),
		       g.at,
		       (ROW_NUMBER() OVER (ORDER BY (pm.id = $5) DESC, pm.created_at, pm.id) - 1)::INT
		FROM unnest($2::uuid[], $3::numeric[], $4::numeric[]) AS w(member, share, paid)
		JOIN project_members pm ON pm.id = w.member
		LEFT JOIN unnest($6::uuid[], $7::timestamptz[]) AS g(user_id, at) ON g.user_id = pm.user_id
		ON CONFLICT (split_transaction_id, event_member_id) WHERE event_member_id IS NOT NULL
		DO UPDATE SET user_id = EXCLUDED.user_id, name = EXCLUDED.name, share = EXCLUDED.share,
		              paid = EXCLUDED.paid, sort = EXCLUDED.sort`,
		billID, members, shares, paid, main, goneUsers, goneAt); err != nil {
		return fmt.Errorf("people: %w", err)
	}
	// a copy made before its person existed finds the person now
	if _, err := tx.Exec(ctx, `UPDATE transactions t SET split_person_id = (SELECT p.id FROM split_transaction_people p
			WHERE p.split_transaction_id = $1 AND p.user_id = t.user_id)
		WHERE t.split_transaction_id = $1 AND t.split_person_id IS NULL`, billID); err != nil {
		return fmt.Errorf("copy person: %w", err)
	}
	return nil
}

// movePersonTx — on these bills, member from's person becomes member to's; when to already has a
// person there the two merge (shares and paid add up, to's row stays).
func movePersonTx(ctx context.Context, tx pgx.Tx, bills []uuid.UUID, from, to uuid.UUID) error {
	if from == to {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE split_transaction_people t SET share = t.share + o.share, paid = t.paid + o.paid,
			paid_back = t.paid_back OR o.paid_back
		FROM split_transaction_people o
		WHERE o.split_transaction_id = t.split_transaction_id AND o.split_transaction_id = ANY($1)
		  AND o.event_member_id = $2 AND t.event_member_id = $3`, bills, from, to); err != nil {
		return fmt.Errorf("merge person: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM split_transaction_people o
		WHERE o.split_transaction_id = ANY($1) AND o.event_member_id = $2
		  AND EXISTS (SELECT 1 FROM split_transaction_people t
		              WHERE t.split_transaction_id = o.split_transaction_id AND t.event_member_id = $3)`,
		bills, from, to); err != nil {
		return fmt.Errorf("merged person: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE split_transaction_people sp
		SET event_member_id = pm.id, user_id = pm.user_id, name = LEFT(pm.display_name, 100)
		FROM project_members pm
		WHERE pm.id = $3 AND sp.split_transaction_id = ANY($1) AND sp.event_member_id = $2`, bills, from, to); err != nil {
		return fmt.Errorf("move person: %w", err)
	}
	return resortPeopleTx(ctx, tx, bills)
}

// resortPeopleTx — the main payer first (sort 0), then by when they joined the event.
func resortPeopleTx(ctx context.Context, tx pgx.Tx, bills []uuid.UUID) error {
	if _, err := tx.Exec(ctx, `UPDATE split_transaction_people sp SET sort = r.n
		FROM (SELECT p.id, (ROW_NUMBER() OVER (PARTITION BY p.split_transaction_id
		              ORDER BY (p.event_member_id = b.transaction_member_id) DESC NULLS LAST, pm.created_at, p.sort, p.id) - 1)::INT AS n
		        FROM split_transaction_people p
		        JOIN project_transactions b ON b.id = p.split_transaction_id
		        LEFT JOIN project_members pm ON pm.id = p.event_member_id
		       WHERE p.split_transaction_id = ANY($1)) r
		WHERE r.id = sp.id AND sp.sort <> r.n`, bills); err != nil {
		return fmt.Errorf("people order: %w", err)
	}
	return nil
}

// refreshPaidBackTx — the "paid back" tick of each person of a bill follows its payback rows.
func refreshPaidBackTx(ctx context.Context, tx pgx.Tx, billID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `UPDATE split_transaction_people sp SET paid_back = EXISTS (
			SELECT 1 FROM project_transactions pb WHERE pb.paid_back_for_id = sp.split_transaction_id
			   AND pb.transaction_member_id = sp.event_member_id AND pb.parent_project_transaction_id IS NULL)
		WHERE sp.split_transaction_id = $1`, billID); err != nil {
		return fmt.Errorf("paid back: %w", err)
	}
	return nil
}

// setDisconnectedTx — a user's opt-out of a bill (their copy is cut off from it), or its undo.
func setDisconnectedTx(ctx context.Context, tx pgx.Tx, billID, userID uuid.UUID, on bool) (bool, error) {
	q := `UPDATE split_transaction_people SET disconnected_at = NOW()
		WHERE split_transaction_id = $1 AND user_id = $2 AND disconnected_at IS NULL`
	if !on {
		q = `UPDATE split_transaction_people SET disconnected_at = NULL
			WHERE split_transaction_id = $1 AND user_id = $2 AND disconnected_at IS NOT NULL`
	}
	tag, err := tx.Exec(ctx, q, billID, userID)
	if err != nil {
		return false, fmt.Errorf("disconnect: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
