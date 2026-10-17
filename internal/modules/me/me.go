// Package me serves the caller's shell data: GET /me/counts (badges + the
// counts and figures the More hub shows), in one round trip.
package me

import (
	"context"
	"fmt"
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

// DebtsSummary / BudgetsSummary / RecurringCount — the figures the More
// tiles show next to the counts (the same numbers as the Home tiles).
type DebtsSummary = personal_debts.DebtTotals

type BudgetsSummary struct {
	TotalBudget    float64 `json:"total_budget"`
	TotalSpent     float64 `json:"total_spent"`
	UtilizationPct float64 `json:"utilization_pct"`
	OverLimitCount int     `json:"over_limit_count"`
}

type RecurringCount struct {
	ActiveCount   int `json:"active_count"`
	UpcomingCount int `json:"upcoming_count"`
}

// Counts is spec schema MeCounts.counts.
type Counts struct {
	Categories   int                 `json:"categories"`
	Tags         int                 `json:"tags"`
	Contacts     int                 `json:"contacts"`
	ActiveEvents int                 `json:"active_events"`
	OpenDebts    int                 `json:"open_debts"`
	Budgets      int                 `json:"budgets"`
	Goals        shared.SavingTotals `json:"goals"`

	DebtsSummary   DebtsSummary   `json:"debts_summary"`
	BudgetsSummary BudgetsSummary `json:"budgets_summary"`
	Recurring      RecurringCount `json:"recurring"`
	// Archived — how many archived things there are in total (the ที่เก็บถาวร row);
	// the lists are GET /me/archived.
	Archived int `json:"archived"`
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
	due     shared.DueChecker
}

// WithDueCheck runs the recurring due check before the counts (the boot call).
func (s *Service) WithDueCheck(fn shared.DueChecker) *Service { s.due = fn; return s }

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
//   - open_debts: people with something open (debts_summary.open_count)
//   - budgets: active personal (scope user) budgets (the Home budgets tile)
//   - goals: active goals; progress = sum(min(current,target)) / sum(target)
//     with current = linked account balance * allocation_pct / 100
//   - debts_summary / budgets_summary: the Home debts / budgets tile numbers
//   - recurring: active recurring settings, and upcoming items within the Home
//     window (overdue included)
var countsSQL = `SELECT
	(SELECT COUNT(*) FROM categories WHERE user_id = $1 AND is_system = FALSE),
	(SELECT COUNT(*) FROM tags WHERE user_id = $1),
	(SELECT COUNT(*) FROM contacts WHERE user_id = $1 AND status = 'active'),
	(SELECT COUNT(*) FROM projects p WHERE p.status = 'active' AND (p.owner_user_id = $1 OR EXISTS (
		SELECT 1 FROM project_members m WHERE m.project_id = p.id AND m.user_id = $1 AND m.status = 'active'))),
	(SELECT COUNT(*) FROM (SELECT 1 FROM personal_debts pd WHERE pd.user_id = $1
		GROUP BY ` + shared.DebtKeySQL("pd") + `
		HAVING ABS(SUM(` + shared.DebtSignedSQL("pd") + `)) > 0.005 OR BOOL_OR(NOT pd.paid)) open_people),
	(SELECT COUNT(*) FROM budgets WHERE user_id = $1 AND status = 'active' AND closed_on IS NULL)`

func (s *Service) Counts(ctx context.Context, userID uuid.UUID, headerTZ string) (*MeCounts, error) {
	if s.due != nil {
		if err := s.due(ctx, userID, headerTZ); err != nil {
			return nil, err
		}
	}
	badges, err := shared.LoadBadges(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}
	out := &MeCounts{Badges: badges}
	if err := s.db.QueryRow(ctx, countsSQL, userID).Scan(
		&out.Counts.Categories, &out.Counts.Tags, &out.Counts.Contacts,
		&out.Counts.ActiveEvents, &out.Counts.OpenDebts, &out.Counts.Budgets,
	); err != nil {
		return nil, fmt.Errorf("me counts: %w", err)
	}
	arch, err := s.Archived(ctx, userID)
	if err != nil {
		return nil, err
	}
	out.Counts.Archived = arch.Total
	// Savings: the same numbers as the Home tile.
	goals, err := shared.LoadSavingTotals(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}
	out.Counts.Goals = *goals

	// Debts: the same totals as the Home debts tile.
	if s.debts != nil {
		t, err := s.debts.Totals(ctx, userID)
		if err != nil {
			return nil, err
		}
		out.Counts.DebtsSummary = t
	}
	// Budgets: every active personal budget at its own current period.
	today := time.Now()
	if s.budgets != nil {
		r, err := s.budgets.Totals(ctx, userID, headerTZ)
		if err != nil {
			return nil, err
		}
		out.Counts.BudgetsSummary = BudgetsSummary{
			TotalBudget: r.TotalBudget, TotalSpent: r.TotalSpent,
			UtilizationPct: r.UtilizationPct, OverLimitCount: r.OverLimitCount,
		}
		if loc, _, err := s.budgets.ResolveTimezone(ctx, userID, headerTZ); err == nil && loc != nil {
			today = today.In(loc)
		}
	}
	// Recurring: active settings, and upcoming items within the Home window.
	todayStr := today.Format("2006-01-02")
	until := today.AddDate(0, 0, upcomingDays).Format("2006-01-02")
	if err := s.db.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM recurring_rules WHERE user_id = $1 AND status = 'active'),
		(SELECT COUNT(*) FROM recurring_items i LEFT JOIN recurring_rules r ON r.id = i.rule_id
		 WHERE i.user_id = $1 AND i.produced_at IS NULL AND i.pending_id IS NULL AND i.transaction_id IS NULL
		   AND NOT i.archived AND i.date > $2::date AND i.date <= $3::date
		   AND (i.rule_id IS NULL OR r.status = 'active'))`, userID, todayStr, until).Scan(
		&out.Counts.Recurring.ActiveCount, &out.Counts.Recurring.UpcomingCount); err != nil {
		return nil, fmt.Errorf("me recurring: %w", err)
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
