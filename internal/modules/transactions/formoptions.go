package transactions

// GET /transactions/form-options (API spec v2, schema FormOptions):
// everything the transaction form / draft editor needs in ONE call —
// wallets, the preselected wallet, categories (flat, tree order), tags,
// events and the fee category. Direct SQL (no imports of the accounts /
// categories / tags / projects modules).

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// AccountOption — a wallet in the picker (spec AccountOption).
type AccountOption struct {
	shared.AccountRef
	Balance   float64 `json:"balance"`
	SortOrder int     `json:"sort_order"` // the caller's own order (account_members)
	IsDefault bool    `json:"is_default"` // = default_account_id
}

// CategoryOption — a category in the picker (spec CategoryOption): active,
// non-system, flat in tree display order; the tree is rebuilt from
// parent_id.
type CategoryOption struct {
	shared.CategoryRef
	SortOrder  int     `json:"sort_order"`
	Depth      int     `json:"depth"` // 1 = top level … 3
	ParentName *string `json:"parent_name"`
}

// TagOption — spec schema Tag (a tag with its usage count).
type TagOption struct {
	ID          uuid.UUID        `json:"id"`
	Name        string           `json:"name"`
	Description *string          `json:"description"`
	Note        *string          `json:"note"`
	IconCode    *shared.IconCode `json:"icon_code"`
	UsageCount  int              `json:"usage_count"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

// FormOptions — spec schema FormOptions.
type FormOptions struct {
	Accounts []AccountOption `json:"accounts"`
	// The wallet of the caller's most recent expense / income (by entry
	// time) among their active wallets, else the first wallet in their
	// order, else null.
	DefaultAccountID *uuid.UUID        `json:"default_account_id"`
	Categories       []CategoryOption  `json:"categories"`
	Tags             []TagOption       `json:"tags"`   // most used first, then by name
	Events           []shared.EventRef `json:"events"` // active, caller is an active non-viewer member
	// users' preference fee_category_id when it is one of the caller's
	// active expense categories; null otherwise.
	FeeCategoryID *uuid.UUID `json:"fee_category_id"`
}

// FormOptions — GET /transactions/form-options.
func (s *Service) FormOptions(ctx context.Context, userID uuid.UUID) (*FormOptions, error) {
	db := s.store.db
	out := &FormOptions{}
	var err error
	if out.Accounts, err = formAccounts(ctx, db, userID); err != nil {
		return nil, err
	}
	if out.DefaultAccountID, err = formDefaultAccount(ctx, db, userID, out.Accounts); err != nil {
		return nil, err
	}
	for i := range out.Accounts {
		out.Accounts[i].IsDefault = out.DefaultAccountID != nil && out.Accounts[i].ID == *out.DefaultAccountID
	}
	if out.Categories, err = formCategories(ctx, db, userID); err != nil {
		return nil, err
	}
	if out.Tags, err = formTags(ctx, db, userID); err != nil {
		return nil, err
	}
	if out.Events, err = formEvents(ctx, db, userID); err != nil {
		return nil, err
	}
	if out.FeeCategoryID, err = formFeeCategory(ctx, db, userID); err != nil {
		return nil, err
	}
	return out, nil
}

// formAccounts — the caller's active wallets (active membership, status
// active) in the caller's own order.
func formAccounts(ctx context.Context, q shared.Querier, userID uuid.UUID) ([]AccountOption, error) {
	rows, err := q.Query(ctx, `
		SELECT a.id, a.name, a.icon_code, a.logo_url, a.type, a.currency, a.balance, m.sort_order
		FROM accounts a
		JOIN LATERAL (SELECT am.sort_order FROM account_members am
		              WHERE am.account_id = a.id AND am.user_id = $1
		                AND am.joined_at IS NOT NULL AND am.left_at IS NULL
		              LIMIT 1) m ON TRUE
		WHERE a.status = 'active'
		ORDER BY m.sort_order, a.created_at, a.id`, userID)
	if err != nil {
		return nil, fmt.Errorf("form accounts: %w", err)
	}
	defer rows.Close()
	out := []AccountOption{}
	for rows.Next() {
		var (
			o    AccountOption
			icon []byte
		)
		if err := rows.Scan(&o.ID, &o.Name, &icon, &o.LogoURL, &o.Type, &o.Currency, &o.Balance, &o.SortOrder); err != nil {
			return nil, fmt.Errorf("form accounts scan: %w", err)
		}
		o.IconCode, o.Group = shared.IconFromJSON(icon), shared.AccountGroup(o.Type)
		out = append(out, o)
	}
	return out, rows.Err()
}

// formDefaultAccount — the wallet of the caller's most recent expense /
// income among accounts, else the first of accounts, else nil.
func formDefaultAccount(ctx context.Context, q shared.Querier, userID uuid.UUID, accounts []AccountOption) (*uuid.UUID, error) {
	if len(accounts) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(accounts))
	for i := range accounts {
		ids[i] = accounts[i].ID
	}
	rows, err := q.Query(ctx, `
		SELECT t.account_id FROM transactions t
		WHERE t.user_id = $1 AND t.account_id = ANY($2) AND t.type IN ('expense', 'income')
		ORDER BY t.created_at DESC, t.id DESC
		LIMIT 1`, userID, ids)
	if err != nil {
		return nil, fmt.Errorf("form default account: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		return &id, rows.Err()
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	first := accounts[0].ID
	return &first, nil
}

// formCategories — the caller's active non-system categories, depth-first
// in display order: by type (expense, income), then siblings by
// sort_order / created_at.
func formCategories(ctx context.Context, q shared.Querier, userID uuid.UUID) ([]CategoryOption, error) {
	rows, err := q.Query(ctx, `
		SELECT id, name, type, icon_code, is_system, system_kind, parent_id, sort_order
		FROM categories
		WHERE user_id = $1 AND NOT is_system
		ORDER BY type, sort_order, created_at, id`, userID)
	if err != nil {
		return nil, fmt.Errorf("form categories: %w", err)
	}
	type node struct {
		opt CategoryOption
	}
	var (
		all      []*node
		byID     = map[uuid.UUID]*node{}
		children = map[uuid.UUID][]*node{}
	)
	for rows.Next() {
		var (
			n    node
			icon []byte
		)
		if err := rows.Scan(&n.opt.ID, &n.opt.Name, &n.opt.Type, &icon, &n.opt.IsSystem, &n.opt.SystemKind,
			&n.opt.ParentID, &n.opt.SortOrder); err != nil {
			rows.Close()
			return nil, fmt.Errorf("form categories scan: %w", err)
		}
		n.opt.IconCode = shared.IconFromJSON(icon)
		all = append(all, &n)
		byID[n.opt.ID] = &n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var roots []*node
	for _, n := range all {
		if n.opt.ParentID != nil {
			if _, ok := byID[*n.opt.ParentID]; ok {
				children[*n.opt.ParentID] = append(children[*n.opt.ParentID], n)
				continue
			}
		}
		roots = append(roots, n)
	}
	// The query order already sorts siblings; a stable sort by type keeps
	// expense roots before income roots whatever their parents' order.
	sort.SliceStable(roots, func(i, j int) bool { return roots[i].opt.Type < roots[j].opt.Type })

	out := []CategoryOption{}
	var walk func(n *node, depth int, parent *string)
	walk = func(n *node, depth int, parent *string) {
		if depth > 3 {
			return
		}
		o := n.opt
		o.Depth, o.ParentName = depth, parent
		out = append(out, o)
		name := n.opt.Name
		for _, c := range children[n.opt.ID] {
			walk(c, depth+1, &name)
		}
	}
	for _, r := range roots {
		walk(r, 1, nil)
	}
	return out, nil
}

// formTags — the caller's tags, most used first, then by name (the tags
// list order).
func formTags(ctx context.Context, q shared.Querier, userID uuid.UUID) ([]TagOption, error) {
	rows, err := q.Query(ctx, `
		SELECT t.id, t.name, t.description, t.note, t.icon_code, t.created_at, t.updated_at,
		       (SELECT COUNT(*) FROM transaction_tags tt WHERE tt.tag_id = t.id) AS usage_count
		FROM tags t
		WHERE t.user_id = $1
		ORDER BY usage_count DESC, LOWER(t.name), t.id`, userID)
	if err != nil {
		return nil, fmt.Errorf("form tags: %w", err)
	}
	defer rows.Close()
	out := []TagOption{}
	for rows.Next() {
		var (
			t    TagOption
			icon []byte
		)
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Note, &icon, &t.CreatedAt, &t.UpdatedAt, &t.UsageCount); err != nil {
			return nil, fmt.Errorf("form tags scan: %w", err)
		}
		t.IconCode = shared.IconFromJSON(icon)
		out = append(out, t)
	}
	return out, rows.Err()
}

// formEvents — active events the caller can add bills to (an active
// member, not a viewer), most recently updated first.
func formEvents(ctx context.Context, q shared.Querier, userID uuid.UUID) ([]shared.EventRef, error) {
	rows, err := q.Query(ctx, `
		SELECT p.id, p.name, p.icon_code, p.status
		FROM projects p
		WHERE p.status = 'active'
		  AND EXISTS (SELECT 1 FROM project_members m
		              WHERE m.project_id = p.id AND m.user_id = $1
		                AND m.status = 'active' AND m.role <> 'viewer')
		ORDER BY p.updated_at DESC, p.id`, userID)
	if err != nil {
		return nil, fmt.Errorf("form events: %w", err)
	}
	defer rows.Close()
	out := []shared.EventRef{}
	for rows.Next() {
		var (
			e    shared.EventRef
			icon []byte
		)
		if err := rows.Scan(&e.ID, &e.Name, &icon, &e.Status); err != nil {
			return nil, fmt.Errorf("form events scan: %w", err)
		}
		e.IconCode = shared.IconFromJSON(icon)
		out = append(out, e)
	}
	return out, rows.Err()
}

// formFeeCategory — the fee_category_id preference when it still names one
// of the caller's active, non-system expense categories.
func formFeeCategory(ctx context.Context, q shared.Querier, userID uuid.UUID) (*uuid.UUID, error) {
	rows, err := q.Query(ctx, `
		SELECT c.id
		FROM user_preferences up
		JOIN categories c ON c.id::text = up.preferences->>'fee_category_id'
		WHERE up.user_id = $1 AND c.user_id = $1
		  AND NOT c.is_system AND c.type = 'expense'`, userID)
	if err != nil {
		return nil, fmt.Errorf("form fee category: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, nil
	}
	var id uuid.UUID
	if err := rows.Scan(&id); err != nil {
		return nil, err
	}
	return &id, nil
}

type FormDefaults struct {
	DefaultAccountID *uuid.UUID `json:"default_account_id"`
	FeeCategoryID    *uuid.UUID `json:"fee_category_id"`
}

// FormDefaults — the two defaults of FormOptions on their own (the sync dataset form_defaults).
func (s *Service) FormDefaults(ctx context.Context, userID uuid.UUID) (*FormDefaults, error) {
	db := s.store.db
	accounts, err := formAccounts(ctx, db, userID)
	if err != nil {
		return nil, err
	}
	out := &FormDefaults{}
	if out.DefaultAccountID, err = formDefaultAccount(ctx, db, userID, accounts); err != nil {
		return nil, err
	}
	if out.FeeCategoryID, err = formFeeCategory(ctx, db, userID); err != nil {
		return nil, err
	}
	return out, nil
}
