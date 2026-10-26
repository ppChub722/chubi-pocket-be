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
	repay_contact_id, repay_person_name, paid_by_amount,
	created_at, updated_at`

const txColumnsT = `t.id, t.user_id, t.account_id, t.type, t.amount, t.category_id,
	to_char(t.date, 'YYYY-MM-DD') AS date, t.description, t.note, t.transfer_group_id,
	t.source_personal_debt_id, t.source_project_transaction_id, t.project_id,
	t.repay_contact_id, t.repay_person_name, t.paid_by_amount,
	t.created_at, t.updated_at`

func scanTx(row pgx.Row) (*Transaction, error) {
	var t Transaction
	err := row.Scan(
		&t.ID, &t.UserID, &t.AccountID, &t.Type, &t.Amount, &t.CategoryID,
		&t.Date, &t.Description, &t.Note, &t.TransferGroupID,
		&t.SourcePersonalDebtID, &t.SourceProjectTransactionID, &t.ProjectID,
		&t.RepayContactID, &t.RepayPersonName, &t.PaidByAmount,
		&t.CreatedAt, &t.UpdatedAt,
	)
	return &t, err
}

// reportableRow — the row's category counts in reports (shared.ReportableCategoryPredicate; no
// category counts).
const reportableRow = `COALESCE((SELECT rc.include_in_report FROM categories rc WHERE rc.id = t.category_id), TRUE)`

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

func (s *Store) CountByCategory(ctx context.Context, categoryID uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM transactions WHERE category_id = $1`, categoryID).Scan(&n)
	return n, err
}

func (s *Store) List(ctx context.Context, userID uuid.UUID, f ListFilter) ([]Transaction, int, Totals, []DailyPoint, error) {
	whereClauses := []string{txVisible("t", "$1")}
	args := []any{userID}

	walletIDs := append([]uuid.UUID(nil), f.AccountIDs...)
	if f.AccountID != nil {
		walletIDs = append(walletIDs, *f.AccountID)
	}
	var walletOr []string
	if len(walletIDs) > 0 {
		args = append(args, walletIDs)
		walletOr = append(walletOr, fmt.Sprintf("t.account_id = ANY($%d)", len(args)))
	}
	if f.NoWallet {
		walletOr = append(walletOr, "t.account_id IS NULL")
	}
	if len(walletOr) > 0 {
		whereClauses = append(whereClauses, "("+strings.Join(walletOr, " OR ")+")")
	}
	if len(walletIDs) == 0 && !f.IncludeArchived {
		whereClauses = append(whereClauses,
			"NOT EXISTS (SELECT 1 FROM accounts xa WHERE xa.id = t.account_id AND xa.status = 'archived')")
	}
	categoryIDs := append([]uuid.UUID(nil), f.CategoryIDs...)
	if f.CategoryID != nil {
		categoryIDs = append(categoryIDs, *f.CategoryID)
	}
	var categoryOr []string
	if len(categoryIDs) > 0 {
		args = append(args, categoryIDs)
		if f.IncludeChildren {
			categoryOr = append(categoryOr, fmt.Sprintf(
				"t.category_id IN (SELECT id FROM categories WHERE id = ANY($%[1]d) OR parent_id = ANY($%[1]d))", len(args)))
		} else {
			categoryOr = append(categoryOr, fmt.Sprintf("t.category_id = ANY($%d)", len(args)))
		}
	}
	if f.Uncategorized {
		categoryOr = append(categoryOr, "t.category_id IS NULL")
	}
	if len(categoryOr) > 0 {
		whereClauses = append(whereClauses, "("+strings.Join(categoryOr, " OR ")+")")
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		args = append(args, "%"+shared.EscapeLike(q)+"%")
		whereClauses = append(whereClauses, fmt.Sprintf(`(t.description ILIKE $%[1]d OR t.note ILIKE $%[1]d
			OR EXISTS (SELECT 1 FROM categories qc LEFT JOIN categories qp ON qp.id = qc.parent_id
			           WHERE qc.id = t.category_id AND (qc.name ILIKE $%[1]d OR qp.name ILIKE $%[1]d))
			OR EXISTS (SELECT 1 FROM accounts qa WHERE qa.id = t.account_id AND qa.name ILIKE $%[1]d))`, len(args)))
	}
	var tagOr []string
	if len(f.TagIDs) > 0 {
		args = append(args, f.TagIDs)
		tagOr = append(tagOr, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM transaction_tags tt WHERE tt.transaction_id = t.id AND tt.tag_id = ANY($%d))", len(args)))
	}
	if f.NoTags {
		tagOr = append(tagOr, "NOT EXISTS (SELECT 1 FROM transaction_tags nt WHERE nt.transaction_id = t.id)")
	}
	if len(tagOr) > 0 {
		whereClauses = append(whereClauses, "("+strings.Join(tagOr, " OR ")+")")
	}
	var typeNames []string
	if f.Type != nil {
		typeNames = append(typeNames, string(*f.Type))
	}
	for _, ty := range f.Types {
		typeNames = append(typeNames, string(ty))
	}
	if len(typeNames) > 0 {
		args = append(args, typeNames)
		whereClauses = append(whereClauses, fmt.Sprintf("t.type = ANY($%d)", len(args)))
	}
	if f.From != nil {
		args = append(args, *f.From)
		whereClauses = append(whereClauses, fmt.Sprintf("t.date >= $%d::date", len(args)))
	}
	if f.To != nil {
		args = append(args, *f.To)
		whereClauses = append(whereClauses, fmt.Sprintf("t.date <= $%d::date", len(args)))
	}
	if f.AmountMin != nil {
		args = append(args, *f.AmountMin)
		whereClauses = append(whereClauses, fmt.Sprintf("t.amount >= $%d", len(args)))
	}
	if f.AmountMax != nil {
		args = append(args, *f.AmountMax)
		whereClauses = append(whereClauses, fmt.Sprintf("t.amount <= $%d", len(args)))
	}
	if f.CanJoinEvent {
		whereClauses = append(whereClauses, `t.user_id = $1 AND t.type IN ('expense', 'income')
			AND t.project_id IS NULL AND t.source_personal_debt_id IS NULL AND t.source_project_transaction_id IS NULL
			AND t.paid_by_amount IS NULL
			AND NOT EXISTS (SELECT 1 FROM categories jc WHERE jc.id = t.category_id AND jc.is_system)
			AND NOT EXISTS (SELECT 1 FROM personal_debts jd WHERE jd.source_transaction_id = t.id AND jd.user_id = t.user_id AND jd.paid)`)
	}
	if f.CounterpartyKey != "" {
		args = append(args, f.CounterpartyKey)
		// The person's money: their debts' own transactions, the bills split with them, the bills
		// they paid / received for me, the transactions linked to their debts, and the repayments.
		whereClauses = append(whereClauses, fmt.Sprintf(`t.user_id = $1 AND (EXISTS (SELECT 1 FROM personal_debts kd
			WHERE kd.user_id = t.user_id AND %[1]s = $%[3]d
			  AND (kd.id = t.source_personal_debt_id
			       OR t.id IN (kd.source_transaction_id, kd.paid_for_transaction_id, kd.linked_transaction_id)))
			OR (t.repay_person_name IS NOT NULL AND %[2]s = $%[3]d))`, shared.DebtKeySQL("kd"), shared.RepayKeySQL("t"), len(args)))
	}

	where := strings.Join(whereClauses, " AND ")

	var (
		total  int
		totals Totals
	)
	share := shared.ShareAmountExpr("t")
	// The list shows future-dated rows, the totals count them from their day. Like reports, the
	// income / expense totals leave out categories kept out of reports (Debt Paid / Received, …).
	due := `t.date <= ` + shared.TodaySQL("$1")
	counted := due + ` AND ` + reportableRow
	err := s.db.QueryRow(ctx, `SELECT COUNT(*),
		COALESCE(SUM(`+share+`) FILTER (WHERE t.type = 'income' AND `+counted+`), 0),
		COALESCE(SUM(`+share+`) FILTER (WHERE t.type = 'expense' AND `+counted+`), 0),
		COUNT(*) FILTER (WHERE t.type <> 'transfer' AND `+due+`)
		FROM transactions t WHERE `+where, args...).Scan(&total, &totals.Income, &totals.Expense, &totals.Count)
	if err != nil {
		return nil, 0, Totals{}, nil, fmt.Errorf("count: %w", err)
	}
	totals.Net = totals.Income - totals.Expense
	var daily []DailyPoint
	if !f.RangeFrom.IsZero() {
		if daily, err = s.dailySeries(ctx, where, args, f.RangeFrom, f.RangeTo); err != nil {
			return nil, 0, Totals{}, nil, err
		}
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

	q := fmt.Sprintf(`SELECT %s FROM transactions t
		WHERE %s
		ORDER BY %s, t.id
		LIMIT $%d OFFSET $%d`, txColumnsT, where, orderBy, limitIdx, offsetIdx)

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, Totals{}, nil, fmt.Errorf("list: %w", err)
	}
	defer rows.Close()

	out := make([]Transaction, 0, f.PerPage)
	for rows.Next() {
		t, err := scanTx(rows)
		if err != nil {
			return nil, 0, Totals{}, nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, Totals{}, nil, err
	}
	return out, total, totals, daily, nil
}

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

func (s *Store) LockAccountsForUpdate(ctx context.Context, tx pgx.Tx, ids []uuid.UUID) error {
	for _, id := range ids {
		if _, err := tx.Exec(ctx,
			`SELECT 1 FROM accounts WHERE id = $1 FOR UPDATE`, id); err != nil {
			return fmt.Errorf("lock account %s: %w", id, err)
		}
	}
	return nil
}

// GetAccountBalanceTx reads the current cached balance under lock (an archived wallet is read-only:
// ErrAccountArchived).
func (s *Store) GetAccountBalanceTx(ctx context.Context, tx pgx.Tx, userID, accountID uuid.UUID) (balance float64, currency string, err error) {
	var status string
	err = tx.QueryRow(ctx,
		`SELECT a.balance, a.currency, a.status FROM accounts a
		 WHERE a.id = $1 AND `+shared.ActiveMembershipPredicate("a.id", "$2"),
		accountID, userID).Scan(&balance, &currency, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ErrAccountForbidden
	}
	if err == nil && status == "archived" {
		return 0, "", ErrAccountArchived
	}
	return
}

func (s *Store) accountArchived(ctx context.Context, id uuid.UUID) (bool, error) {
	var archived bool
	err := s.db.QueryRow(ctx, `SELECT status = 'archived' FROM accounts WHERE id = $1`, id).Scan(&archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return archived, err
}

func (s *Store) ApplyBalanceDeltaTx(ctx context.Context, tx pgx.Tx, userID, accountID uuid.UUID, delta float64) (newBalance float64, err error) {
	err = tx.QueryRow(ctx, `
		UPDATE accounts SET balance = balance + $1, updated_by_user_id = $2
		WHERE id = $3
		RETURNING balance`, delta, userID, accountID).Scan(&newBalance)
	return
}

func (s *Store) InsertRowTx(ctx context.Context, tx pgx.Tx, t *Transaction) (*Transaction, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}
	t.ID = id

	q := `INSERT INTO transactions
		(id, user_id, account_id, type, amount, category_id, date, note,
		 transfer_group_id, source_personal_debt_id, source_project_transaction_id, project_id,
		 description, repay_contact_id, repay_person_name, paid_by_amount, created_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7::date, $8, $9, $10, $11, $12, $13, $14, $15, $16, $2)
		RETURNING ` + txColumns
	created, err := scanTx(tx.QueryRow(ctx, q,
		t.ID, t.UserID, t.AccountID, string(t.Type), t.Amount, t.CategoryID,
		t.Date, shared.CleanText(t.Note), t.TransferGroupID,
		t.SourcePersonalDebtID, t.SourceProjectTransactionID, t.ProjectID, shared.CleanText(t.Description),
		t.RepayContactID, t.RepayPersonName, t.PaidByAmount))
	if err != nil {
		return nil, fmt.Errorf("insert tx: %w", err)
	}
	return created, nil
}

func (s *Store) UpdateRowTx(
	ctx context.Context, tx pgx.Tx, userID, id uuid.UUID,
	amount *float64, date *string,
	categoryID *uuid.UUID, categoryChange bool,
	description *string, descriptionChange bool,
	note *string, noteChange bool,
	accountID *uuid.UUID, accountIDChange bool,
	paidBy *float64, paidByChange bool,
) (*Transaction, error) {
	q := `UPDATE transactions SET updated_by_user_id = $1`
	args := []any{userID}
	if paidByChange {
		args = append(args, paidBy)
		q += fmt.Sprintf(", paid_by_amount = $%d", len(args))
	}
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
