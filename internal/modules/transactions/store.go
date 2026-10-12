package transactions

import (
	"context"
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
	ErrTxNotFound         = errors.New("transaction not found")
	ErrSourcePTNotFound   = errors.New("source project transaction not found")
	ErrSourcePTNotForUser = errors.New("caller is not a member of the source project")
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
	to_char(date, 'YYYY-MM-DD') AS date, description, note, transfer_group_id,
	source_personal_debt_id, source_project_transaction_id, project_id,
	created_at, updated_at`

// txColumnsT — txColumns qualified with the alias t.
const txColumnsT = `t.id, t.user_id, t.account_id, t.type, t.amount, t.category_id,
	to_char(t.date, 'YYYY-MM-DD') AS date, t.description, t.note, t.transfer_group_id,
	t.source_personal_debt_id, t.source_project_transaction_id, t.project_id,
	t.created_at, t.updated_at`

func scanTx(row pgx.Row) (*Transaction, error) {
	var t Transaction
	err := row.Scan(
		&t.ID, &t.UserID, &t.AccountID, &t.Type, &t.Amount, &t.CategoryID,
		&t.Date, &t.Description, &t.Note, &t.TransferGroupID,
		&t.SourcePersonalDebtID, &t.SourceProjectTransactionID, &t.ProjectID,
		&t.CreatedAt, &t.UpdatedAt,
	)
	return &t, err
}

// --- Read ---

// txVisible is the read-visibility rule for transactions (spec §14): the
// caller sees a row when they authored it (their book, incl. locked
// history on wallets they left) OR they hold an ACTIVE membership on the
// row's account (shared-wallet ledger, all authors). Backfilled owner
// rows make personal accounts behave exactly as the old `user_id = $1`.
func txVisible(txAlias, userParam string) string {
	return "(" + txAlias + ".user_id = " + userParam + " OR " +
		shared.ActiveMembershipPredicate(txAlias+".account_id", userParam) + ")"
}

func (s *Store) GetByID(ctx context.Context, userID, id uuid.UUID) (*Transaction, error) {
	q := `SELECT ` + txColumns + ` FROM transactions t
		WHERE t.id = $1 AND ` + txVisible("t", "$2")
	t, err := scanTx(s.db.QueryRow(ctx, q, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTxNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	return t, nil
}

// IsActiveMember reports whether userID holds an active membership on
// accountID. Inlined here (not calling the accounts module) to avoid an
// import cycle — same reasoning as LookupSourcePTTx above.
func (s *Store) IsActiveMember(ctx context.Context, accountID, userID uuid.UUID) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx,
		`SELECT `+shared.ActiveMembershipPredicate("$1", "$2"),
		accountID, userID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("is member: %w", err)
	}
	return ok, nil
}

// GetByGroupID loads both rows of a transfer pair. No author filter —
// on shared wallets a member may operate on another member's transfer;
// the service authorizes via membership on both accounts.
func (s *Store) GetByGroupID(ctx context.Context, groupID uuid.UUID) ([]Transaction, error) {
	q := `SELECT ` + txColumns + ` FROM transactions
		WHERE transfer_group_id = $1
		ORDER BY type, account_id`
	rows, err := s.db.Query(ctx, q, groupID)
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

// List returns a page of rows (Service.List resolves them with
// buildDetails), the filtered row count and the money totals over the
// whole filtered set. Sort = date_desc by default.
func (s *Store) List(ctx context.Context, userID uuid.UUID, f ListFilter) ([]Transaction, int, Totals, error) {
	whereClauses := []string{txVisible("t", "$1")}
	args := []any{userID}

	if f.AccountID != nil {
		args = append(args, *f.AccountID)
		whereClauses = append(whereClauses, fmt.Sprintf("t.account_id = $%d", len(args)))
	}
	if f.CategoryID != nil {
		args = append(args, *f.CategoryID)
		if f.IncludeChildren {
			whereClauses = append(whereClauses, fmt.Sprintf(
				"t.category_id IN (SELECT id FROM categories WHERE id = $%[1]d OR parent_id = $%[1]d)", len(args)))
		} else {
			whereClauses = append(whereClauses, fmt.Sprintf("t.category_id = $%d", len(args)))
		}
	}
	if f.Uncategorized {
		whereClauses = append(whereClauses, "t.category_id IS NULL")
	}
	// Search + tags use subqueries, not joins, so the COUNT query below can
	// share the same WHERE.
	if q := strings.TrimSpace(f.Q); q != "" {
		args = append(args, "%"+shared.EscapeLike(q)+"%")
		whereClauses = append(whereClauses, fmt.Sprintf(`(t.description ILIKE $%[1]d OR t.note ILIKE $%[1]d
			OR EXISTS (SELECT 1 FROM categories qc LEFT JOIN categories qp ON qp.id = qc.parent_id
			           WHERE qc.id = t.category_id AND (qc.name ILIKE $%[1]d OR qp.name ILIKE $%[1]d))
			OR EXISTS (SELECT 1 FROM accounts qa WHERE qa.id = t.account_id AND qa.name ILIKE $%[1]d))`, len(args)))
	}
	if len(f.TagIDs) > 0 {
		args = append(args, f.TagIDs)
		whereClauses = append(whereClauses, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM transaction_tags tt WHERE tt.transaction_id = t.id AND tt.tag_id = ANY($%d))", len(args)))
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
	if f.NoWallet {
		whereClauses = append(whereClauses, "t.account_id IS NULL")
	}

	where := strings.Join(whereClauses, " AND ")

	// Row count + money totals over the whole filtered set, one pass.
	// Share basis (spec 12 §4.5), same as the summary.
	var (
		total  int
		totals Totals
	)
	share := shared.ShareAmountExpr("t")
	err := s.db.QueryRow(ctx, `SELECT COUNT(*),
		COALESCE(SUM(`+share+`) FILTER (WHERE t.type = 'income'), 0),
		COALESCE(SUM(`+share+`) FILTER (WHERE t.type = 'expense'), 0),
		COUNT(*) FILTER (WHERE t.type <> 'transfer')
		FROM transactions t WHERE `+where, args...).Scan(&total, &totals.Income, &totals.Expense, &totals.Count)
	if err != nil {
		return nil, 0, Totals{}, fmt.Errorf("count: %w", err)
	}
	totals.Net = totals.Income - totals.Expense

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

	q := fmt.Sprintf(`SELECT %s FROM transactions t
		WHERE %s
		ORDER BY %s, t.id
		LIMIT $%d OFFSET $%d`, txColumnsT, where, orderBy, limitIdx, offsetIdx)

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, Totals{}, fmt.Errorf("list: %w", err)
	}
	defer rows.Close()

	out := make([]Transaction, 0, f.PerPage)
	for rows.Next() {
		t, err := scanTx(rows)
		if err != nil {
			return nil, 0, Totals{}, fmt.Errorf("scan: %w", err)
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, Totals{}, err
	}
	return out, total, totals, nil
}

// EventSplitsTotal — for an event bill (shared.EventBillPredicate), Σ the
// other members' splits on its board row; 0 for any other transaction.
func (s *Store) EventSplitsTotal(ctx context.Context, txID uuid.UUID) (float64, error) {
	var total float64
	err := s.db.QueryRow(ctx, `
		SELECT CASE WHEN `+shared.EventBillPredicate("t")+` THEN COALESCE((
			SELECT SUM(c.amount) FROM project_transactions c
			WHERE c.parent_project_transaction_id = t.source_project_transaction_id), 0)
		ELSE 0 END
		FROM transactions t WHERE t.id = $1`, txID).Scan(&total)
	return total, err
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
//
// This doubles as the WRITE authorization gate: the caller must hold an
// ACTIVE membership on the account (spec §14/6 — "writes to an account
// require active membership"). Personal accounts pass via the backfilled
// owner row — no special case.
func (s *Store) GetAccountBalanceTx(ctx context.Context, tx pgx.Tx, userID, accountID uuid.UUID) (balance float64, currency string, err error) {
	err = tx.QueryRow(ctx,
		`SELECT a.balance, a.currency FROM accounts a
		 WHERE a.id = $1 AND `+shared.ActiveMembershipPredicate("a.id", "$2"),
		accountID, userID).Scan(&balance, &currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ErrAccountForbidden
	}
	return
}

// ApplyBalanceDeltaTx adjusts accounts.balance by `delta` (positive or
// negative). Caller has already locked the row AND verified membership
// (GetAccountBalanceTx) — on a shared wallet the actor is not necessarily
// the account's owner, so the WHERE matches by id only and the actor is
// stamped as updater.
func (s *Store) ApplyBalanceDeltaTx(ctx context.Context, tx pgx.Tx, userID, accountID uuid.UUID, delta float64) (newBalance float64, err error) {
	err = tx.QueryRow(ctx, `
		UPDATE accounts SET balance = balance + $1, updated_by_user_id = $2
		WHERE id = $3
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
		 description, created_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7::date, $8, $9, $10, $11, $12, $13, $2)
		RETURNING ` + txColumns
	created, err := scanTx(tx.QueryRow(ctx, q,
		t.ID, t.UserID, t.AccountID, string(t.Type), t.Amount, t.CategoryID,
		t.Date, shared.CleanText(t.Note), t.TransferGroupID,
		t.SourcePersonalDebtID, t.SourceProjectTransactionID, t.ProjectID, shared.CleanText(t.Description)))
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
	description *string, descriptionChange bool,
	note *string, noteChange bool,
	accountID *uuid.UUID, accountIDChange bool,
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
	if descriptionChange {
		args = append(args, description)
		q += fmt.Sprintf(", description = $%d", len(args))
	}
	if noteChange {
		args = append(args, note)
		q += fmt.Sprintf(", note = $%d", len(args))
	}
	if accountIDChange {
		args = append(args, accountID)
		q += fmt.Sprintf(", account_id = $%d", len(args))
	}
	args = append(args, id)
	// WHERE by id only — on shared wallets the actor may edit another
	// member's row (spec §14/2 rule 2); service-level authz decides who
	// may reach this. $1 (the actor) is stamped into updated_by_user_id
	// for the audit trail.
	q += fmt.Sprintf(" WHERE id = $%d RETURNING ", len(args)) + txColumns

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
// already reversed the balance and authorized the actor — members may
// delete other members' rows on shared wallets, so no author filter here.
func (s *Store) DeleteRowTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	tag, err := tx.Exec(ctx,
		`DELETE FROM transactions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete tx: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrTxNotFound
	}
	return nil
}

// ReplaceTagsTx makes the caller's tags on txID exactly tagIDs (other
// users' tags on a shared-wallet row are theirs and stay). Every id must be
// one of the caller's tags → ErrTagNotFound.
func (s *Store) ReplaceTagsTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID, tagIDs []uuid.UUID) error {
	if err := verifyTagsTx(ctx, tx, userID, tagIDs); err != nil {
		return err
	}
	if tagIDs == nil {
		tagIDs = []uuid.UUID{}
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM transaction_tags tt USING tags t
		WHERE tt.tag_id = t.id AND tt.transaction_id = $1 AND t.user_id = $2
		  AND NOT (tt.tag_id = ANY($3))`, txID, userID, tagIDs); err != nil {
		return fmt.Errorf("detach tags: %w", err)
	}
	return attachTagsTx(ctx, tx, userID, txID, tagIDs)
}

// AttachTagsTx adds the caller's tags to txID (existing pairs stay).
func (s *Store) AttachTagsTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID, tagIDs []uuid.UUID) error {
	if len(tagIDs) == 0 {
		return nil
	}
	if err := verifyTagsTx(ctx, tx, userID, tagIDs); err != nil {
		return err
	}
	return attachTagsTx(ctx, tx, userID, txID, tagIDs)
}

func verifyTagsTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, tagIDs []uuid.UUID) error {
	ids := uniqueUUIDs(tagIDs)
	if len(ids) == 0 {
		return nil
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM tags WHERE user_id = $1 AND id = ANY($2)`,
		userID, ids).Scan(&n); err != nil {
		return fmt.Errorf("tag ownership: %w", err)
	}
	if n != len(ids) {
		return ErrTagNotFound
	}
	return nil
}

func attachTagsTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID, tagIDs []uuid.UUID) error {
	ids := uniqueUUIDs(tagIDs)
	if len(ids) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO transaction_tags (transaction_id, tag_id, created_by_user_id)
		SELECT $1, unnest($2::uuid[]), $3
		ON CONFLICT (transaction_id, tag_id) DO NOTHING`, txID, ids, userID); err != nil {
		return fmt.Errorf("attach tags: %w", err)
	}
	return nil
}

func uniqueUUIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
