// Package institutions — the user's own banks, card issuers, finance companies and stores (0.4.3):
// a debt counterparty that is not a person, and what a wallet belongs to. A row may copy a central
// payment provider (one own row per provider).
package institutions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

var (
	ErrNotFound         = errors.New("institution not found")
	ErrInUse            = errors.New("debts, recurring items or wallets still point at this institution")
	ErrValidation       = errors.New("invalid request")
	ErrProviderNotFound = errors.New("payment provider not found")
	ErrInvalidState     = errors.New("this action doesn't apply in the current status")
)

var kinds = map[string]bool{"bank": true, "card": true, "finance": true, "store": true, "other": true}

// providerKind — the institution kind a central provider gives by default.
func providerKind(k string) string {
	switch k {
	case "bank", "finance", "store":
		return k
	case "card_issuer":
		return "card"
	}
	return "other"
}

type ProviderRef struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	ShortName *string   `json:"short_name"`
	Color     *string   `json:"color"`
	Phone     *string   `json:"phone"`
	Website   *string   `json:"website"`
}

type Institution struct {
	ID          uuid.UUID        `json:"id"`
	Name        string           `json:"name"`
	Kind        string           `json:"kind"`
	ProviderID  *uuid.UUID       `json:"provider_id"`
	Provider    *ProviderRef     `json:"provider"`
	Phone       *string          `json:"phone"`
	Email       *string          `json:"email"`
	IconCode    *shared.IconCode `json:"icon_code"`
	Description *string          `json:"description"`
	Note        *string          `json:"note"`
	Status      string           `json:"status"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

// DebtSummary — the institution's debt balance (its debt page's hero, in short).
type DebtSummary struct {
	Key        string  `json:"key"`
	Net        float64 `json:"net"` // > 0 = it owes me, < 0 = I owe it
	Side       string  `json:"side"`
	EntryCount int     `json:"entry_count"`
	// RecurringCount — its recurring items (installments, loans).
	RecurringCount int `json:"recurring_count"`
}

type Detail struct {
	Institution
	Accounts []shared.AccountRef `json:"accounts"`
	Debt     DebtSummary         `json:"debt"`
}

type List struct {
	Data []Institution `json:"data"`
}

type Input struct {
	Name        *string          `json:"name"`
	Kind        *string          `json:"kind"`
	ProviderID  *uuid.UUID       `json:"provider_id"`
	Phone       *string          `json:"phone"`
	Email       *string          `json:"email"`
	IconCode    *shared.IconCode `json:"icon_code"`
	Description *string          `json:"description"`
	Note        *string          `json:"note"`
}

type DeleteResult struct {
	ID uuid.UUID `json:"id"`
}

// querier — a pool or a transaction.
type querier interface {
	shared.Querier
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Service struct{ db *pgxpool.Pool }

func NewService(db *pgxpool.Pool) *Service { return &Service{db: db} }

const columns = `i.id, i.name, i.kind, i.provider_id, i.phone, i.email, i.icon_code, i.description, i.note, i.status,
	i.created_at, i.updated_at, p.id, p.name_th, p.short_name, p.color, p.phone, p.website`

const from = ` FROM institutions i LEFT JOIN payment_providers p ON p.id = i.provider_id`

func scan(row pgx.Row) (*Institution, error) {
	var (
		in    Institution
		icon  []byte
		pid   *uuid.UUID
		pName *string
		p     ProviderRef
	)
	if err := row.Scan(&in.ID, &in.Name, &in.Kind, &in.ProviderID, &in.Phone, &in.Email, &icon, &in.Description, &in.Note,
		&in.Status, &in.CreatedAt, &in.UpdatedAt, &pid, &pName, &p.ShortName, &p.Color, &p.Phone, &p.Website); err != nil {
		return nil, err
	}
	in.IconCode = shared.IconFromJSON(icon)
	if pid != nil && pName != nil {
		p.ID, p.Name = *pid, *pName
		in.Provider = &p
	}
	return &in, nil
}

func (s *Service) get(ctx context.Context, q querier, userID, id uuid.UUID) (*Institution, error) {
	in, err := scan(q.QueryRow(ctx, `SELECT `+columns+from+` WHERE i.id = $1 AND i.user_id = $2`, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("institution: %w", err)
	}
	return in, nil
}

func (s *Service) List(ctx context.Context, userID uuid.UUID, status string) (*List, error) {
	cond := `i.status = 'active'`
	switch status {
	case "archived":
		cond = `i.status = 'archived'`
	case "all":
		cond = `TRUE`
	}
	rows, err := s.db.Query(ctx, `SELECT `+columns+from+` WHERE i.user_id = $1 AND `+cond+` ORDER BY LOWER(i.name), i.id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list institutions: %w", err)
	}
	defer rows.Close()
	out := &List{Data: []Institution{}}
	for rows.Next() {
		in, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out.Data = append(out.Data, *in)
	}
	return out, rows.Err()
}

func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (*Detail, error) {
	in, err := s.get(ctx, s.db, userID, id)
	if err != nil {
		return nil, err
	}
	out := &Detail{Institution: *in, Accounts: []shared.AccountRef{}}
	rows, err := s.db.Query(ctx, `SELECT id FROM accounts WHERE institution_id = $1 AND user_id = $2 ORDER BY sort_order, created_at`, id, userID)
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var a uuid.UUID
		if err := rows.Scan(&a); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, a)
	}
	rows.Close()
	refs, err := shared.LoadAccountRefs(ctx, s.db, ids)
	if err != nil {
		return nil, err
	}
	for _, a := range ids {
		if r, ok := refs[a]; ok {
			out.Accounts = append(out.Accounts, r)
		}
	}
	key := shared.InstitutionKey(id)
	out.Debt.Key = key
	if err := s.db.QueryRow(ctx, `SELECT COALESCE(SUM(l.signed), 0)::float8, COUNT(*) FROM (`+shared.DebtLedgerSQL("$1")+`) l
		WHERE l.key = $2`, userID, key).Scan(&out.Debt.Net, &out.Debt.EntryCount); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(ctx, `SELECT COUNT(DISTINCT recurring_id) FROM recurring_rules
		WHERE user_id = $1 AND institution_id = $2 AND status <> 'archived'`,
		userID, id).Scan(&out.Debt.RecurringCount); err != nil {
		return nil, err
	}
	out.Debt.Side = shared.DebtSide(out.Debt.Net)
	return out, nil
}

func clean(p *string, max int) (*string, error) {
	v := shared.CleanText(p)
	if v != nil && len([]rune(*v)) > max {
		return nil, ErrValidation
	}
	return v, nil
}

// apply — the fields of in onto row (create: every field; update: the same, full replace).
func apply(row *Institution, in Input) error {
	if in.Name == nil || strings.TrimSpace(*in.Name) == "" || len([]rune(strings.TrimSpace(*in.Name))) > 100 {
		return ErrValidation
	}
	row.Name = strings.TrimSpace(*in.Name)
	if in.Kind == nil || !kinds[*in.Kind] {
		return ErrValidation
	}
	row.Kind = *in.Kind
	var err error
	if row.Phone, err = clean(in.Phone, 30); err != nil {
		return err
	}
	if row.Email, err = clean(in.Email, 200); err != nil {
		return err
	}
	if row.Description, err = clean(in.Description, 200); err != nil {
		return err
	}
	if row.Note, err = clean(in.Note, 500); err != nil {
		return err
	}
	row.IconCode = in.IconCode
	return nil
}

func iconJSON(ic *shared.IconCode) ([]byte, error) {
	if ic == nil {
		return nil, nil
	}
	return json.Marshal(ic)
}

// Create — with a provider_id the user's existing row of that provider comes back (created false).
// Name and kind default to the provider's.
func (s *Service) Create(ctx context.Context, userID uuid.UUID, in Input) (*Institution, bool, error) {
	if in.ProviderID != nil {
		var (
			name, kind string
			phone      *string
		)
		err := s.db.QueryRow(ctx, `SELECT name_th, kind, phone FROM payment_providers WHERE id = $1`, *in.ProviderID).Scan(&name, &kind, &phone)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, ErrProviderNotFound
		}
		if err != nil {
			return nil, false, err
		}
		var existing uuid.UUID
		err = s.db.QueryRow(ctx, `SELECT id FROM institutions WHERE user_id = $1 AND provider_id = $2`, userID, *in.ProviderID).Scan(&existing)
		if err == nil {
			got, err := s.get(ctx, s.db, userID, existing)
			return got, false, err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, false, err
		}
		if in.Name == nil || strings.TrimSpace(*in.Name) == "" {
			in.Name = &name
		}
		if in.Kind == nil {
			k := providerKind(kind)
			in.Kind = &k
		}
		if in.Phone == nil {
			in.Phone = phone
		}
	}
	var row Institution
	if err := apply(&row, in); err != nil {
		return nil, false, err
	}
	icon, err := iconJSON(row.IconCode)
	if err != nil {
		return nil, false, ErrValidation
	}
	id, _ := uuid.NewV7()
	tag, err := s.db.Exec(ctx, `INSERT INTO institutions (id, user_id, name, kind, provider_id, phone, email, icon_code, description, note)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10) ON CONFLICT DO NOTHING`,
		id, userID, row.Name, row.Kind, in.ProviderID, row.Phone, row.Email, icon, row.Description, row.Note)
	if err != nil {
		return nil, false, fmt.Errorf("create institution: %w", err)
	}
	if tag.RowsAffected() == 0 && in.ProviderID != nil { // a parallel create of the same provider won
		var existing uuid.UUID
		if err := s.db.QueryRow(ctx, `SELECT id FROM institutions WHERE user_id = $1 AND provider_id = $2`, userID, *in.ProviderID).Scan(&existing); err != nil {
			return nil, false, err
		}
		got, err := s.get(ctx, s.db, userID, existing)
		return got, false, err
	}
	got, err := s.get(ctx, s.db, userID, id)
	return got, true, err
}

// Update — a full replace of the editable fields; provider_id is kept (a provider row is the
// user's copy, the provider itself never changes here).
func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, in Input) (*Institution, error) {
	cur, err := s.get(ctx, s.db, userID, id)
	if err != nil {
		return nil, err
	}
	if err := apply(cur, in); err != nil {
		return nil, err
	}
	icon, err := iconJSON(cur.IconCode)
	if err != nil {
		return nil, ErrValidation
	}
	if _, err := s.db.Exec(ctx, `UPDATE institutions SET name = $3, kind = $4, phone = $5, email = $6, icon_code = $7::jsonb,
		description = $8, note = $9 WHERE id = $1 AND user_id = $2`,
		id, userID, cur.Name, cur.Kind, cur.Phone, cur.Email, icon, cur.Description, cur.Note); err != nil {
		return nil, fmt.Errorf("update institution: %w", err)
	}
	// the debt rows keep the name as their display copy
	if _, err := s.db.Exec(ctx, `UPDATE personal_debts SET counterparty_person_name = $3
		WHERE user_id = $1 AND counterparty_institution_id = $2 AND counterparty_person_name <> $3`, userID, id, cur.Name); err != nil {
		return nil, err
	}
	return s.get(ctx, s.db, userID, id)
}

func (s *Service) setStatus(ctx context.Context, userID, id uuid.UUID, from, to string) (*Institution, error) {
	cur, err := s.get(ctx, s.db, userID, id)
	if err != nil {
		return nil, err
	}
	if cur.Status != from {
		return nil, ErrInvalidState
	}
	if _, err := s.db.Exec(ctx, `UPDATE institutions SET status = $3 WHERE id = $1 AND user_id = $2`, id, userID, to); err != nil {
		return nil, err
	}
	return s.get(ctx, s.db, userID, id)
}

func (s *Service) Archive(ctx context.Context, userID, id uuid.UUID) (*Institution, error) {
	return s.setStatus(ctx, userID, id, "active", "archived")
}

func (s *Service) Restore(ctx context.Context, userID, id uuid.UUID) (*Institution, error) {
	return s.setStatus(ctx, userID, id, "archived", "active")
}

// Delete — a hard delete, refused while a debt row, a recurring item or a wallet points at it.
func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) (*DeleteResult, error) {
	if _, err := s.get(ctx, s.db, userID, id); err != nil {
		return nil, err
	}
	var used bool
	if err := s.db.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM personal_debts WHERE counterparty_institution_id = $1)
		OR EXISTS (SELECT 1 FROM recurring_rules WHERE institution_id = $1)
		OR EXISTS (SELECT 1 FROM debt_plans WHERE institution_id = $1)
		OR EXISTS (SELECT 1 FROM accounts WHERE institution_id = $1)`, id).Scan(&used); err != nil {
		return nil, err
	}
	if used {
		return nil, ErrInUse
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM institutions WHERE id = $1 AND user_id = $2`, id, userID); err != nil {
		return nil, err
	}
	return &DeleteResult{ID: id}, nil
}

// CheckOwned — the institution is the user's (and, when active is set, not archived). Other
// modules call it before pointing a row at one.
func CheckOwned(ctx context.Context, q querier, userID, id uuid.UUID, active bool) error {
	var status string
	err := q.QueryRow(ctx, `SELECT status FROM institutions WHERE id = $1 AND user_id = $2`, id, userID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if active && status != "active" {
		return ErrInvalidState
	}
	return nil
}
