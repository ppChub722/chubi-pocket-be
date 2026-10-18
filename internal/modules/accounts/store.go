package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
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

const accountColumns = `id, user_id, name, type, balance, currency, icon_code, logo_url,
	description, note, status,
	credit_limit, statement_date, payment_due_date, minimum_payment,
	sort_order, created_at, updated_at, identifiers, track_balance`

func scanAccount(row pgx.Row) (*Account, error) {
	var a Account
	var iconBytes, identifiersBytes []byte
	err := row.Scan(
		&a.ID, &a.UserID, &a.Name, &a.Type, &a.Balance, &a.Currency, &iconBytes, &a.LogoURL,
		&a.Description, &a.Note, &a.Status,
		&a.CreditLimit, &a.StatementDate, &a.PaymentDueDate, &a.MinimumPayment,
		&a.SortOrder, &a.CreatedAt, &a.UpdatedAt, &identifiersBytes, &a.TrackBalance,
	)
	if err != nil {
		return nil, err
	}
	a.Identifiers = []Identifier{}
	if len(identifiersBytes) > 0 {
		if err := json.Unmarshal(identifiersBytes, &a.Identifiers); err != nil {
			return nil, fmt.Errorf("unmarshal identifiers: %w", err)
		}
	}
	a.IconCode = shared.IconFromJSON(iconBytes)
	a.Group = shared.AccountGroup(a.Type)
	return &a, nil
}

func (s *Store) GetByID(ctx context.Context, userID, id uuid.UUID) (*Account, error) {
	q := `SELECT ` + accountColumns + ` FROM accounts a
		WHERE a.id = $1 AND ` + shared.ActiveMembershipPredicate("a.id", "$2")
	a, err := scanAccount(s.db.QueryRow(ctx, q, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAccountNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	single := []Account{*a}
	if err := s.fillMembers(ctx, userID, single); err != nil {
		return nil, err
	}
	return &single[0], nil
}

// getAnyMember reads a wallet WITHOUT the active-membership gate - only for a caller who is (or
// was) a member, e.g.
func (s *Store) getAnyMember(ctx context.Context, userID, id uuid.UUID) (*Account, error) {
	a, err := scanAccount(s.db.QueryRow(ctx, `SELECT `+accountColumns+` FROM accounts a WHERE a.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAccountNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	single := []Account{*a}
	if err := s.fillMembers(ctx, userID, single); err != nil {
		return nil, err
	}
	return &single[0], nil
}

func (s *Store) List(ctx context.Context, userID uuid.UUID, status, accType string) ([]Account, error) {
	q := `SELECT ` + accountColumns + ` FROM accounts a
		WHERE ` + shared.ActiveMembershipPredicate("a.id", "$1")
	args := []any{userID}
	if status != "all" {
		args = append(args, status)
		q += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if accType != "" {
		args = append(args, accType)
		q += fmt.Sprintf(" AND type = $%d", len(args))
	}
	q += ` ORDER BY (SELECT am.sort_order FROM account_members am
		WHERE am.account_id = a.id AND am.user_id = $1
		  AND am.joined_at IS NOT NULL AND am.left_at IS NULL LIMIT 1) ASC,
		created_at ASC`

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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.fillMembers(ctx, userID, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) SummaryAggregate(ctx context.Context, accountID uuid.UUID, from, to string) (income, expense float64, count int, err error) {
	share := shared.ShareAmountExpr("t")
	q := `SELECT
		COALESCE(SUM(` + share + `) FILTER (WHERE t.type = 'income'), 0),
		COALESCE(SUM(` + share + `) FILTER (WHERE t.type = 'expense'), 0),
		COUNT(*)
		FROM transactions t
		LEFT JOIN categories c ON c.id = t.category_id
		WHERE t.account_id = $1
		  AND t.date >= $2::date
		  AND t.date <= $3::date
		  AND COALESCE(c.include_in_report, TRUE) = TRUE`
	err = s.db.QueryRow(ctx, q, accountID, from, to).
		Scan(&income, &expense, &count)
	return
}

func (s *Store) InsertTx(ctx context.Context, tx pgx.Tx, a *Account) (*Account, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}
	a.ID = id

	var iconJSON []byte
	if a.IconCode != nil {
		if iconJSON, err = json.Marshal(a.IconCode); err != nil {
			return nil, fmt.Errorf("marshal icon_code: %w", err)
		}
	}

	identifiers := a.Identifiers
	if identifiers == nil {
		identifiers = []Identifier{}
	}
	identifiersJSON, err := json.Marshal(identifiers)
	if err != nil {
		return nil, fmt.Errorf("marshal identifiers: %w", err)
	}

	q := `INSERT INTO accounts
		(id, user_id, name, type, balance, currency, icon_code, logo_url,
		 description, note, status,
		 credit_limit, statement_date, payment_due_date, minimum_payment,
		 sort_order, created_by_user_id, identifiers, track_balance)
		VALUES ($1, $2, $3, $4, 0, $5, $6::jsonb, $7,
		        $8, $9, 'active',
		        $10, $11, $12, $13, $14, $2, $15::jsonb, $16)
		RETURNING ` + accountColumns
	created, err := scanAccount(tx.QueryRow(ctx, q,
		a.ID, a.UserID, a.Name, a.Type, a.Currency, iconJSON, a.LogoURL,
		shared.CleanText(a.Description), shared.CleanText(a.Note),
		a.CreditLimit, a.StatementDate, a.PaymentDueDate, a.MinimumPayment,
		a.SortOrder, identifiersJSON, a.TrackBalance))
	if err != nil {
		return nil, fmt.Errorf("insert account: %w", err)
	}
	return created, nil
}

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
	if iconCode, present := req.IconCodeChange(); present {
		var iconJSON []byte
		if iconCode != nil {
			var err error
			if iconJSON, err = json.Marshal(iconCode); err != nil {
				return nil, fmt.Errorf("marshal icon_code: %w", err)
			}
		}
		args = append(args, iconJSON)
		q += fmt.Sprintf(", icon_code = $%d::jsonb", len(args))
	}
	if logoURL, present := req.LogoURLChange(); present {
		add("logo_url", logoURL)
	}
	if desc, present := req.DescriptionChange(); present {
		add("description", desc)
	}
	if note, present := req.NoteChange(); present {
		add("note", note)
	}
	if clearCreditFields {
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
	if req.TrackBalance != nil {
		add("track_balance", *req.TrackBalance)
	}
	if req.Identifiers != nil {
		b, err := json.Marshal(*req.Identifiers)
		if err != nil {
			return nil, fmt.Errorf("marshal identifiers: %w", err)
		}
		args = append(args, b)
		q += fmt.Sprintf(", identifiers = $%d::jsonb", len(args))
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

func (s *Store) Restore(ctx context.Context, userID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE accounts SET status = 'active', updated_by_user_id = $1
		 WHERE id = $2 AND user_id = $1 AND status = 'archived'`, userID, id)
	if err != nil {
		return fmt.Errorf("db error: %w", err)
	}
	_ = tag // already active = idempotent
	return nil
}

func (s *Store) CountArchived(ctx context.Context, userID uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM accounts a
		WHERE a.status = 'archived' AND `+shared.ActiveMembershipPredicate("a.id", "$1"), userID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count archived: %w", err)
	}
	return n, nil
}

func (s *Store) UserCurrency(ctx context.Context, userID uuid.UUID) (string, error) {
	var cur string
	if err := s.db.QueryRow(ctx, `SELECT currency FROM users WHERE id = $1`, userID).Scan(&cur); err != nil {
		return "", fmt.Errorf("user currency: %w", err)
	}
	return cur, nil
}

func (s *Store) SetMySortOrder(ctx context.Context, userID, accountID uuid.UUID, order int) error {
	_, err := s.db.Exec(ctx, `UPDATE account_members SET sort_order = $1, updated_at = NOW()
		WHERE account_id = $2 AND user_id = $3 AND joined_at IS NOT NULL AND left_at IS NULL`,
		order, accountID, userID)
	if err != nil {
		return fmt.Errorf("sort order: %w", err)
	}
	return nil
}
