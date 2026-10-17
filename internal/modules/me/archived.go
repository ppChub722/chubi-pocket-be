package me

// The archived hub (ที่เก็บถาวร): everything the user archived, by kind, in one
// call — counts and the rows to restore / delete from.

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// ArchivedItem — one archived thing (spec ArchivedItem). The restore / delete
// calls are the entity's own (POST /<kind>/:id/restore, DELETE /<kind>/:id).
type ArchivedItem struct {
	ID         uuid.UUID        `json:"id"`
	Name       string           `json:"name"`
	IconCode   *shared.IconCode `json:"icon_code"`
	ArchivedAt time.Time        `json:"archived_at"` // last change of the row
	// CanRestore — whether the caller may restore it (wallets: owner only; events: owner only).
	CanRestore bool `json:"can_restore"`
	// Secondary — the row's second line, as data: kind balance | limit | target | planned | amount; null for contacts.
	Secondary *Secondary `json:"secondary"`
}

// ArchivedGroup — one kind.
type ArchivedGroup struct {
	Count int            `json:"count"`
	Items []ArchivedItem `json:"items"`
}

// ArchivedHub — spec schema ArchivedHub.
type ArchivedHub struct {
	Total     int           `json:"total"`
	Wallets   ArchivedGroup `json:"wallets"`
	Budgets   ArchivedGroup `json:"budgets"`
	Goals     ArchivedGroup `json:"goals"`
	Events    ArchivedGroup `json:"events"`
	Contacts  ArchivedGroup `json:"contacts"`
	Recurring ArchivedGroup `json:"recurring"`
}

func (s *Service) archivedGroup(ctx context.Context, q string, userID uuid.UUID) (ArchivedGroup, error) {
	g := ArchivedGroup{Items: []ArchivedItem{}}
	rows, err := s.db.Query(ctx, q, userID)
	if err != nil {
		return g, fmt.Errorf("archived: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			it   ArchivedItem
			icon []byte
		)
		var (
			secKind *string
			secAmt  *float64
		)
		if err := rows.Scan(&it.ID, &it.Name, &icon, &it.ArchivedAt, &it.CanRestore, &secKind, &secAmt); err != nil {
			return g, err
		}
		it.IconCode = shared.IconFromJSON(icon)
		if secKind != nil {
			it.Secondary = &Secondary{Kind: *secKind, Amount: secAmt}
		}
		g.Items = append(g.Items, it)
	}
	g.Count = len(g.Items)
	return g, rows.Err()
}

// Archived — GET /me/archived.
func (s *Service) Archived(ctx context.Context, userID uuid.UUID) (*ArchivedHub, error) {
	out := &ArchivedHub{}
	var err error
	if out.Wallets, err = s.archivedGroup(ctx, `SELECT a.id, a.name, a.icon_code, a.updated_at,
		EXISTS (SELECT 1 FROM account_members am WHERE am.account_id = a.id AND am.user_id = $1 AND am.role = 'owner' AND am.left_at IS NULL), 'balance', NULL::float8 FROM accounts a
		WHERE a.status = 'archived' AND `+shared.ActiveMembershipPredicate("a.id", "$1")+` ORDER BY a.updated_at DESC, a.id`, userID); err != nil {
		return nil, err
	}
	if len(out.Wallets.Items) > 0 {
		ids := make([]uuid.UUID, len(out.Wallets.Items))
		for i, it := range out.Wallets.Items {
			ids[i] = it.ID
		}
		bals, err := shared.LoadWalletBalances(ctx, s.db, ids)
		if err != nil {
			return nil, err
		}
		byID := map[uuid.UUID]float64{}
		for _, b := range bals {
			byID[b.AccountID] = b.Balance
		}
		for i := range out.Wallets.Items {
			b := byID[out.Wallets.Items[i].ID]
			out.Wallets.Items[i].Secondary.Amount = &b
		}
	}
	if out.Budgets, err = s.archivedGroup(ctx, `SELECT id, name, icon_code, updated_at, TRUE, 'limit', amount::float8 FROM budgets
		WHERE user_id = $1 AND status = 'archived' ORDER BY updated_at DESC, id`, userID); err != nil {
		return nil, err
	}
	if out.Goals, err = s.archivedGroup(ctx, `SELECT id, name, icon_code, updated_at, TRUE, 'target', target_amount::float8 FROM saving_goals
		WHERE user_id = $1 AND status = 'archived' ORDER BY updated_at DESC, id`, userID); err != nil {
		return nil, err
	}
	if out.Events, err = s.archivedGroup(ctx, `SELECT p.id, p.name, p.icon_code, p.updated_at, p.owner_user_id = $1, 'planned', p.planned_amount::float8 FROM projects p
		JOIN project_members m ON m.project_id = p.id AND m.user_id = $1 AND m.status = 'active'
		WHERE p.status = 'archived' ORDER BY p.updated_at DESC, p.id`, userID); err != nil {
		return nil, err
	}
	if out.Contacts, err = s.archivedGroup(ctx, `SELECT id, display_name, icon_code, updated_at, TRUE, NULL::text, NULL::float8 FROM contacts
		WHERE user_id = $1 AND status = 'archived' ORDER BY updated_at DESC, id`, userID); err != nil {
		return nil, err
	}
	if out.Recurring, err = s.archivedGroup(ctx, `SELECT id, name, icon_code, updated_at, TRUE, 'amount', amount::float8 FROM recurring_rules
		WHERE user_id = $1 AND status = 'archived' ORDER BY updated_at DESC, id`, userID); err != nil {
		return nil, err
	}
	out.Total = out.Wallets.Count + out.Budgets.Count + out.Goals.Count + out.Events.Count + out.Contacts.Count + out.Recurring.Count
	return out, nil
}

// GET /v1/me/archived — getMyArchived.
func (h *Handler) Archived(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	out, err := h.service.Archived(c.Request.Context(), userID)
	if err != nil {
		response.InternalError(c, "Failed to load archived items", err.Error())
		return
	}
	c.JSON(http.StatusOK, out)
}

// Secondary — the second line of an archived row (the app formats the amount).
type Secondary struct {
	Kind   string   `json:"kind"`
	Amount *float64 `json:"amount"`
}
