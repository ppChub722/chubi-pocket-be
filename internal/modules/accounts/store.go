package accounts

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

func (s *Store) Pool() *pgxpool.Pool { return s.db }

var (
	ErrAccountNotFound = errors.New("account not found")
)

const accountColumns = `id, user_id, name, type, balance, currency, icon, color,
	description, note, status,
	credit_limit, statement_date, payment_due_date, minimum_payment,
	sort_order, created_at, updated_at`

func scanAccount(row pgx.Row) (*Account, error) {
	var a Account
	err := row.Scan(
		&a.ID, &a.UserID, &a.Name, &a.Type, &a.Balance, &a.Currency, &a.Icon, &a.Color,
		&a.Description, &a.Note, &a.Status,
		&a.CreditLimit, &a.StatementDate, &a.PaymentDueDate, &a.MinimumPayment,
		&a.SortOrder, &a.CreatedAt, &a.UpdatedAt,
	)
	return &a, err
}

// --- Reads ---

func (s *Store) GetByID(ctx context.Context, userID, id uuid.UUID) (*Account, error) {
	q := `SELECT ` + accountColumns + ` FROM accounts WHERE id = $1 AND user_id = $2`
	a, err := scanAccount(s.db.QueryRow(ctx, q, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAccountNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return a, nil
}

func (s *Store) List(ctx context.Context, userID uuid.UUID, status, accType string) ([]Account, error) {
	q := `SELECT ` + accountColumns + ` FROM accounts WHERE user_id = $1`
	args := []any{userID}
	if status != "all" {
		args = append(args, status)
		q += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if accType != "" {
		args = append(args, accType)
		q += fmt.Sprintf(" AND type = $%d", len(args))
	}
	q += " ORDER BY sort_order ASC, created_at ASC"

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	defer rows.Close()

	out := make([]Account, 0)
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// SummaryAggregate returns income / expense / count for an account in
// a date range, filtered to **reportable** transactions only — per
// spec §03/§2.7 + §05/§4.14c, the rule is single: include the row
// when its category has `include_in_report = TRUE`, OR the row has no
// category (uncategorized expense/income — still real activity).
//
// Structurally this excludes every row whose category is system
// (Opening Balance, Adjustment, Transfer In/Out — all seeded with
// include_in_report = FALSE) plus any user-flagged starter rows
// (Lending, Reimbursements). Transfers in particular drop out without
// a special branch — they always carry a system category.
//
// LEFT JOIN keeps uncategorized rows in the aggregate; COALESCE
// treats NULL category as include-by-default.
func (s *Store) SummaryAggregate(ctx context.Context, userID, accountID uuid.UUID, from, to string) (income, expense float64, count int, err error) {
	q := `SELECT
		COALESCE(SUM(t.amount) FILTER (WHERE t.type = 'income'), 0),
		COALESCE(SUM(t.amount) FILTER (WHERE t.type = 'expense'), 0),
		COUNT(*)
		FROM transactions t
		LEFT JOIN categories c ON c.id = t.category_id
		WHERE t.user_id = $1
		  AND t.account_id = $2
		  AND t.date >= $3::date
		  AND t.date <= $4::date
		  AND COALESCE(c.include_in_report, TRUE) = TRUE`
	err = s.db.QueryRow(ctx, q, userID, accountID, from, to).
		Scan(&income, &expense, &count)
	return
}

// --- Writes ---

// InsertTx inserts an accounts row inside the caller's tx with balance=0.
// Caller (Service.Create) then optionally calls transactions.CreateInTx to
// add an Opening Balance transaction in the same tx, which drives the cached
// balance to the requested value via the standard balance flow.
func (s *Store) InsertTx(ctx context.Context, tx pgx.Tx, a *Account) (*Account, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}
	a.ID = id
	q := `INSERT INTO accounts
		(id, user_id, name, type, balance, currency, icon, color,
		 description, note, status,
		 credit_limit, statement_date, payment_due_date, minimum_payment,
		 sort_order, created_by_user_id)
		VALUES ($1, $2, $3, $4, 0, $5, $6, $7,
		        $8, $9, 'active',
		        $10, $11, $12, $13, $14, $2)
		RETURNING ` + accountColumns
	created, err := scanAccount(tx.QueryRow(ctx, q,
		a.ID, a.UserID, a.Name, a.Type, a.Currency, a.Icon, a.Color,
		a.Description, a.Note,
		a.CreditLimit, a.StatementDate, a.PaymentDueDate, a.MinimumPayment,
		a.SortOrder))
	if err != nil {
		return nil, fmt.Errorf("insert account: %w", err)
	}
	return created, nil
}

// Update applies a partial update. balance is never written here — only by
// the transaction service (or the manual-adjust path which composes via tx).
func (s *Store) Update(ctx context.Context, userID, id uuid.UUID, req UpdateRequest, clearCreditFields bool) (*Account, error) {
	q := `UPDATE accounts SET updated_by_user_id = $1`
	args := []any{userID}

	add := func(col string, val any) {
		args = append(args, val)
		q += fmt.Sprintf(", %s = $%d", col, len(args))
	}

	if req.Name != nil {
		add("name", strings.TrimSpace(*req.Name))
	}
	if req.Type != nil {
		add("type", *req.Type)
	}
	if req.Currency != nil {
		add("currency", *req.Currency)
	}
	if req.Icon != nil {
		add("icon", *req.Icon)
	}
	if req.Color != nil {
		add("color", *req.Color)
	}
	// description / note presence-tracked: explicit null clears, missing
	// field leaves the column alone. The presence bools were captured by
	// UpdateRequest.UnmarshalJSON.
	if desc, present := req.DescriptionChange(); present {
		add("description", desc)
	}
	if note, present := req.NoteChange(); present {
		add("note", note)
	}
	if req.Status != nil {
		add("status", *req.Status)
	}
	if clearCreditFields {
		// Type changed FROM credit → null out billing fields per spec §3.7.
		q += ", credit_limit = NULL, statement_date = NULL, payment_due_date = NULL, minimum_payment = NULL"
	} else {
		if req.CreditLimit != nil {
			add("credit_limit", *req.CreditLimit)
		}
		if req.StatementDate != nil {
			add("statement_date", *req.StatementDate)
		}
		if req.PaymentDueDate != nil {
			add("payment_due_date", *req.PaymentDueDate)
		}
		if req.MinimumPayment != nil {
			add("minimum_payment", *req.MinimumPayment)
		}
	}
	if req.SortOrder != nil {
		add("sort_order", *req.SortOrder)
	}

	args = append(args, id)
	q += fmt.Sprintf(" WHERE id = $%d AND user_id = $1 RETURNING ", len(args)) + accountColumns

	a, err := scanAccount(s.db.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAccountNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	return a, nil
}

// Archive flips status to 'archived'. Soft delete only.
func (s *Store) Archive(ctx context.Context, userID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE accounts SET status = 'archived', updated_by_user_id = $1
		 WHERE id = $2 AND user_id = $1`, userID, id)
	if err != nil {
		return fmt.Errorf("db error: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAccountNotFound
	}
	return nil
}
