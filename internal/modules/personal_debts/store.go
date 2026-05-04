package personal_debts

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
	ErrDebtNotFound      = errors.New("personal debt not found")
	ErrAlreadySettled    = errors.New("debt is already settled")
	ErrAlreadyCancelled  = errors.New("debt is already cancelled")
	ErrOverpayment       = errors.New("settle amount exceeds outstanding")
)

const debtColumns = `id, user_id, direction,
	counterparty_contact_id, counterparty_person_name,
	source_transaction_id, source_project_transaction_id, project_id,
	amount, settled_amount, currency, status, note,
	created_at, updated_at`

func scanDebt(row pgx.Row) (*PersonalDebt, error) {
	var d PersonalDebt
	err := row.Scan(
		&d.ID, &d.UserID, &d.Direction,
		&d.CounterpartyContactID, &d.CounterpartyPersonName,
		&d.SourceTransactionID, &d.SourceProjectTransactionID, &d.ProjectID,
		&d.Amount, &d.SettledAmount, &d.Currency, &d.Status, &d.Note,
		&d.CreatedAt, &d.UpdatedAt,
	)
	return &d, err
}

// --- Manual create / list / get / update / cancel / delete ---

func (s *Store) Create(ctx context.Context, userID uuid.UUID, req CreateRequest) (*PersonalDebt, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}
	q := `INSERT INTO personal_debts
		(id, user_id, direction, counterparty_contact_id, counterparty_person_name,
		 amount, currency, note, created_by_user_id, updated_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $2, $2)
		RETURNING ` + debtColumns
	return scanDebt(s.db.QueryRow(ctx, q,
		id, userID, req.Direction, req.CounterpartyContactID,
		strings.TrimSpace(req.CounterpartyPersonName),
		req.Amount, strings.ToUpper(req.Currency), req.Note,
	))
}

// CreateAttachedTx is used by transactions module when an expense/income
// is created with splits — inserts one debt row per debtor in the same
// caller's tx. Direction is derived from the caller's role:
//   - splitter (creditor) → 'owed_to_me'
//   - linked debtor (auto-created on partner's side) → 'i_owe'
func (s *Store) CreateAttachedTx(
	ctx context.Context, tx pgx.Tx,
	userID uuid.UUID, direction string,
	contactID *uuid.UUID, personName string,
	sourceTxID, sourceProjectTxID, projectID *uuid.UUID,
	amount float64, currency string, note *string,
) (*PersonalDebt, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}
	q := `INSERT INTO personal_debts
		(id, user_id, direction, counterparty_contact_id, counterparty_person_name,
		 source_transaction_id, source_project_transaction_id, project_id,
		 amount, currency, note, created_by_user_id, updated_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $2, $2)
		RETURNING ` + debtColumns
	return scanDebt(tx.QueryRow(ctx, q,
		id, userID, direction, contactID, strings.TrimSpace(personName),
		sourceTxID, sourceProjectTxID, projectID,
		amount, strings.ToUpper(currency), note,
	))
}

func (s *Store) GetByID(ctx context.Context, userID, id uuid.UUID) (*PersonalDebt, error) {
	q := `SELECT ` + debtColumns + ` FROM personal_debts WHERE id = $1 AND user_id = $2`
	d, err := scanDebt(s.db.QueryRow(ctx, q, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDebtNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return d, nil
}

func (s *Store) List(ctx context.Context, userID uuid.UUID, f ListFilter) ([]PersonalDebtView, int, error) {
	whereClauses := []string{"user_id = $1"}
	args := []any{userID}

	switch strings.ToLower(strings.TrimSpace(f.Direction)) {
	case "i_owe":
		whereClauses = append(whereClauses, "direction = 'i_owe'")
	case "owed_to_me":
		whereClauses = append(whereClauses, "direction = 'owed_to_me'")
	}

	switch strings.ToLower(strings.TrimSpace(f.Status)) {
	case "", "open":
		whereClauses = append(whereClauses, "status = 'open'")
	case "settled":
		whereClauses = append(whereClauses, "status = 'settled'")
	case "cancelled":
		whereClauses = append(whereClauses, "status = 'cancelled'")
	case "all":
		// no filter
	}

	if f.CounterpartyContactID != nil {
		args = append(args, *f.CounterpartyContactID)
		whereClauses = append(whereClauses, fmt.Sprintf("counterparty_contact_id = $%d", len(args)))
	}
	if f.From != nil {
		args = append(args, *f.From)
		whereClauses = append(whereClauses, fmt.Sprintf("created_at >= $%d::timestamptz", len(args)))
	}
	if f.To != nil {
		args = append(args, *f.To)
		whereClauses = append(whereClauses, fmt.Sprintf("created_at <= $%d::timestamptz", len(args)))
	}

	where := strings.Join(whereClauses, " AND ")

	var total int
	if err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM personal_debts WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count: %w", err)
	}

	args = append(args, f.PerPage)
	limitIdx := len(args)
	args = append(args, (f.Page-1)*f.PerPage)
	offsetIdx := len(args)

	q := fmt.Sprintf(`
		SELECT %s, (amount - settled_amount) AS outstanding
		FROM personal_debts
		WHERE %s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d`, debtColumns, where, limitIdx, offsetIdx)

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list: %w", err)
	}
	defer rows.Close()

	out := make([]PersonalDebtView, 0, f.PerPage)
	for rows.Next() {
		var v PersonalDebtView
		if err := rows.Scan(
			&v.ID, &v.UserID, &v.Direction,
			&v.CounterpartyContactID, &v.CounterpartyPersonName,
			&v.SourceTransactionID, &v.SourceProjectTransactionID, &v.ProjectID,
			&v.Amount, &v.SettledAmount, &v.Currency, &v.Status, &v.Note,
			&v.CreatedAt, &v.UpdatedAt, &v.Outstanding,
		); err != nil {
			return nil, 0, fmt.Errorf("scan: %w", err)
		}
		out = append(out, v)
	}
	return out, total, rows.Err()
}

// Update applies a partial patch. Direct-edit path for non-cash adjustments
// (forgiveness, barter, math errors).
func (s *Store) Update(ctx context.Context, userID, id uuid.UUID, req UpdateRequest) (*PersonalDebt, error) {
	current, err := s.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}

	q := `UPDATE personal_debts SET updated_by_user_id = $1`
	args := []any{userID}

	if req.CounterpartyContactID != nil {
		args = append(args, *req.CounterpartyContactID)
		q += fmt.Sprintf(", counterparty_contact_id = $%d", len(args))
	}
	if req.CounterpartyPersonName != nil {
		args = append(args, strings.TrimSpace(*req.CounterpartyPersonName))
		q += fmt.Sprintf(", counterparty_person_name = $%d", len(args))
	}
	if req.Amount != nil {
		args = append(args, *req.Amount)
		q += fmt.Sprintf(", amount = $%d", len(args))
	}
	if req.SettledAmount != nil {
		args = append(args, *req.SettledAmount)
		q += fmt.Sprintf(", settled_amount = $%d", len(args))
	}
	if req.Currency != nil {
		args = append(args, strings.ToUpper(*req.Currency))
		q += fmt.Sprintf(", currency = $%d", len(args))
	}
	if req.Status != nil {
		// status='settled' requires settled_amount >= amount.
		nextSettled := current.SettledAmount
		if req.SettledAmount != nil {
			nextSettled = *req.SettledAmount
		}
		nextAmount := current.Amount
		if req.Amount != nil {
			nextAmount = *req.Amount
		}
		if *req.Status == StatusSettled && nextSettled < nextAmount {
			return nil, fmt.Errorf("cannot mark settled when settled_amount < amount")
		}
		args = append(args, *req.Status)
		q += fmt.Sprintf(", status = $%d", len(args))
	}
	if req.Note != nil {
		args = append(args, *req.Note)
		q += fmt.Sprintf(", note = $%d", len(args))
	}

	args = append(args, id)
	q += fmt.Sprintf(" WHERE id = $%d AND user_id = $1 RETURNING ", len(args)) + debtColumns

	d, err := scanDebt(s.db.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDebtNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	return d, nil
}

func (s *Store) Cancel(ctx context.Context, userID, id uuid.UUID) (*PersonalDebt, error) {
	current, err := s.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if current.Status == StatusCancelled {
		return nil, ErrAlreadyCancelled
	}
	q := `UPDATE personal_debts SET status = 'cancelled', updated_by_user_id = $1
		WHERE id = $2 AND user_id = $1 RETURNING ` + debtColumns
	return scanDebt(s.db.QueryRow(ctx, q, userID, id))
}

func (s *Store) Delete(ctx context.Context, userID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM personal_debts WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrDebtNotFound
	}
	return nil
}

// SettleInTx bumps settled_amount within the provided tx. Caller is
// responsible for the corresponding income/expense row in transactions.
// Auto-flips status to 'settled' when fully covered.
func (s *Store) SettleInTx(
	ctx context.Context, tx pgx.Tx, userID, id uuid.UUID, amount float64,
) (*PersonalDebt, error) {
	q := `UPDATE personal_debts SET
		settled_amount = settled_amount + $1,
		status = CASE WHEN settled_amount + $1 >= amount THEN 'settled' ELSE status END,
		updated_by_user_id = $2
		WHERE id = $3 AND user_id = $2
		RETURNING ` + debtColumns
	d, err := scanDebt(tx.QueryRow(ctx, q, amount, userID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDebtNotFound
	}
	return d, err
}

// AutoBumpInTx — called by transactions module's hook when a transaction
// with source_personal_debt_id lands. The transaction creator (debt owner)
// has already been validated to own the debt; just bump.
func (s *Store) AutoBumpInTx(
	ctx context.Context, tx pgx.Tx, userID, debtID uuid.UUID, delta float64,
) error {
	if delta <= 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `
		UPDATE personal_debts SET
			settled_amount = LEAST(amount, settled_amount + $1),
			status = CASE WHEN settled_amount + $1 >= amount THEN 'settled' ELSE status END,
			updated_by_user_id = $2
		WHERE id = $3 AND user_id = $2 AND status = 'open'`,
		delta, userID, debtID)
	if err != nil {
		return fmt.Errorf("auto-bump: %w", err)
	}
	return nil
}

// People aggregates open debts grouped by counterparty (contact_id when
// present, else lower(person_name)) and computes net position per person.
func (s *Store) People(ctx context.Context, userID uuid.UUID) ([]PersonRow, float64, float64, error) {
	q := `
		WITH grouped AS (
			SELECT
				counterparty_contact_id,
				LOWER(COALESCE(
					(SELECT COALESCE(c.nickname, c.display_name) FROM contacts c
					 WHERE c.id = pd.counterparty_contact_id),
					pd.counterparty_person_name
				)) AS group_key,
				COALESCE(
					(SELECT COALESCE(c.nickname, c.display_name) FROM contacts c
					 WHERE c.id = pd.counterparty_contact_id),
					pd.counterparty_person_name
				) AS display_name,
				direction,
				(amount - settled_amount) AS outstanding
			FROM personal_debts pd
			WHERE user_id = $1 AND status = 'open'
		)
		SELECT
			counterparty_contact_id,
			MAX(display_name) AS display_name,
			COALESCE(SUM(outstanding) FILTER (WHERE direction = 'owed_to_me'), 0) AS owed_to_me,
			COALESCE(SUM(outstanding) FILTER (WHERE direction = 'i_owe'), 0) AS i_owe,
			COUNT(*) AS open_count
		FROM grouped
		GROUP BY counterparty_contact_id, group_key
		ORDER BY ABS(
			COALESCE(SUM(outstanding) FILTER (WHERE direction = 'owed_to_me'), 0) -
			COALESCE(SUM(outstanding) FILTER (WHERE direction = 'i_owe'), 0)
		) DESC`

	rows, err := s.db.Query(ctx, q, userID)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("people: %w", err)
	}
	defer rows.Close()

	out := make([]PersonRow, 0)
	var totalOwed, totalIOwe float64
	for rows.Next() {
		var r PersonRow
		if err := rows.Scan(
			&r.ContactID, &r.DisplayName,
			&r.OwedToMeOpen, &r.IOweOpen, &r.OpenCount,
		); err != nil {
			return nil, 0, 0, err
		}
		r.NetPosition = r.OwedToMeOpen - r.IOweOpen
		out = append(out, r)
		totalOwed += r.OwedToMeOpen
		totalIOwe += r.IOweOpen
	}
	return out, totalOwed, totalIOwe, rows.Err()
}

// FindMatchingForCounterparty looks up the partner-side debt (created
// when a linked split fires both sides) by source transaction. Used to
// keep both books in sync — bumping caller's settled_amount also bumps
// the matching counterparty row when the relationship is symmetric.
func (s *Store) FindMatchingForCounterparty(
	ctx context.Context, tx pgx.Tx, sourceTxID uuid.UUID, counterpartyUserID uuid.UUID,
) (*PersonalDebt, error) {
	q := `SELECT ` + debtColumns + ` FROM personal_debts
		WHERE source_transaction_id = $1
		  AND user_id = $2
		LIMIT 1`
	d, err := scanDebt(tx.QueryRow(ctx, q, sourceTxID, counterpartyUserID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // no matching row, fine
	}
	return d, err
}

// DebtsForTransaction returns all debts originating from a transaction
// (one per debtor for the splitter side) — used by the FE to show
// split breakdown on a transaction detail page.
func (s *Store) DebtsForTransaction(ctx context.Context, userID, txID uuid.UUID) ([]PersonalDebt, error) {
	q := `SELECT ` + debtColumns + ` FROM personal_debts
		WHERE user_id = $1 AND source_transaction_id = $2
		ORDER BY created_at`
	rows, err := s.db.Query(ctx, q, userID, txID)
	if err != nil {
		return nil, fmt.Errorf("debts for tx: %w", err)
	}
	defer rows.Close()
	out := make([]PersonalDebt, 0)
	for rows.Next() {
		d, err := scanDebt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// SumDebtsForTransaction returns the total amount of open lent debts
// originating from a transaction. Used by reports for "my share"
// calculation.
func (s *Store) SumDebtsForTransaction(ctx context.Context, userID, txID uuid.UUID) (float64, error) {
	var total float64
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount), 0) FROM personal_debts
		WHERE user_id = $1 AND source_transaction_id = $2
		  AND direction = 'owed_to_me' AND status != 'cancelled'`,
		userID, txID).Scan(&total)
	return total, err
}
