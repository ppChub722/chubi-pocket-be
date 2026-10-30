package projects

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// rowsToPeople — fixtures that insert split rows straight into project_transactions (the old shape)
// get people for them: each such bill's rows become its splits (payers as given, else the main payer
// pays it all) and the rows go, as migration 129 does. payers: bill → member → paid.
func (f *qcFixture) rowsToPeople(t *testing.T, payers map[uuid.UUID]map[uuid.UUID]float64) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id FROM project_transactions b WHERE b.parent_project_transaction_id IS NULL
		AND (NOT EXISTS (SELECT 1 FROM split_transaction_people p WHERE p.split_transaction_id = b.id)
		  OR EXISTS (SELECT 1 FROM project_transactions c WHERE c.parent_project_transaction_id = b.id))`)
	if err != nil {
		t.Fatal(err)
	}
	var bills []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		bills = append(bills, id)
	}
	rows.Close()
	for _, b := range bills {
		splits := map[uuid.UUID]float64{}
		cr, err := tx.Query(ctx, `SELECT transaction_member_id, amount::float8 FROM project_transactions
			WHERE parent_project_transaction_id = $1`, b)
		if err != nil {
			t.Fatal(err)
		}
		for cr.Next() {
			var (
				m uuid.UUID
				a float64
			)
			if err := cr.Scan(&m, &a); err != nil {
				t.Fatal(err)
			}
			splits[m] += a
		}
		cr.Close()
		if _, err := tx.Exec(ctx, `DELETE FROM project_transactions WHERE parent_project_transaction_id = $1`, b); err != nil {
			t.Fatal(err)
		}
		w := billPeopleWrite{Splits: splits, Payers: payers[b]}
		if w.Payers == nil {
			w.Payers = map[uuid.UUID]float64{}
		}
		if err := putBillPeopleTx(ctx, tx, b, w); err != nil {
			t.Fatal(err)
		}
	}
	// paybacks inserted with paid_back_for_id tick the bill they paid back
	if _, err := tx.Exec(ctx, `UPDATE split_transaction_people sp SET paid_back = EXISTS (
			SELECT 1 FROM project_transactions pb WHERE pb.paid_back_for_id = sp.split_transaction_id
			   AND pb.transaction_member_id = sp.event_member_id)
		WHERE sp.paid_back <> EXISTS (SELECT 1 FROM project_transactions pb WHERE pb.paid_back_for_id = sp.split_transaction_id
			   AND pb.transaction_member_id = sp.event_member_id)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
