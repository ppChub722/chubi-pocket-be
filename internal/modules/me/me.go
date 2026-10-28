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

type DebtsSummary = personal_debts.DebtTotals

type BudgetsSummary struct {
	TotalBudget    float64 `json:"total_budget"`
	TotalSpent     float64 `json:"total_spent"`
	UtilizationPct float64 `json:"utilization_pct"`
	OverLimitCount int     `json:"over_limit_count"`
}

type RecurringCount struct {
	ActiveCount   int `json:"active_count"`
	UpcomingCount int `json:"upcoming_count"` // occurrences in the next upcomingDays days
	// DueCount is the occurrences whose date has come and are not recorded yet;
	// NeedsAmountCount those of this month with no amount. Both are 0 until an
	// Expecter is set.
	DueCount         int `json:"due_count"`
	NeedsAmountCount int `json:"needs_amount_count"`
}

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
	Archived       int            `json:"archived"`
}

type MeCounts struct {
	Badges shared.Badges `json:"badges"`
	Counts Counts        `json:"counts"`
}

const upcomingDays = 14

type Service struct {
	db      *pgxpool.Pool
	budgets *budgets.Service
	debts   *personal_debts.Service
	// expected, when set, supplies the recurring occurrences behind the
	// recurring counts; nil keeps counting the stored recurring items.
	expected shared.Expecter
}

func (s *Service) WithExpected(e shared.Expecter) *Service { s.expected = e; return s }

// expectedStart is how far back overdue occurrences are asked for; the
// Expecter limits them to the setting's own start.
var expectedStart = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// expectedCounts fills the upcoming / due / needs-amount counts from the
// occurrences still to be recorded.
func (s *Service) expectedCounts(ctx context.Context, userID uuid.UUID, today time.Time, c *RecurringCount) error {
	day := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	until := day.AddDate(0, 0, upcomingDays)
	monthStart := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
	monthEnd := time.Date(day.Year(), day.Month()+1, 0, 0, 0, 0, 0, time.UTC)
	to := until
	if monthEnd.After(to) {
		to = monthEnd
	}
	occs, err := s.expected.ExpectedBetween(ctx, userID, expectedStart, to)
	if err != nil {
		return fmt.Errorf("me expected: %w", err)
	}
	for _, o := range occs {
		switch {
		case o.Due:
			c.DueCount++
		case !o.Date.After(until):
			c.UpcomingCount++
		}
		if o.Amount == 0 && !o.Date.Before(monthStart) && !o.Date.After(monthEnd) {
			c.NeedsAmountCount++
		}
	}
	return nil
}

func NewService(db *pgxpool.Pool) *Service { return &Service{db: db} }

func (s *Service) WithBudgets(b *budgets.Service) *Service      { s.budgets = b; return s }
func (s *Service) WithDebts(d *personal_debts.Service) *Service { s.debts = d; return s }

// archivedTotalSQL counts what the archived hub lists, group by group (see Archived).
var archivedTotalSQL = `(
	(SELECT COUNT(*) FROM accounts a WHERE a.status = 'archived' AND ` + shared.ActiveMembershipPredicate("a.id", "$1") + `)
	+ (SELECT COUNT(*) FROM budgets WHERE user_id = $1 AND status = 'archived')
	+ (SELECT COUNT(*) FROM saving_goals WHERE user_id = $1 AND status = 'archived')
	+ (SELECT COUNT(*) FROM projects p JOIN project_members m ON m.project_id = p.id AND m.user_id = $1 AND m.status = 'active'
	   WHERE p.status = 'archived' AND p.kind = 'event')
	+ (SELECT COUNT(*) FROM contacts WHERE user_id = $1 AND status = 'archived')
	+ (SELECT COUNT(*) FROM recurring_rules WHERE user_id = $1 AND status = 'archived'))`

var countsSQL = `SELECT
	(SELECT COUNT(*) FROM categories WHERE user_id = $1 AND is_system = FALSE),
	(SELECT COUNT(*) FROM tags WHERE user_id = $1),
	(SELECT COUNT(*) FROM contacts WHERE user_id = $1 AND status = 'active'),
	(SELECT COUNT(*) FROM projects p WHERE p.status = 'active' AND p.kind = 'event' AND (p.owner_user_id = $1 OR EXISTS (
		SELECT 1 FROM project_members m WHERE m.project_id = p.id AND m.user_id = $1 AND m.status = 'active'))),
	(SELECT COUNT(*) FROM (SELECT 1 FROM (` + shared.DebtLedgerSQL("$1") + `) l
		GROUP BY l.key
		HAVING ABS(SUM(l.signed)) > 0.005 OR BOOL_OR(NOT l.paid)) open_people),
	(SELECT COUNT(*) FROM budgets WHERE user_id = $1 AND status = 'active' AND closed_on IS NULL),
	(SELECT COUNT(*) FROM notifications WHERE recipient_user_id = $1 AND read_at IS NULL),
	(SELECT COUNT(*) FROM pending_transactions WHERE user_id = $1),
	` + archivedTotalSQL + `,
	(SELECT COUNT(*) FROM recurring_rules WHERE user_id = $1 AND status = 'active')`

func (s *Service) Counts(ctx context.Context, userID uuid.UUID, headerTZ string) (*MeCounts, error) {
	ctx = shared.WithExpectedMemo(ctx) // a read: the recurring counts and the budgets share one read of the occurrences
	out := &MeCounts{}
	if err := s.db.QueryRow(ctx, countsSQL, userID).Scan(
		&out.Counts.Categories, &out.Counts.Tags, &out.Counts.Contacts,
		&out.Counts.ActiveEvents, &out.Counts.OpenDebts, &out.Counts.Budgets,
		&out.Badges.UnreadNotifications, &out.Badges.PendingDrafts, &out.Counts.Archived,
		&out.Counts.Recurring.ActiveCount,
	); err != nil {
		return nil, fmt.Errorf("me counts: %w", err)
	}
	goals, err := shared.LoadSavingTotals(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}
	out.Counts.Goals = *goals

	if s.debts != nil {
		t, err := s.debts.Totals(ctx, userID)
		if err != nil {
			return nil, err
		}
		out.Counts.DebtsSummary = t
	}
	today := time.Now()
	if s.budgets != nil {
		if loc, _, err := s.budgets.ResolveTimezone(ctx, userID, headerTZ); err == nil && loc != nil {
			today = today.In(loc)
		}
	}
	if s.expected != nil {
		// the wide window first: the budgets below reuse its occurrences (shared.ExpectedMemo)
		if err := s.expectedCounts(ctx, userID, today, &out.Counts.Recurring); err != nil {
			return nil, err
		}
	} else {
		todayStr := today.Format("2006-01-02")
		until := today.AddDate(0, 0, upcomingDays).Format("2006-01-02")
		if err := s.db.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM recurring_items i LEFT JOIN recurring_rules r ON r.id = i.rule_id
		 WHERE i.user_id = $1 AND i.produced_at IS NULL AND i.pending_id IS NULL AND i.transaction_id IS NULL
		   AND NOT i.archived AND i.date > $2::date AND i.date <= $3::date
		   AND r.status = 'active')`, userID, todayStr, until).Scan(
			&out.Counts.Recurring.UpcomingCount); err != nil {
			return nil, fmt.Errorf("me recurring: %w", err)
		}
	}
	if s.budgets != nil {
		r, err := s.budgets.Totals(ctx, userID, headerTZ)
		if err != nil {
			return nil, err
		}
		out.Counts.BudgetsSummary = BudgetsSummary{
			TotalBudget: r.TotalBudget, TotalSpent: r.TotalSpent,
			UtilizationPct: r.UtilizationPct, OverLimitCount: r.OverLimitCount,
		}
	}
	return out, nil
}

type Handler struct {
	service *Service
}

func NewHandler(s *Service) *Handler { return &Handler{service: s} }

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
