package budgets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

var (
	ErrBudgetNotFound    = errors.New("budget not found")
	ErrPeriodNotFound    = errors.New("budget period not found")
	ErrInvalidCategory   = errors.New("categories must be your own expense categories (not system ones)")
	ErrAccountNotFound   = errors.New("wallet not found")
	ErrInvalidRule       = errors.New("invalid budget period rule")
	ErrValidation        = errors.New("invalid request")
	ErrInvalidTransition = errors.New("this action doesn't apply in the budget's current state")
)

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

func (s *Store) Pool() *pgxpool.Pool { return s.db }

const budgetColumns = `id, user_id, name, description, note, icon_code, amount::float8, warn_pct, scope_all,
	tag_names, recurring, freq, interval_n, start_date, anchor_date, end_kind, end_date, end_count,
	closed_on, status, created_at, updated_at`

// scopeColumns read a budget's scope with the budget itself (one statement, not three):
// its categories in display order and its wallets.
const scopeColumns = `(SELECT COALESCE(json_agg(json_build_object('id', bc.category_id, 'include_children', bc.include_children)
		ORDER BY c.sort_order, c.name), '[]'::json)
		FROM budget_categories bc JOIN categories c ON c.id = bc.category_id WHERE bc.budget_id = budgets.id),
	(SELECT COALESCE(json_agg(w.account_id), '[]'::json) FROM budget_wallets w WHERE w.budget_id = budgets.id)`

const budgetSelect = budgetColumns + `, ` + scopeColumns

// scanBudget reads a row of budgetSelect.
func scanBudget(row pgx.Row) (*Budget, error) {
	var (
		b                Budget
		icon             []byte
		warn             *int16
		interval         int32
		count            *int32
		cats, walletsRaw []byte
	)
	if err := row.Scan(&b.ID, &b.UserID, &b.Name, &b.Description, &b.Note, &icon, &b.Amount, &warn, &b.ScopeAll,
		&b.TagNames, &b.Recurring, &b.Freq, &interval, &b.StartDate, &b.AnchorDate, &b.EndKind, &b.EndDate, &count,
		&b.ClosedOn, &b.Status, &b.CreatedAt, &b.UpdatedAt, &cats, &walletsRaw); err != nil {
		return nil, err
	}
	var scopeRows []struct {
		ID              uuid.UUID `json:"id"`
		IncludeChildren bool      `json:"include_children"`
	}
	if err := json.Unmarshal(cats, &scopeRows); err != nil {
		return nil, err
	}
	b.Categories = make([]scopeCat, len(scopeRows))
	for i, r := range scopeRows {
		b.Categories[i] = scopeCat{ID: r.ID, IncludeChildren: r.IncludeChildren}
	}
	b.Wallets = []uuid.UUID{}
	if err := json.Unmarshal(walletsRaw, &b.Wallets); err != nil {
		return nil, err
	}
	b.IconCode = shared.IconFromJSON(icon)
	b.Interval = int(interval)
	if warn != nil {
		v := int(*warn)
		b.WarnPct = &v
	}
	if count != nil {
		v := int(*count)
		b.EndCount = &v
	}
	if b.TagNames == nil {
		b.TagNames = []string{}
	}
	return &b, nil
}

func (s *Store) get(ctx context.Context, q pgx.Tx, userID, id uuid.UUID, lock bool) (*Budget, error) {
	sql := `SELECT ` + budgetSelect + ` FROM budgets WHERE id = $1 AND user_id = $2`
	if lock {
		sql += ` FOR UPDATE OF budgets`
	}
	var row pgx.Row
	if q != nil {
		row = q.QueryRow(ctx, sql, id, userID)
	} else {
		row = s.db.QueryRow(ctx, sql, id, userID)
	}
	b, err := scanBudget(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBudgetNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("budget: %w", err)
	}
	return b, nil
}

func (s *Store) listAll(ctx context.Context, userID uuid.UUID) ([]*Budget, error) {
	rows, err := s.db.Query(ctx, `SELECT `+budgetSelect+` FROM budgets WHERE user_id = $1 ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, fmt.Errorf("budgets: %w", err)
	}
	var out []*Budget
	for rows.Next() {
		b, err := scanBudget(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) save(ctx context.Context, tx pgx.Tx, b *Budget) error {
	var icon []byte
	if b.IconCode != nil {
		var err error
		if icon, err = json.Marshal(b.IconCode); err != nil {
			return err
		}
	}
	tags := b.TagNames
	if tags == nil {
		tags = []string{}
	}
	args := []any{b.ID, b.UserID, b.Name, shared.CleanText(b.Description), shared.CleanText(b.Note), icon, b.Amount,
		b.WarnPct, b.ScopeAll, tags, b.Recurring, b.Freq, b.Interval, b.StartDate, b.AnchorDate, b.EndKind,
		b.EndDate, b.EndCount, b.ClosedOn, b.Status}
	var err error
	if b.ID == uuid.Nil {
		b.ID, _ = uuid.NewV7()
		args[0] = b.ID
		_, err = tx.Exec(ctx, `INSERT INTO budgets (id, user_id, name, description, note, icon_code, amount, warn_pct,
			scope_all, tag_names, recurring, freq, interval_n, start_date, anchor_date, end_kind, end_date, end_count,
			closed_on, status)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)`, args...)
	} else {
		_, err = tx.Exec(ctx, `UPDATE budgets SET name = $3, description = $4, note = $5, icon_code = $6::jsonb,
			amount = $7, warn_pct = $8, scope_all = $9, tag_names = $10, recurring = $11, freq = $12, interval_n = $13,
			start_date = $14, anchor_date = $15, end_kind = $16, end_date = $17, end_count = $18, closed_on = $19,
			status = $20 WHERE id = $1 AND user_id = $2`, args...)
	}
	if err != nil {
		return fmt.Errorf("save budget: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM budget_categories WHERE budget_id = $1`, b.ID); err != nil {
		return err
	}
	for _, c := range b.Categories {
		if _, err := tx.Exec(ctx, `INSERT INTO budget_categories (budget_id, category_id, include_children)
			VALUES ($1, $2, $3)`, b.ID, c.ID, c.IncludeChildren); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM budget_wallets WHERE budget_id = $1`, b.ID); err != nil {
		return err
	}
	for _, w := range b.Wallets {
		if _, err := tx.Exec(ctx, `INSERT INTO budget_wallets (budget_id, account_id) VALUES ($1, $2)`, b.ID, w); err != nil {
			return err
		}
	}
	return nil
}

const periodColumns = `id, seq, start_date, end_date, amount::float8, overridden, note`

func scanPeriod(row pgx.Row) (*periodRow, error) {
	var (
		p   periodRow
		seq int32
	)
	if err := row.Scan(&p.ID, &seq, &p.Start, &p.End, &p.Amount, &p.Overridden, &p.Note); err != nil {
		return nil, err
	}
	p.Seq = int(seq)
	return &p, nil
}

func (s *Store) periods(ctx context.Context, q shared.Querier, budgetID uuid.UUID, where string, args ...any) ([]*periodRow, error) {
	rows, err := q.Query(ctx, `SELECT `+periodColumns+` FROM budget_periods WHERE budget_id = $1 `+where,
		append([]any{budgetID}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("periods: %w", err)
	}
	defer rows.Close()
	var out []*periodRow
	for rows.Next() {
		p, err := scanPeriod(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) baseSeq(ctx context.Context, q rowQuerier, b *Budget) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT COUNT(*) FROM budget_periods WHERE budget_id = $1 AND start_date < $2`,
		b.ID, b.AnchorDate).Scan(&n)
	return n, err
}

// periodsOf reads the periods of every budget in one statement, oldest first per budget.
func (s *Store) periodsOf(ctx context.Context, q shared.Querier, budgetIDs []uuid.UUID) (map[uuid.UUID][]*periodRow, error) {
	out := map[uuid.UUID][]*periodRow{}
	if len(budgetIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT budget_id, `+periodColumns+` FROM budget_periods
		WHERE budget_id = ANY($1) ORDER BY budget_id, start_date`, budgetIDs)
	if err != nil {
		return nil, fmt.Errorf("periods: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			bid uuid.UUID
			p   periodRow
			seq int32
		)
		if err := rows.Scan(&bid, &p.ID, &seq, &p.Start, &p.End, &p.Amount, &p.Overridden, &p.Note); err != nil {
			return nil, err
		}
		p.Seq = int(seq)
		out[bid] = append(out[bid], &p)
	}
	return out, rows.Err()
}

// baseSeqIn is baseSeq over the budget's periods already read: the periods that
// start before its anchor.
func baseSeqIn(b *Budget, rows []*periodRow) int {
	n := 0
	for _, p := range rows {
		if p.Start.Before(b.AnchorDate) {
			n++
		}
	}
	return n
}

func (s *Store) syncPeriods(ctx context.Context, tx pgx.Tx, b *Budget, today time.Time) error {
	base, err := s.baseSeq(ctx, tx, b)
	if err != nil {
		return err
	}
	have, err := s.periods(ctx, tx, b.ID, `AND start_date >= $2`, b.AnchorDate)
	if err != nil {
		return err
	}
	byStart := map[time.Time]*periodRow{}
	for _, p := range have {
		byStart[p.Start] = p
	}
	seen := map[uuid.UUID]bool{}
	for i, sp := range b.spans(base, today) {
		seq := base + i + 1
		if p, ok := byStart[sp.Start]; ok {
			seen[p.ID] = true
			if p.Seq != seq || !sameDate(p.End, sp.End) {
				if _, err := tx.Exec(ctx, `UPDATE budget_periods SET seq = $2, end_date = $3 WHERE id = $1`,
					p.ID, seq, sp.End); err != nil {
					return err
				}
			}
			continue
		}
		id, _ := uuid.NewV7()
		if _, err := tx.Exec(ctx, `INSERT INTO budget_periods (id, budget_id, seq, start_date, end_date, amount)
			VALUES ($1, $2, $3, $4, $5, $6)`, id, b.ID, seq, sp.Start, sp.End, b.Amount); err != nil {
			return fmt.Errorf("insert period: %w", err)
		}
	}
	for _, p := range have {
		if !seen[p.ID] {
			if _, err := tx.Exec(ctx, `DELETE FROM budget_periods WHERE id = $1`, p.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func sameDate(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func (s *Store) checkScope(ctx context.Context, q pgx.Tx, userID uuid.UUID, b *Budget) error {
	if len(b.Categories) > 0 {
		ids := make([]uuid.UUID, len(b.Categories))
		for i, c := range b.Categories {
			ids[i] = c.ID
		}
		var n int
		if err := q.QueryRow(ctx, `SELECT COUNT(*) FROM categories WHERE id = ANY($1) AND user_id = $2
			AND type = 'expense' AND NOT is_system`, ids, userID).Scan(&n); err != nil {
			return err
		}
		if n != len(ids) {
			return ErrInvalidCategory
		}
	}
	for _, w := range b.Wallets {
		var ok bool
		if err := q.QueryRow(ctx, `SELECT `+shared.ActiveMembershipPredicate("$1", "$2"), w, userID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return ErrAccountNotFound
		}
	}
	return nil
}

func (s *Store) expandCategories(ctx context.Context, cats []scopeCat) ([]uuid.UUID, error) {
	b := &Budget{Categories: cats}
	b.ID = uuid.Nil
	m, err := s.expandCategoriesOf(ctx, []*Budget{b})
	if err != nil {
		return nil, err
	}
	return m[b.ID], nil
}

// expandCategoriesOf gives, per budget, its scope categories with the sub-categories of
// the ones that include children: ONE recursive statement for every budget.
func (s *Store) expandCategoriesOf(ctx context.Context, bs []*Budget) (map[uuid.UUID][]uuid.UUID, error) {
	out := make(map[uuid.UUID][]uuid.UUID, len(bs))
	seenRoot := map[uuid.UUID]bool{}
	var roots []uuid.UUID
	for _, b := range bs {
		out[b.ID] = []uuid.UUID{}
		for _, c := range b.Categories {
			if c.IncludeChildren && !seenRoot[c.ID] {
				seenRoot[c.ID] = true
				roots = append(roots, c.ID)
			}
		}
	}
	family := map[uuid.UUID][]uuid.UUID{}
	if len(roots) > 0 {
		rows, err := s.db.Query(ctx, `WITH RECURSIVE d(root, id) AS (
				SELECT r, r FROM unnest($1::uuid[]) AS r
				UNION SELECT d.root, c.id FROM categories c JOIN d ON c.parent_id = d.id)
			SELECT root, id FROM d`, roots)
		if err != nil {
			return nil, fmt.Errorf("expand categories: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var root, id uuid.UUID
			if err := rows.Scan(&root, &id); err != nil {
				return nil, err
			}
			family[root] = append(family[root], id)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	for _, b := range bs {
		for _, c := range b.Categories {
			if c.IncludeChildren {
				out[b.ID] = append(out[b.ID], family[c.ID]...)
			} else {
				out[b.ID] = append(out[b.ID], c.ID)
			}
		}
	}
	return out, nil
}
