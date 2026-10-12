// Package me serves the caller's shell data: GET /me/counts (badges + the
// counts and figures the More hub shows), in one round trip.
package me

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/budgets"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// GoalsCount is counts.goals (same numbers as the Home saving_goals tile).
type GoalsCount struct {
	Count       int     `json:"count"`
	ProgressPct float64 `json:"progress_pct"`
}

// DebtsSummary / BudgetsSummary / ScheduledCount — the figures the More
// tiles show next to the counts (the same numbers as the Home tiles).
type DebtsSummary struct {
	OwedToMe float64 `json:"owed_to_me"`
	IOwe     float64 `json:"i_owe"`
	Net      float64 `json:"net"`
}

type BudgetsSummary struct {
	TotalBudget    float64 `json:"total_budget"`
	TotalSpent     float64 `json:"total_spent"`
	UtilizationPct float64 `json:"utilization_pct"`
	OverLimitCount int     `json:"over_limit_count"`
}

type ScheduledCount struct {
	ActiveCount   int `json:"active_count"`
	UpcomingCount int `json:"upcoming_count"`
}

// Counts is spec schema MeCounts.counts.
type Counts struct {
	Categories   int        `json:"categories"`
	Tags         int        `json:"tags"`
	Contacts     int        `json:"contacts"`
	ActiveEvents int        `json:"active_events"`
	OpenDebts    int        `json:"open_debts"`
	Budgets      int        `json:"budgets"`
	Goals        GoalsCount `json:"goals"`

	DebtsSummary   DebtsSummary   `json:"debts_summary"`
	BudgetsSummary BudgetsSummary `json:"budgets_summary"`
	Scheduled      ScheduledCount `json:"scheduled"`
}

// MeCounts is spec schema MeCounts.
type MeCounts struct {
	Badges shared.Badges `json:"badges"`
	Counts Counts        `json:"counts"`
}

// upcomingDays — the Home dashboard's look-ahead window.
const upcomingDays = 14

type Service struct {
	db      *pgxpool.Pool
	budgets *budgets.Service
	debts   *personal_debts.Service
}

func NewService(db *pgxpool.Pool) *Service { return &Service{db: db} }

// WithBudgets / WithDebts wire the services whose numbers the More tiles
// reuse (the same rules as the Home dashboard tiles).
func (s *Service) WithBudgets(b *budgets.Service) *Service      { s.budgets = b; return s }
func (s *Service) WithDebts(d *personal_debts.Service) *Service { s.debts = d; return s }

// Rules (each mirrors the page it labels):
//   - categories: active, non-system
//   - tags: all
//   - contacts: active
//   - active_events: status active, caller owns it or is an active member
//   - open_debts: open personal_debts rows (the Home debts tile open_count)
//   - budgets: active personal (scope user) budgets (the Home budgets tile)
//   - goals: active goals; progress = sum(min(current,target)) / sum(target)
//     with current = linked account balance * allocation_pct / 100
//   - debts_summary / budgets_summary: the Home debts / budgets tile numbers
//   - scheduled: active scheduled transactions, and those due within the Home
//     window (overdue included)
const countsSQL = `SELECT
	(SELECT COUNT(*) FROM categories WHERE user_id = $1 AND status = 'active' AND is_system = FALSE),
	(SELECT COUNT(*) FROM tags WHERE user_id = $1),
	(SELECT COUNT(*) FROM contacts WHERE user_id = $1 AND status = 'active'),
	(SELECT COUNT(*) FROM projects p WHERE p.status = 'active' AND (p.owner_user_id = $1 OR EXISTS (
		SELECT 1 FROM project_members m WHERE m.project_id = p.id AND m.user_id = $1 AND m.status = 'active'))),
	(SELECT COUNT(*) FROM personal_debts WHERE user_id = $1 AND status = 'open'),
	(SELECT COUNT(*) FROM budgets WHERE user_id = $1 AND scope = 'user' AND status = 'active'),
	g.cnt, g.target, g.current
FROM (
	SELECT COUNT(*) AS cnt,
	       COALESCE(SUM(sg.target_amount), 0)::float8 AS target,
	       COALESCE(SUM(LEAST(ROUND(a.balance * sg.allocation_pct / 100, 2), sg.target_amount)), 0)::float8 AS current
	FROM saving_goals sg JOIN accounts a ON a.id = sg.linked_account_id
	WHERE sg.user_id = $1 AND sg.status = 'active'
) g`

func (s *Service) Counts(ctx context.Context, userID uuid.UUID, headerTZ string) (*MeCounts, error) {
	badges, err := shared.LoadBadges(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}
	out := &MeCounts{Badges: badges}
	var target, current float64
	if err := s.db.QueryRow(ctx, countsSQL, userID).Scan(
		&out.Counts.Categories, &out.Counts.Tags, &out.Counts.Contacts,
		&out.Counts.ActiveEvents, &out.Counts.OpenDebts, &out.Counts.Budgets,
		&out.Counts.Goals.Count, &target, &current,
	); err != nil {
		return nil, fmt.Errorf("me counts: %w", err)
	}
	if target > 0 {
		out.Counts.Goals.ProgressPct = math.Round(current/target*10000) / 100
	}

	// Debts: the same totals as the Home debts tile.
	if s.debts != nil {
		r, err := s.debts.People(ctx, userID)
		if err != nil {
			return nil, err
		}
		out.Counts.DebtsSummary = DebtsSummary{OwedToMe: r.TotalOwedToMe, IOwe: r.TotalIOwe, Net: r.NetPosition}
	}
	// Budgets: every active personal budget at its own current period.
	today := time.Now()
	if s.budgets != nil {
		r, err := s.budgets.Overview(ctx, userID, "", budgets.ScopeUser, nil, headerTZ)
		if err != nil {
			return nil, err
		}
		out.Counts.BudgetsSummary = BudgetsSummary{
			TotalBudget: r.TotalBudget, TotalSpent: r.TotalSpent,
			UtilizationPct: r.OverallUtilizationPct, OverLimitCount: r.BudgetsOverLimit,
		}
		if loc, _, err := s.budgets.ResolveTimezone(ctx, userID, headerTZ); err == nil && loc != nil {
			today = today.In(loc)
		}
	}
	// Scheduled: active ones, and those due within the Home window.
	until := today.AddDate(0, 0, upcomingDays).Format("2006-01-02")
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE next_billing_date <= $2::date)
		FROM scheduled_transactions WHERE user_id = $1 AND status = 'active'`, userID, until).Scan(
		&out.Counts.Scheduled.ActiveCount, &out.Counts.Scheduled.UpcomingCount); err != nil {
		return nil, fmt.Errorf("me scheduled: %w", err)
	}
	return out, nil
}

type Handler struct {
	service *Service
}

func NewHandler(s *Service) *Handler { return &Handler{service: s} }

// GET /v1/me/counts — getMyCounts.
func (h *Handler) Counts(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	out, err := h.service.Counts(c.Request.Context(), userID, c.GetHeader("X-Timezone"))
	if err != nil {
		response.InternalError(c, "Failed to load counts", err.Error())
		return
	}
	c.JSON(http.StatusOK, out)
}
