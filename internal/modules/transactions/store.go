package transactions

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
	ErrTxNotFound          = errors.New("transaction not found")
	ErrSourcePTNotFound    = errors.New("source project transaction not found")
	ErrSourcePTNotForUser  = errors.New("caller is not a member of the source project")
)

// LookupSourcePTTx resolves a project_transaction by id and confirms the
// caller is an active member of its project. Returns the project_id used to
// stamp the personal-mirror row's project_id column.
//
// Inlined here (instead of calling the projects module) to avoid an import
// cycle: projects already depends on transactions semantics being stable.
func (s *Store) LookupSourcePTTx(
	ctx context.Context, tx pgx.Tx, ptID, userID uuid.UUID,
) (uuid.UUID, error) {
	var projectID uuid.UUID
	err := tx.QueryRow(ctx,
		`SELECT project_id FROM project_transactions WHERE id = $1`, ptID,
	).Scan(&projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrSourcePTNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("source PT lookup: %w", err)
	}
	var n int
	if err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM project_members
		WHERE project_id = $1 AND user_id = $2 AND status = 'active'`,
		projectID, userID,
	).Scan(&n); err != nil {
		return uuid.Nil, fmt.Errorf("source PT membership: %w", err)
	}
	if n == 0 {
		return uuid.Nil, ErrSourcePTNotForUser
	}
	return projectID, nil
}

const txColumns = `id, user_id, account_id, type, amount, category_id,
	to_char(date, 'YYYY-MM-DD') AS date, note, transfer_group_id,
	source_personal_debt_id, source_project_transaction_id, project_id,
	created_at, updated_at`

func scanTx(row pgx.Row) (*Transaction, error) {
	var t Transaction
	err := row.Scan(
		&t.ID, &t.UserID, &t.AccountID, &t.Type, &t.Amount, &t.CategoryID,
		&t.Date, &t.Note, &t.TransferGroupID,
		&t.SourcePersonalDebtID, &t.SourceProjectTransactionID, &t.ProjectID,
		&t.CreatedAt, &t.UpdatedAt,
	)
	return &t, err
}

// --- Read ---

func (s *Store) GetByID(ctx context.Context, userID, id uuid.UUID) (*Transaction, error) {
	q := `SELECT ` + txColumns + ` FROM transactions WHERE id = $1 AND user_id = $2`
	t, err := scanTx(s.db.QueryRow(ctx, q, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTxNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return t, nil
}

func (s *Store) GetByGroupID(ctx context.Context, userID, groupID uuid.UUID) ([]Transaction, error) {
	q := `SELECT ` + txColumns + ` FROM transactions
		WHERE user_id = $1 AND transfer_group_id = $2
		ORDER BY type, account_id`
	rows, err := s.db.Query(ctx, q, userID, groupID)
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	defer rows.Close()
	out := make([]Transaction, 0, 2)
	for rows.Next() {
		t, err := scanTx(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// CountByCategory is called by categories.Store.CountTransactions to decide
// archive-vs-hard-delete. Phase 1a.3 wires this up after the transactions
// table exists.
func (s *Store) CountByCategory(ctx context.Context, categoryID uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM transactions WHERE category_id = $1`, categoryID).Scan(&n)
	return n, err
}

// List returns a page of TransactionDetail rows joined with embedded
// account + category refs. Sort = date_desc by default.
func (s *Store) List(ctx context.Context, userID uuid.UUID, f ListFilter) ([]TransactionDetail, int, error) {
	whereClauses := []string{"t.user_id = $1"}
	args := []any{userID}

	if f.AccountID != nil {
		args = append(args, *f.AccountID)
		whereClauses = append(whereClauses, fmt.Sprintf("t.account_id = $%d", len(args)))
	}
	if f.CategoryID != nil {
		args = append(args, *f.CategoryID)
		whereClauses = append(whereClauses, fmt.Sprintf("t.category_id = $%d", len(args)))
	}
	if f.Type != nil {
		args = append(args, string(*f.Type))
		whereClauses = append(whereClauses, fmt.Sprintf("t.type = $%d", len(args)))
	}
	if f.From != nil {
		args = append(args, *f.From)
		whereClauses = append(whereClauses, fmt.Sprintf("t.date >= $%d::date", len(args)))
	}
	if f.To != nil {
		args = append(args, *f.To)
		whereClauses = append(whereClauses, fmt.Sprintf("t.date <= $%d::date", len(args)))
	}

	where := strings.Join(whereClauses, " AND ")

	// total count
	var total int
	err := s.db.QueryRow(ctx,
		"SELECT COUNT(*) FROM transactions t WHERE "+where, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("count: %w", err)
	}

	orderBy := "t.date DESC, t.created_at DESC"
	switch f.Sort {
	case "date_asc":
		orderBy = "t.date ASC, t.created_at ASC"
	case "amount_desc":
		orderBy = "t.amount DESC, t.created_at DESC"
	case "amount_asc":
		orderBy = "t.amount ASC, t.created_at ASC"
	}

	args = append(args, f.PerPage)
	limitIdx := len(args)
	args = append(args, (f.Page-1)*f.PerPage)
	offsetIdx := len(args)

	q := fmt.Sprintf(`
		SELECT t.id, t.user_id, t.account_id, t.type, t.amount, t.category_id,
		       to_char(t.date, 'YYYY-MM-DD') AS date, t.note, t.transfer_group_id,
		       t.source_personal_debt_id, t.source_project_transaction_id, t.project_id,
		       t.created_at, t.updated_at,
		       a.id, a.name,
		       c.id, c.name
		FROM transactions t
		JOIN accounts a   ON a.id = t.account_id
		LEFT JOIN categories c ON c.id = t.category_id
		WHERE %s
		ORDER BY %s
		LIMIT $%d OFFSET $%d`, where, orderBy, limitIdx, offsetIdx)

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list: %w", err)
	}
	defer rows.Close()

	out := make([]TransactionDetail, 0, f.PerPage)
	for rows.Next() {
		var (
			d        TransactionDetail
			accID    uuid.UUID
			accName  string
			catID    *uuid.UUID
			catName  *string
		)
		err := rows.Scan(
			&d.ID, &d.UserID, &d.AccountID, &d.Type, &d.Amount, &d.CategoryID,
			&d.Date, &d.Note, &d.TransferGroupID,
			&d.SourcePersonalDebtID, &d.SourceProjectTransactionID, &d.ProjectID,
			&d.CreatedAt, &d.UpdatedAt,
			&accID, &accName,
			&catID, &catName,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("scan: %w", err)
		}
		d.Account = &EmbeddedRef{ID: accID, Name: accName}
		if catID != nil && catName != nil {
			d.Category = &EmbeddedRef{ID: *catID, Name: *catName}
		}
		// Init empty slice so JSON serializes as [] not null. Filled in
		// the batch-tag query below.
		d.Tags = []EmbeddedTag{}
		d.IsResolve = d.SourcePersonalDebtID != nil
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	// Batch-fetch tags for the page in a single query, then patch onto
	// each row by id. Avoids N+1 against the page; with per_page <= 100
	// this stays a small in-memory join.
	if err := s.fillTagsForList(ctx, userID, out); err != nil {
		return nil, 0, fmt.Errorf("fill tags: %w", err)
	}
	if err := s.fillHasSplitsForList(ctx, out); err != nil {
		return nil, 0, fmt.Errorf("fill has_splits: %w", err)
	}
	return out, total, nil
}

// fillHasSplitsForList flags every row that has any debts attached
// (i.e. the splitter's side of a split-bill transaction). Single batch
// query against the personal_debts.source_transaction_id index.
func (s *Store) fillHasSplitsForList(ctx context.Context, details []TransactionDetail) error {
	if len(details) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(details))
	for i := range details {
		ids[i] = details[i].ID
	}
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT source_transaction_id
		FROM personal_debts
		WHERE source_transaction_id = ANY($1)`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	hasByID := make(map[uuid.UUID]struct{}, len(details))
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return err
		}
		hasByID[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range details {
		if _, ok := hasByID[details[i].ID]; ok {
			details[i].HasSplits = true
		}
	}
	return nil
}

// fillTagsForList populates `d.Tags` for every row in `details` via one
// query against the transaction_tags junction. No-op when the slice is
// empty.
func (s *Store) fillTagsForList(ctx context.Context, userID uuid.UUID, details []TransactionDetail) error {
	if len(details) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(details))
	for i := range details {
		ids[i] = details[i].ID
	}
	rows, err := s.db.Query(ctx, `
		SELECT tt.transaction_id, t.id, t.name, t.icon_code
		FROM tags t
		JOIN transaction_tags tt ON tt.tag_id = t.id
		WHERE tt.transaction_id = ANY($1) AND t.user_id = $2
		ORDER BY tt.transaction_id, LOWER(t.name)`,
		ids, userID)
	if err != nil {
		return err
	}
	defer rows.Close()

	tagsByTx := make(map[uuid.UUID][]EmbeddedTag, len(details))
	for rows.Next() {
		var (
			txID      uuid.UUID
			tag       EmbeddedTag
			iconBytes []byte
		)
		if err := rows.Scan(&txID, &tag.ID, &tag.Name, &iconBytes); err != nil {
			return err
		}
		if iconBytes != nil {
			tag.IconCode = new(shared.IconCode)
			if err := json.Unmarshal(iconBytes, tag.IconCode); err != nil {
				return fmt.Errorf("unmarshal tag icon_code: %w", err)
			}
		}
		tagsByTx[txID] = append(tagsByTx[txID], tag)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for i := range details {
		if t := tagsByTx[details[i].ID]; t != nil {
			details[i].Tags = t
		}
	}
	return nil
}

// --- Tx-aware writes (called via balance helper) ---

// LockAccountsForUpdate runs SELECT 1 ... FOR UPDATE on each account_id in
// `ids`. Caller MUST sort `ids` (lowest UUID first) before calling — that's
// the deadlock-prevention rule.
func (s *Store) LockAccountsForUpdate(ctx context.Context, tx pgx.Tx, ids []uuid.UUID) error {
	for _, id := range ids {
		if _, err := tx.Exec(ctx,
			`SELECT 1 FROM accounts WHERE id = $1 FOR UPDATE`, id); err != nil {
			return fmt.Errorf("lock account %s: %w", id, err)
		}
	}
	return nil
}

// GetAccountBalanceTx reads the current cached balance under lock. Returns
// also the currency for currency-mismatch checks on transfers.
func (s *Store) GetAccountBalanceTx(ctx context.Context, tx pgx.Tx, userID, accountID uuid.UUID) (balance float64, currency string, err error) {
	err = tx.QueryRow(ctx,
		`SELECT balance, currency FROM accounts WHERE id = $1 AND user_id = $2`,
		accountID, userID).Scan(&balance, &currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ErrAccountForbidden
	}
	return
}

// ApplyBalanceDeltaTx adjusts accounts.balance by `delta` (positive or
// negative). Caller has already locked the row.
func (s *Store) ApplyBalanceDeltaTx(ctx context.Context, tx pgx.Tx, userID, accountID uuid.UUID, delta float64) (newBalance float64, err error) {
	err = tx.QueryRow(ctx, `
		UPDATE accounts SET balance = balance + $1, updated_by_user_id = $2
		WHERE id = $3 AND user_id = $2
		RETURNING balance`, delta, userID, accountID).Scan(&newBalance)
	return
}

// InsertRowTx inserts a single transactions row. Caller is responsible for
// the corresponding balance update.
func (s *Store) InsertRowTx(ctx context.Context, tx pgx.Tx, t *Transaction) (*Transaction, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}
	t.ID = id

	q := `INSERT INTO transactions
		(id, user_id, account_id, type, amount, category_id, date, note,
		 transfer_group_id, source_personal_debt_id, source_project_transaction_id, project_id,
		 created_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7::date, $8, $9, $10, $11, $12, $2)
		RETURNING ` + txColumns
	created, err := scanTx(tx.QueryRow(ctx, q,
		t.ID, t.UserID, t.AccountID, string(t.Type), t.Amount, t.CategoryID,
		t.Date, t.Note, t.TransferGroupID,
		t.SourcePersonalDebtID, t.SourceProjectTransactionID, t.ProjectID))
	if err != nil {
		return nil, fmt.Errorf("insert tx: %w", err)
	}
	return created, nil
}

// UpdateRowTx updates editable fields. Called by Service.Update for one of
// the editable subsets; balance delta is applied separately when amount
// changes.
func (s *Store) UpdateRowTx(
	ctx context.Context, tx pgx.Tx, userID, id uuid.UUID,
	amount *float64, date *string,
	categoryID *uuid.UUID, categoryChange bool,
	note *string, noteChange bool,
) (*Transaction, error) {
	q := `UPDATE transactions SET updated_by_user_id = $1`
	args := []any{userID}
	if amount != nil {
		args = append(args, *amount)
		q += fmt.Sprintf(", amount = $%d", len(args))
	}
	if date != nil {
		args = append(args, *date)
		q += fmt.Sprintf(", date = $%d::date", len(args))
	}
	if categoryChange {
		args = append(args, categoryID)
		q += fmt.Sprintf(", category_id = $%d", len(args))
	}
	if noteChange {
		args = append(args, note)
		q += fmt.Sprintf(", note = $%d", len(args))
	}
	args = append(args, id)
	q += fmt.Sprintf(" WHERE id = $%d AND user_id = $1 RETURNING ", len(args)) + txColumns

	t, err := scanTx(tx.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTxNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update tx: %w", err)
	}
	return t, nil
}


// DeleteRowTx removes a single transactions row by id. Caller (service)
// already reversed the balance.
func (s *Store) DeleteRowTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID) error {
	tag, err := tx.Exec(ctx,
		`DELETE FROM transactions WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("delete tx: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrTxNotFound
	}
	return nil
}
