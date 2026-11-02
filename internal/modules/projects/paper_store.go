package projects

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

var (
	ErrPaperNotFound = errors.New("bill not found")
	// ErrPaperChanged — someone saved the paper since this version was read (409 BILL_CHANGED).
	ErrPaperChanged     = errors.New("the bill was changed by someone else: reload it")
	ErrPaperNotEven     = apierr.Invalid("what everyone paid and what everyone owes must each add up to the amount", "people", apierr.FieldInvalid)
	ErrNotPaperOwner    = errors.New("only the bill's owner can do this")
	ErrNotOnPaper       = errors.New("you are not on this bill")
	ErrNothingToClaim   = errors.New("this bill gives you nothing to put in your books")
	ErrAlreadyClaimed   = errors.New("your books already hold this bill")
	ErrPaybackSplitOnly = apierr.Invalid("a payback is a bill of an event", "type", apierr.FieldInvalid)
)

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// collectIDs — ids from one query inside a DB transaction.
func collectIDs(ctx context.Context, q shared.Querier, sql string, args ...any) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("ids: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// paperRow — the paper's own row, locked.
type paperRow struct {
	ID        uuid.UUID
	ProjectID *uuid.UUID
	Owner     uuid.UUID
	Type      string
	Amount    float64
	Date      string
	Desc      *string
	SplitMode string
	Version   int
}

func lockPaperTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*paperRow, error) {
	var r paperRow
	err := tx.QueryRow(ctx, `SELECT id, project_id, owner_user_id, type, amount::float8, to_char(date, 'YYYY-MM-DD'),
		description, split_mode, version FROM project_transactions WHERE id = $1 FOR UPDATE`, id).
		Scan(&r.ID, &r.ProjectID, &r.Owner, &r.Type, &r.Amount, &r.Date, &r.Desc, &r.SplitMode, &r.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPaperNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("paper: %w", err)
	}
	return &r, nil
}

// peopleTx — the paper's rows now, by id, in order.
func peopleTx(ctx context.Context, tx pgx.Tx, paperID uuid.UUID) ([]paperPerson, error) {
	rows, err := tx.Query(ctx, `SELECT id, user_id, project_member_id, name, share::float8, paid::float8, status, sort
		FROM split_transaction_people WHERE split_transaction_id = $1 ORDER BY sort, created_at, id`, paperID)
	if err != nil {
		return nil, fmt.Errorf("paper people: %w", err)
	}
	defer rows.Close()
	var out []paperPerson
	for rows.Next() {
		var p paperPerson
		if err := rows.Scan(&p.ID, &p.UserID, &p.MemberID, &p.Name, &p.Share, &p.Paid, &p.Status, &p.Sort); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func byID(ps []paperPerson) map[uuid.UUID]paperPerson {
	m := make(map[uuid.UUID]paperPerson, len(ps))
	for _, p := range ps {
		m[p.ID] = p
	}
	return m
}

// balanced — Σ paid and Σ share both equal the amount.
func balanced(amount float64, ps []paperPerson) bool {
	var paid, share float64
	for _, p := range ps {
		paid += p.Paid
		share += p.Share
	}
	return math.Abs(paid-amount) < 0.005 && math.Abs(share-amount) < 0.005
}

// equalSplit — every share the same (the leftover cents on the owner's row, else the first).
func equalSplit(amount float64, ps []paperPerson, owner uuid.UUID) {
	if len(ps) == 0 {
		return
	}
	lead := 0
	for i, p := range ps {
		if p.UserID != nil && *p.UserID == owner {
			lead = i
		}
	}
	shares := shared.EqualShares(amount, len(ps), lead)
	for i := range ps {
		ps[i].Share = shares[i]
	}
}

// insertPaperTx — a new paper row.
func insertPaperTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, projectID *uuid.UUID, owner uuid.UUID, in PaperInput) error {
	_, err := tx.Exec(ctx, `INSERT INTO project_transactions
		(id, project_id, owner_user_id, type, payback_kind, amount, date, description, note, split_mode,
		 created_by_user_id, updated_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7::date, $8, $9, $10, $3, $3)`,
		id, projectID, owner, in.Type, in.PaybackKind, round2(in.Amount), in.Date,
		shared.CleanText(in.Description), shared.CleanText(in.Note), splitModeOr(in.SplitMode))
	if err != nil {
		return fmt.Errorf("insert paper: %w", err)
	}
	return nil
}

func splitModeOr(m string) string {
	if m == "" {
		return shared.SplitCustom
	}
	return m
}

// updatePaperTx — the paper's own fields; version must still be the one the writer read.
func updatePaperTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, version int, writer uuid.UUID, in PaperInput) error {
	tag, err := tx.Exec(ctx, `UPDATE project_transactions SET type = $3, payback_kind = $4, amount = $5, date = $6::date,
		description = $7, note = $8, split_mode = $9, version = version + 1, updated_by_user_id = $10
		WHERE id = $1 AND version = $2`,
		id, version, in.Type, in.PaybackKind, round2(in.Amount), in.Date,
		shared.CleanText(in.Description), shared.CleanText(in.Note), splitModeOr(in.SplitMode), writer)
	if err != nil {
		return fmt.Errorf("update paper: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrPaperChanged
	}
	return nil
}

// bumpPaperTx — a change of the people only (merge, removal, move): a new version.
func bumpPaperTx(ctx context.Context, tx pgx.Tx, id, writer uuid.UUID) error {
	if _, err := tx.Exec(ctx, `UPDATE project_transactions SET version = version + 1, updated_by_user_id = $2,
		updated_at = NOW() WHERE id = $1`, id, writer); err != nil {
		return fmt.Errorf("bump paper: %w", err)
	}
	return nil
}

// putPeopleTx — the paper's rows become want: rows not in want go (answers them), kept rows take
// their new share / paid / order, new rows are inserted (their ids filled in). The writer's contact
// bindings are recorded.
func putPeopleTx(ctx context.Context, tx pgx.Tx, paperID, writer uuid.UUID, want []paperPerson) ([]paperPerson, error) {
	cur, err := peopleTx(ctx, tx, paperID)
	if err != nil {
		return nil, err
	}
	keep := map[uuid.UUID]bool{}
	for _, p := range want {
		if p.ID != uuid.Nil {
			keep[p.ID] = true
		}
	}
	var gone []paperPerson
	for _, p := range cur {
		if !keep[p.ID] {
			gone = append(gone, p)
		}
	}
	for _, p := range gone {
		if _, err := tx.Exec(ctx, `DELETE FROM split_transaction_people WHERE id = $1`, p.ID); err != nil {
			return nil, fmt.Errorf("remove person: %w", err)
		}
	}
	for i := range want {
		p := &want[i]
		if p.ID != uuid.Nil {
			if _, err := tx.Exec(ctx, `UPDATE split_transaction_people SET share = $2, paid = $3, sort = $4,
				project_member_id = $5, user_id = $6, name = $7, status = $8 WHERE id = $1`,
				p.ID, p.Share, p.Paid, p.Sort, p.MemberID, p.UserID, p.Name, p.Status); err != nil {
				return nil, fmt.Errorf("update person: %w", err)
			}
			continue
		}
		if err := tx.QueryRow(ctx, `INSERT INTO split_transaction_people
			(split_transaction_id, user_id, project_member_id, name, share, paid, sort, status)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
			paperID, p.UserID, p.MemberID, p.Name, p.Share, p.Paid, p.Sort, p.Status).Scan(&p.ID); err != nil {
			return nil, fmt.Errorf("insert person: %w", err)
		}
		if p.ContactID != nil {
			if _, err := tx.Exec(ctx, `INSERT INTO split_person_contacts (split_person_id, user_id, contact_id)
				VALUES ($1, $2, $3) ON CONFLICT (split_person_id, user_id) DO UPDATE SET contact_id = EXCLUDED.contact_id`,
				p.ID, writer, *p.ContactID); err != nil {
				return nil, fmt.Errorf("bind person contact: %w", err)
			}
		}
	}
	return gone, nil
}

// setPaperTagsTx — an event bill's event tags (ids must be the event's).
func setPaperTagsTx(ctx context.Context, tx pgx.Tx, paperID, projectID uuid.UUID, tagIDs []uuid.UUID) error {
	if _, err := tx.Exec(ctx, `DELETE FROM project_transaction_tags WHERE project_transaction_id = $1`, paperID); err != nil {
		return fmt.Errorf("clear bill tags: %w", err)
	}
	if len(tagIDs) == 0 {
		return nil
	}
	tag, err := tx.Exec(ctx, `INSERT INTO project_transaction_tags (project_transaction_id, event_tag_id)
		SELECT $1, et.id FROM event_tags et WHERE et.id = ANY($2) AND et.project_id = $3
		ON CONFLICT DO NOTHING`, paperID, uniqueIDs(tagIDs), projectID)
	if err != nil {
		return fmt.Errorf("bill tags: %w", err)
	}
	if int(tag.RowsAffected()) != len(uniqueIDs(tagIDs)) {
		return ErrEventTagNotFound
	}
	return nil
}
