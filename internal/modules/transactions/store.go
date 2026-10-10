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
	to_char(date, 'YYYY-MM-DD') AS date, description, note, transfer_group_id,
	source_personal_debt_id, source_project_transaction_id, project_id,
	created_at, updated_at`

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

// List returns a page of TransactionDetail rows joined with embedded
// account + category refs. Sort = date_desc by default.
func (s *Store) List(ctx context.Context, userID uuid.UUID, f ListFilter) ([]TransactionDetail, int, Totals, error) {
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
	// Search + tags use subqueries, not the SELECT's joins, so the COUNT
	// query below can share the same WHERE.
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

	q := fmt.Sprintf(`
		SELECT t.id, t.user_id, t.account_id, t.type, t.amount, t.category_id,
		       to_char(t.date, 'YYYY-MM-DD') AS date, t.description, t.note, t.transfer_group_id,
		       t.source_personal_debt_id, t.source_project_transaction_id, t.project_id,
		       t.created_at, t.updated_at,
		       a.id, a.name,
		       c.id, c.name
		FROM transactions t
		LEFT JOIN accounts a ON a.id = t.account_id
		LEFT JOIN categories c ON c.id = t.category_id
		WHERE %s
		ORDER BY %s
		LIMIT $%d OFFSET $%d`, where, orderBy, limitIdx, offsetIdx)

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, Totals{}, fmt.Errorf("list: %w", err)
	}
	defer rows.Close()

	out := make([]TransactionDetail, 0, f.PerPage)
	for rows.Next() {
		var (
			d       TransactionDetail
			accID   *uuid.UUID
			accName *string
			catID   *uuid.UUID
			catName *string
		)
		err := rows.Scan(
			&d.ID, &d.UserID, &d.AccountID, &d.Type, &d.Amount, &d.CategoryID,
			&d.Date, &d.Description, &d.Note, &d.TransferGroupID,
			&d.SourcePersonalDebtID, &d.SourceProjectTransactionID, &d.ProjectID,
			&d.CreatedAt, &d.UpdatedAt,
			&accID, &accName,
			&catID, &catName,
		)
		if err != nil {
			return nil, 0, Totals{}, fmt.Errorf("scan: %w", err)
		}
		if accID != nil && accName != nil {
			d.Account = &EmbeddedRef{ID: *accID, Name: *accName}
		}
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
		return nil, 0, Totals{}, err
	}

	// Batch-fetch tags for the page in a single query, then patch onto
	// each row by id. Avoids N+1 against the page; with per_page <= 100
	// this stays a small in-memory join.
	if err := s.fillTagsForList(ctx, userID, out); err != nil {
		return nil, 0, Totals{}, fmt.Errorf("fill tags: %w", err)
	}
	if err := s.fillHasSplitsForList(ctx, out); err != nil {
		return nil, 0, Totals{}, fmt.Errorf("fill has_splits: %w", err)
	}
	if err := s.fillMyShareForList(ctx, out); err != nil {
		return nil, 0, Totals{}, fmt.Errorf("fill my_share: %w", err)
	}
	if err := s.fillSplitFlagsForList(ctx, userID, out); err != nil {
		return nil, 0, Totals{}, fmt.Errorf("fill split flags: %w", err)
	}
	if err := s.fillSharedInfoForList(ctx, userID, out); err != nil {
		return nil, 0, Totals{}, fmt.Errorf("fill shared info: %w", err)
	}
	if err := s.fillProjectsForList(ctx, out); err != nil {
		return nil, 0, Totals{}, fmt.Errorf("fill projects: %w", err)
	}
	return out, total, totals, nil
}

// fillSharedInfoForList decorates rows that live on SHARED wallets
// (active member count > 1) with the pinned-contract fields: denormalized
// created_by author, category_render (the author's category, read-only
// for other members), is_locked (caller can no longer move this wallet's
// numbers — ex-member) and can_edit_category (caller is the row's author
// and still active). Rows on personal accounts are left untouched.
// Single batch query per page.
func (s *Store) fillSharedInfoForList(ctx context.Context, userID uuid.UUID, details []TransactionDetail) error {
	if len(details) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(details))
	for i := range details {
		ids[i] = details[i].ID
	}
	rows, err := s.db.Query(ctx, `
		SELECT t.id, t.user_id, u.display_name, u.icon_code,
		       c.name, c.icon_code,
		       (SELECT COUNT(*) FROM account_members am
		         WHERE am.account_id = t.account_id
		           AND am.joined_at IS NOT NULL AND am.left_at IS NULL) AS active_members,
		       `+shared.ActiveMembershipPredicate("t.account_id", "$2")+` AS caller_active
		FROM transactions t
		JOIN users u ON u.id = t.user_id
		LEFT JOIN categories c ON c.id = t.category_id
		WHERE t.id = ANY($1)`, ids, userID)
	if err != nil {
		return err
	}
	defer rows.Close()

	type sharedInfo struct {
		createdBy       *AuthorRef
		categoryRender  *CategoryRender
		isLocked        bool
		canEditCategory bool
		shared          bool
	}
	infoByID := make(map[uuid.UUID]sharedInfo, len(details))
	for rows.Next() {
		var (
			txID          uuid.UUID
			authorID      uuid.UUID
			authorName    string
			authorIcon    []byte
			catName       *string
			catIcon       []byte
			activeMembers int
			callerActive  bool
		)
		if err := rows.Scan(&txID, &authorID, &authorName, &authorIcon,
			&catName, &catIcon, &activeMembers, &callerActive); err != nil {
			return err
		}
		if activeMembers <= 1 {
			continue
		}
		info := sharedInfo{shared: true}
		info.createdBy = &AuthorRef{UserID: authorID, DisplayName: authorName}
		if authorIcon != nil {
			info.createdBy.IconCode = new(shared.IconCode)
			if err := json.Unmarshal(authorIcon, info.createdBy.IconCode); err != nil {
				return fmt.Errorf("unmarshal author icon_code: %w", err)
			}
		}
		if catName != nil {
			info.categoryRender = &CategoryRender{Name: *catName}
			if catIcon != nil {
				info.categoryRender.IconCode = new(shared.IconCode)
				if err := json.Unmarshal(catIcon, info.categoryRender.IconCode); err != nil {
					return fmt.Errorf("unmarshal category icon_code: %w", err)
				}
			}
		}
		info.isLocked = !callerActive
		info.canEditCategory = authorID == userID && callerActive
		infoByID[txID] = info
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range details {
		info, ok := infoByID[details[i].ID]
		if !ok || !info.shared {
			continue
		}
		details[i].CreatedBy = info.createdBy
		details[i].CategoryRender = info.categoryRender
		isLocked, canEdit := info.isLocked, info.canEditCategory
		details[i].IsLocked = &isLocked
		details[i].CanEditCategory = &canEdit
	}
	return nil
}

// fillSplitFlagsForList sets can_split / can_edit_splits / can_join_event
// for callerUserID (one batch query; rules on TransactionDetail).
func (s *Store) fillSplitFlagsForList(ctx context.Context, callerUserID uuid.UUID, details []TransactionDetail) error {
	if len(details) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(details))
	for i := range details {
		ids[i] = details[i].ID
	}
	rows, err := s.db.Query(ctx, `
		SELECT t.id,
		       t.type IN ('income', 'expense') AND t.source_personal_debt_id IS NULL
		         AND NOT COALESCE(c.is_system, FALSE),
		       `+shared.EventBillPredicate("t")+`,
		       EXISTS (SELECT 1 FROM personal_debts d
		               WHERE d.source_transaction_id = t.id AND d.user_id = t.user_id
		                 AND (d.settled_amount > 0.005 OR d.status = 'cancelled')),
		       t.user_id = $2
		FROM transactions t
		LEFT JOIN categories c ON c.id = t.category_id
		WHERE t.id = ANY($1)`, ids, callerUserID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type flags struct{ split, edit, join bool }
	byID := make(map[uuid.UUID]flags, len(details))
	for rows.Next() {
		var (
			id                               uuid.UUID
			billable, eventBill, closed, own bool
		)
		if err := rows.Scan(&id, &billable, &eventBill, &closed, &own); err != nil {
			return err
		}
		// An event bill's own splits stay on it when it moves (only the
		// board splits travel), so a repaid one doesn't block the move.
		byID[id] = flags{split: billable, edit: billable && own, join: billable && own && (eventBill || !closed)}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range details {
		f := byID[details[i].ID]
		details[i].CanSplit, details[i].CanEditSplits, details[i].CanJoinEvent = f.split, f.edit, f.join
	}
	return nil
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

// fillMyShareForList sets my_share on every expense / income row (one
// batch query, same fragment as the reports).
func (s *Store) fillMyShareForList(ctx context.Context, details []TransactionDetail) error {
	if len(details) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(details))
	for i := range details {
		ids[i] = details[i].ID
	}
	rows, err := s.db.Query(ctx, `SELECT t.id, `+shared.ShareAmountExpr("t")+`
		FROM transactions t WHERE t.id = ANY($1) AND t.type IN ('income', 'expense')`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	byID := make(map[uuid.UUID]float64, len(details))
	for rows.Next() {
		var (
			id    uuid.UUID
			share float64
		)
		if err := rows.Scan(&id, &share); err != nil {
			return err
		}
		byID[id] = share
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range details {
		if v, ok := byID[details[i].ID]; ok {
			details[i].MyShare = &v
		}
	}
	return nil
}

// fillHasSplitsForList counts the debts every row made (the splitter's side
// of a split-bill transaction) → split_count + has_splits. Single batch
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
		SELECT source_transaction_id, COUNT(*)
		FROM personal_debts
		WHERE source_transaction_id = ANY($1)
		GROUP BY source_transaction_id`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	countByID := make(map[uuid.UUID]int, len(details))
	for rows.Next() {
		var (
			id uuid.UUID
			n  int
		)
		if err := rows.Scan(&id, &n); err != nil {
			return err
		}
		countByID[id] = n
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range details {
		details[i].SplitCount = countByID[details[i].ID]
		details[i].HasSplits = details[i].SplitCount > 0
	}
	return nil
}

// fillProjectsForList embeds {id, name} for rows that carry a project_id.
// One query per page.
func (s *Store) fillProjectsForList(ctx context.Context, details []TransactionDetail) error {
	var ids []uuid.UUID
	for i := range details {
		if details[i].ProjectID != nil {
			ids = append(ids, *details[i].ProjectID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.db.Query(ctx, `SELECT id, name FROM projects WHERE id = ANY($1)`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	names := make(map[uuid.UUID]string, len(ids))
	for rows.Next() {
		var (
			id   uuid.UUID
			name string
		)
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		names[id] = name
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range details {
		if p := details[i].ProjectID; p != nil {
			if name, ok := names[*p]; ok {
				details[i].Project = &EmbeddedRef{ID: *p, Name: name}
			}
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

// SplitsFor — the debts transaction txID made in userID's book (GET
// /transactions/:id). A shared-wallet member who isn't the author gets
// none: the debts are the author's.
func (s *Store) SplitsFor(ctx context.Context, userID, txID uuid.UUID) ([]SplitRef, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, counterparty_person_name, counterparty_contact_id, direction,
		       amount, settled_amount, status
		FROM personal_debts
		WHERE source_transaction_id = $1 AND user_id = $2
		ORDER BY created_at, id`, txID, userID)
	if err != nil {
		return nil, fmt.Errorf("splits: %w", err)
	}
	defer rows.Close()
	out := []SplitRef{}
	for rows.Next() {
		var r SplitRef
		if err := rows.Scan(&r.DebtID, &r.PersonName, &r.ContactID, &r.Direction,
			&r.Amount, &r.SettledAmount, &r.Status); err != nil {
			return nil, fmt.Errorf("splits scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
