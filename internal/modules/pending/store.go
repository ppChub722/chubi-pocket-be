package pending

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrPendingNotFound = errors.New("pending transaction not found")

type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

func (s *Store) Pool() *pgxpool.Pool { return s.db }

const pendingColumns = `id, source, kind, draft, target_debt_id, target_transaction_id,
	source_ref, last_error, created_at, updated_at`

func scanPending(row pgx.Row) (*PendingTransaction, error) {
	var (
		p                        PendingTransaction
		draft, ref, lastErrBytes []byte
	)
	if err := row.Scan(&p.ID, &p.Source, &p.Kind, &draft, &p.TargetDebtID, &p.TargetTransactionID,
		&ref, &lastErrBytes, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(draft, &p.Draft); err != nil {
		return nil, fmt.Errorf("draft: %w", err)
	}
	if ref != nil {
		p.SourceRef = ref
	}
	if lastErrBytes != nil {
		p.LastError = &SubmitError{}
		if err := json.Unmarshal(lastErrBytes, p.LastError); err != nil {
			return nil, fmt.Errorf("last_error: %w", err)
		}
	}
	return &p, nil
}

func (s *Store) List(ctx context.Context, userID uuid.UUID) ([]PendingTransaction, error) {
	rows, err := s.db.Query(ctx, `SELECT `+pendingColumns+` FROM pending_transactions
		WHERE user_id = $1 ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list pending: %w", err)
	}
	defer rows.Close()
	out := make([]PendingTransaction, 0)
	for rows.Next() {
		p, err := scanPending(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, userID, id uuid.UUID) (*PendingTransaction, error) {
	p, err := scanPending(s.db.QueryRow(ctx, `SELECT `+pendingColumns+`
		FROM pending_transactions WHERE id = $1 AND user_id = $2`, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPendingNotFound
	}
	return p, err
}

// Insert adds one draft (any kind / source — the service decides).
func (s *Store) Insert(
	ctx context.Context, userID uuid.UUID, source, kind string, d Draft,
	targetDebtID, targetTxID *uuid.UUID, sourceRef json.RawMessage,
) (*PendingTransaction, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}
	draft, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	var ref []byte
	if len(sourceRef) > 0 {
		ref = sourceRef
	}
	return scanPending(s.db.QueryRow(ctx, `
		INSERT INTO pending_transactions
			(id, user_id, source, kind, draft, target_debt_id, target_transaction_id, source_ref)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+pendingColumns,
		id, userID, source, kind, draft, targetDebtID, targetTxID, ref))
}

// UpdateDraft replaces the draft and clears the last submit error.
func (s *Store) UpdateDraft(ctx context.Context, userID, id uuid.UUID, d Draft) (*PendingTransaction, error) {
	draft, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	p, err := scanPending(s.db.QueryRow(ctx, `
		UPDATE pending_transactions SET draft = $3, last_error = NULL
		WHERE id = $1 AND user_id = $2
		RETURNING `+pendingColumns, id, userID, draft))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPendingNotFound
	}
	return p, err
}

func (s *Store) SetError(ctx context.Context, userID, id uuid.UUID, e SubmitError) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `UPDATE pending_transactions SET last_error = $3
		WHERE id = $1 AND user_id = $2`, id, userID, b)
	return err
}

func (s *Store) Delete(ctx context.Context, userID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM pending_transactions WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("delete pending: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrPendingNotFound
	}
	return nil
}

func (s *Store) DeleteTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `DELETE FROM pending_transactions WHERE id = $1 AND user_id = $2`, id, userID)
	return err
}
