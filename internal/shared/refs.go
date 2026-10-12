package shared

// Shared reference shapes (API spec v2, internal/apidoc/openapi.yaml): what a
// row shows about another record — a wallet, a category, an event, a tag, a
// person — embedded in the row so the app never loads a whole list to look a
// name / icon / colour up by id. Every list / detail / write response builds
// them through the batch loaders below (one query per kind per page, no N+1).

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier — satisfied by *pgxpool.Pool and pgx.Tx.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// AccountRef — spec schema AccountRef (no balance: it changes).
type AccountRef struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	IconCode *IconCode `json:"icon_code"`
	LogoURL  *string   `json:"logo_url"`
	Type     string    `json:"type"`
	Group    string    `json:"group"` // asset | liability
	Currency string    `json:"currency"`
	Archived bool      `json:"archived"`
}

// AccountGroup — credit_card / pay_later are liabilities, the rest assets.
func AccountGroup(accountType string) string {
	switch accountType {
	case "credit_card", "pay_later":
		return "liability"
	}
	return "asset"
}

// CategoryRef — spec schema CategoryRef.
type CategoryRef struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Type       string     `json:"type"`
	IconCode   *IconCode  `json:"icon_code"`
	IsSystem   bool       `json:"is_system"`
	SystemKind *string    `json:"system_kind"`
	ParentID   *uuid.UUID `json:"parent_id"`
}

// EventRef — spec schema EventRef.
type EventRef struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	IconCode *IconCode `json:"icon_code"`
	Status   string    `json:"status"`
}

// TagRef — spec schema TagRef.
type TagRef struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	IconCode *IconCode `json:"icon_code"`
}

// UserRef — an app user (shared-wallet author, linked partner).
type UserRef struct {
	UserID      uuid.UUID `json:"user_id"`
	DisplayName string    `json:"display_name"`
	IconCode    *IconCode `json:"icon_code"`
}

// PersonRef — a person on a split / debt / draft, name resolved by the one
// name rule (PersonNameSQL).
type PersonRef struct {
	Name         string     `json:"name"`
	ContactID    *uuid.UUID `json:"contact_id"`
	IconCode     *IconCode  `json:"icon_code"`
	Linked       bool       `json:"linked"`
	LinkedUserID *uuid.UUID `json:"linked_user_id"`
}

// MemberRef — a member of an event.
type MemberRef struct {
	ID     uuid.UUID  `json:"id"`
	UserID *uuid.UUID `json:"user_id"`
	Name   string     `json:"name"`
	Icon   *IconCode  `json:"icon_code"`
	IsMe   bool       `json:"is_me"`
}

// WalletBalance — a wallet's balance right after a write. Every write that
// moves money returns the balances of ALL wallets it touched.
type WalletBalance struct {
	AccountID uuid.UUID `json:"account_id"`
	Balance   float64   `json:"balance"`
}

// Badges — the app-bar badges.
type Badges struct {
	UnreadNotifications int `json:"unread_notifications"`
	PendingDrafts       int `json:"pending_drafts"`
}

// Page — the pagination block of a list.
type Page struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	Total      int `json:"total"`
	TotalPages int `json:"total_pages"`
}

// NewPage fills TotalPages.
func NewPage(page, perPage, total int) Page {
	pages := 0
	if perPage > 0 {
		pages = (total + perPage - 1) / perPage
	}
	return Page{Page: page, PerPage: perPage, Total: total, TotalPages: pages}
}

// IconFromJSON decodes a jsonb icon_code column (nil / empty / "null" → nil).
func IconFromJSON(b []byte) *IconCode {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	var ic IconCode
	if err := json.Unmarshal(b, &ic); err != nil {
		return nil
	}
	return &ic
}

// PersonNameSQL — THE person display-name rule, as one SQL expression
// (owner 2026-10-12). contactAlias is a contacts row, linkedUserAlias the
// users row it links to (LEFT JOINed; NULL when unlinked):
//  1. nickname set AND show_nickname → the nickname
//  2. else linked → the linked user's live profile name
//  3. else the contact's display_name
//
// The stored snapshot (person_name) is only used when there is no contact:
// wrap as COALESCE(<this>, snapshot).
func PersonNameSQL(contactAlias, linkedUserAlias string) string {
	return fmt.Sprintf(`(CASE
		WHEN %[1]s.id IS NULL THEN NULL
		WHEN %[1]s.nickname IS NOT NULL AND %[1]s.nickname <> '' AND %[1]s.show_nickname THEN %[1]s.nickname
		WHEN %[2]s.id IS NOT NULL THEN %[2]s.display_name
		ELSE %[1]s.display_name END)`, contactAlias, linkedUserAlias)
}

// PersonIconSQL — the linked user's icon, else the contact's own.
func PersonIconSQL(contactAlias, linkedUserAlias string) string {
	return fmt.Sprintf(`COALESCE(%[2]s.icon_code, %[1]s.icon_code)`, contactAlias, linkedUserAlias)
}

// ───────────── batch loaders ─────────────

// LoadAccountRefs resolves wallets by id (any status).
func LoadAccountRefs(ctx context.Context, q Querier, ids []uuid.UUID) (map[uuid.UUID]AccountRef, error) {
	out := map[uuid.UUID]AccountRef{}
	ids = uniqueIDs(ids)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT id, name, icon_code, logo_url, type, currency, status
		FROM accounts WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("account refs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			r      AccountRef
			icon   []byte
			status string
		)
		if err := rows.Scan(&r.ID, &r.Name, &icon, &r.LogoURL, &r.Type, &r.Currency, &status); err != nil {
			return nil, err
		}
		r.IconCode, r.Group, r.Archived = IconFromJSON(icon), AccountGroup(r.Type), status != "active"
		out[r.ID] = r
	}
	return out, rows.Err()
}

// LoadCategoryRefs resolves categories by id (any status).
func LoadCategoryRefs(ctx context.Context, q Querier, ids []uuid.UUID) (map[uuid.UUID]CategoryRef, error) {
	out := map[uuid.UUID]CategoryRef{}
	ids = uniqueIDs(ids)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT id, name, type, icon_code, is_system, system_kind, parent_id
		FROM categories WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("category refs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			r    CategoryRef
			icon []byte
		)
		if err := rows.Scan(&r.ID, &r.Name, &r.Type, &icon, &r.IsSystem, &r.SystemKind, &r.ParentID); err != nil {
			return nil, err
		}
		r.IconCode = IconFromJSON(icon)
		out[r.ID] = r
	}
	return out, rows.Err()
}

// LoadEventRefs resolves events (projects) by id.
func LoadEventRefs(ctx context.Context, q Querier, ids []uuid.UUID) (map[uuid.UUID]EventRef, error) {
	out := map[uuid.UUID]EventRef{}
	ids = uniqueIDs(ids)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT id, name, icon_code, status FROM projects WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("event refs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			r    EventRef
			icon []byte
		)
		if err := rows.Scan(&r.ID, &r.Name, &icon, &r.Status); err != nil {
			return nil, err
		}
		r.IconCode = IconFromJSON(icon)
		out[r.ID] = r
	}
	return out, rows.Err()
}

// LoadTagRefs resolves tags by id (the caller's own).
func LoadTagRefs(ctx context.Context, q Querier, userID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]TagRef, error) {
	out := map[uuid.UUID]TagRef{}
	ids = uniqueIDs(ids)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT id, name, icon_code FROM tags WHERE id = ANY($1) AND user_id = $2`, ids, userID)
	if err != nil {
		return nil, fmt.Errorf("tag refs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			r    TagRef
			icon []byte
		)
		if err := rows.Scan(&r.ID, &r.Name, &icon); err != nil {
			return nil, err
		}
		r.IconCode = IconFromJSON(icon)
		out[r.ID] = r
	}
	return out, rows.Err()
}

// PersonKey — what a row stores about its person: the contact (maybe) and the
// typed snapshot name.
type PersonKey struct {
	ContactID *uuid.UUID
	Snapshot  string
}

// LoadPersons resolves the callers's contacts by id into PersonRefs (the name
// rule applied). Persons without a contact are built by ResolvePerson.
func LoadPersons(ctx context.Context, q Querier, userID uuid.UUID, contactIDs []uuid.UUID) (map[uuid.UUID]PersonRef, error) {
	out := map[uuid.UUID]PersonRef{}
	contactIDs = uniqueIDs(contactIDs)
	if len(contactIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT c.id, `+PersonNameSQL("c", "lu")+`, `+PersonIconSQL("c", "lu")+`, lu.id
		FROM contacts c LEFT JOIN users lu ON lu.id = c.linked_user_id
		WHERE c.id = ANY($1) AND c.user_id = $2`, contactIDs, userID)
	if err != nil {
		return nil, fmt.Errorf("persons: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id     uuid.UUID
			name   *string
			icon   []byte
			linked *uuid.UUID
		)
		if err := rows.Scan(&id, &name, &icon, &linked); err != nil {
			return nil, err
		}
		p := PersonRef{ContactID: &id, IconCode: IconFromJSON(icon), Linked: linked != nil, LinkedUserID: linked}
		if name != nil {
			p.Name = *name
		}
		out[id] = p
	}
	return out, rows.Err()
}

// ResolvePerson builds the PersonRef of one stored (contact, snapshot) pair
// from a LoadPersons map: a known contact → the live name; otherwise (no
// contact, or a contact that is gone) → the snapshot, a free name.
func ResolvePerson(persons map[uuid.UUID]PersonRef, k PersonKey) PersonRef {
	if k.ContactID != nil {
		if p, ok := persons[*k.ContactID]; ok && p.Name != "" {
			return p
		}
	}
	return PersonRef{Name: k.Snapshot}
}

func uniqueIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id != uuid.Nil && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
