package budgets

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
)

const (
	defaultTimezone = "Asia/Bangkok"
	defaultCurrency = "THB"
)

type Service struct {
	store *Store
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

// ResolveTimezone — hybrid policy. Header value wins for live read calls
// (handler passes it in); falls back to user_preferences.timezone; then
// to defaultTimezone. Returns the *time.Location loaded from tzdata.
func (s *Service) ResolveTimezone(
	ctx context.Context, userID uuid.UUID, headerTZ string,
) (*time.Location, string, error) {
	candidates := []string{headerTZ}
	stored, err := s.store.GetUserTimezone(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	candidates = append(candidates, stored, defaultTimezone)

	for _, name := range candidates {
		if name == "" {
			continue
		}
		if loc, err := time.LoadLocation(name); err == nil {
			return loc, name, nil
		}
	}
	// Last-ditch UTC — should never hit because Asia/Bangkok is in
	// every Go std-tzdata. If it does, surface a real error so the
	// FE knows the response is unreliable.
	loc, err := time.LoadLocation("UTC")
	return loc, "UTC", err
}

// Create — validates category + project ownership, fills currency
// default, inserts.
func (s *Service) Create(
	ctx context.Context, userID uuid.UUID, req CreateRequest, headerTZ string,
) (*BudgetView, error) {
	// 1. Project-scope must include project_id and caller must own it.
	//    (Schema CHECK enforces project_id presence; we validate ownership.)
	if req.Scope == ScopeProject {
		if req.ProjectID == nil {
			return nil, fmt.Errorf("project_id required for scope=project")
		}
		if err := s.store.VerifyProjectOwner(ctx, userID, *req.ProjectID); err != nil {
			return nil, err
		}
	} else if req.ProjectID != nil {
		// Reject mismatch at the API layer with a friendlier error than
		// the DB CHECK violation.
		return nil, fmt.Errorf("project_id must be null for scope=user")
	}

	// 2. Category must be expense-typed, owned, non-system.
	cat, err := s.store.VerifyExpenseCategory(ctx, userID, req.CategoryID)
	if err != nil {
		return nil, err
	}

	// 3. Currency: explicit or user-default or THB.
	currency := defaultCurrency
	if req.Currency != nil && *req.Currency != "" {
		currency = *req.Currency
	} else if userCur, _ := s.store.GetUserCurrency(ctx, userID); userCur != "" {
		currency = userCur
	}

	// 4. Insert.
	row, err := s.store.Create(ctx, userID, req, currency)
	if err != nil {
		return nil, err
	}

	view, err := s.buildView(ctx, userID, row, cat, headerTZ)
	if err != nil {
		return nil, err
	}
	return view, nil
}

func (s *Service) Get(
	ctx context.Context, userID, id uuid.UUID, headerTZ string,
) (*BudgetView, error) {
	row, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	cat, err := s.store.VerifyExpenseCategory(ctx, userID, row.CategoryID)
	if err != nil {
		// Budget anchor was archived — return a placeholder so the
		// shape stays stable.
		cat = &CategoryRef{ID: row.CategoryID, Name: "(archived)"}
	}
	return s.buildView(ctx, userID, row, cat, headerTZ)
}

func (s *Service) List(
	ctx context.Context, userID uuid.UUID, f ListFilter, headerTZ string,
) (*ListResponse, error) {
	rows, err := s.store.List(ctx, userID, f)
	if err != nil {
		return nil, err
	}
	out := make([]BudgetView, 0, len(rows))
	for i := range rows {
		row := rows[i]
		cat, catErr := s.store.VerifyExpenseCategory(ctx, userID, row.CategoryID)
		if catErr != nil {
			cat = &CategoryRef{ID: row.CategoryID, Name: "(archived)"}
		}
		v, err := s.buildView(ctx, userID, &row, cat, headerTZ)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return &ListResponse{Data: out}, nil
}

// authorizeMutation enforces the spec rule (§4.11):
//
//	user-scope    → only the budget's user_id may edit
//	project-scope → only the **current** project owner may edit; the
//	                budget's user_id is just the historical creator
//
// For user-scope rows that aren't owned by the caller, return
// NotFound — they shouldn't have been visible via GetByID anyway.
// For project-scope rows where caller is a member but not owner,
// return ErrProjectNotOwner so the FE can render a "viewer" state.
func (s *Service) authorizeMutation(
	ctx context.Context, callerID uuid.UUID, b *Budget,
) error {
	if b.Scope == ScopeUser {
		if b.UserID != callerID {
			return ErrBudgetNotFound
		}
		return nil
	}
	// Project-scope. Defensive: schema CHECK guarantees project_id is
	// set, but guard anyway so a future schema change doesn't NPE.
	if b.ProjectID == nil {
		return ErrBudgetNotFound
	}
	return s.store.VerifyProjectOwner(ctx, callerID, *b.ProjectID)
}

func (s *Service) Update(
	ctx context.Context, callerID, id uuid.UUID, req UpdateRequest, headerTZ string,
) (*BudgetView, error) {
	current, err := s.store.GetByID(ctx, callerID, id)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeMutation(ctx, callerID, current); err != nil {
		return nil, err
	}
	row, err := s.store.Update(ctx, callerID, id, req)
	if err != nil {
		return nil, err
	}
	cat, catErr := s.store.VerifyExpenseCategory(ctx, callerID, row.CategoryID)
	if catErr != nil {
		cat = &CategoryRef{ID: row.CategoryID, Name: "(archived)"}
	}
	return s.buildView(ctx, callerID, row, cat, headerTZ)
}

func (s *Service) Delete(ctx context.Context, callerID, id uuid.UUID) error {
	current, err := s.store.GetByID(ctx, callerID, id)
	if err != nil {
		return err
	}
	if err := s.authorizeMutation(ctx, callerID, current); err != nil {
		return err
	}
	return s.store.Delete(ctx, id)
}

func (s *Service) Archive(
	ctx context.Context, callerID, id uuid.UUID, headerTZ string,
) (*BudgetView, error) {
	return s.transitionStatus(ctx, callerID, id, StatusArchived, headerTZ)
}

func (s *Service) Restore(
	ctx context.Context, callerID, id uuid.UUID, headerTZ string,
) (*BudgetView, error) {
	return s.transitionStatus(ctx, callerID, id, StatusActive, headerTZ)
}

func (s *Service) transitionStatus(
	ctx context.Context, callerID, id uuid.UUID, target, headerTZ string,
) (*BudgetView, error) {
	current, err := s.store.GetByID(ctx, callerID, id)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeMutation(ctx, callerID, current); err != nil {
		return nil, err
	}
	row, err := s.store.SetStatus(ctx, callerID, id, target)
	if err != nil {
		return nil, err
	}
	cat, catErr := s.store.VerifyExpenseCategory(ctx, callerID, row.CategoryID)
	if catErr != nil {
		cat = &CategoryRef{ID: row.CategoryID, Name: "(archived)"}
	}
	return s.buildView(ctx, callerID, row, cat, headerTZ)
}

// Overview — aggregated picture of all caller's active budgets for a
// given period+scope.
func (s *Service) Overview(
	ctx context.Context, userID uuid.UUID, period, scope string,
	projectID *uuid.UUID, headerTZ string,
) (*OverviewResponse, error) {
	rows, err := s.store.List(ctx, userID, ListFilter{
		Status: StatusActive, Period: period, Scope: scope, ProjectID: projectID,
	})
	if err != nil {
		return nil, err
	}

	loc, _, err := s.ResolveTimezone(ctx, userID, headerTZ)
	if err != nil {
		return nil, err
	}
	pStart, pEnd := periodBounds(time.Now().In(loc), period)

	out := OverviewResponse{
		Period:      period,
		PeriodStart: pStart,
		PeriodEnd:   pEnd,
		Scope:       scope,
		ProjectID:   projectID,
		Budgets:     make([]OverviewItem, 0, len(rows)),
	}

	for i := range rows {
		row := rows[i]
		// Each budget's period might differ from `period` arg; use the
		// budget's own period to compute its bounds for accuracy.
		bStart, bEnd := periodBounds(time.Now().In(loc), row.Period)
		spent, err := s.store.ComputeSpent(ctx, userID, &row, bStart, bEnd)
		if err != nil {
			return nil, err
		}
		cat, catErr := s.store.VerifyExpenseCategory(ctx, userID, row.CategoryID)
		name := "(archived)"
		if catErr == nil {
			name = cat.Name
		}
		over := spent.Total > row.Amount
		util := 0.0
		if row.Amount > 0 {
			util = round2(spent.Total / row.Amount * 100)
		}
		out.Budgets = append(out.Budgets, OverviewItem{
			ID:             row.ID,
			CategoryName:   name,
			Amount:         row.Amount,
			Spent:          spent.Total,
			OverLimit:      over,
			UtilizationPct: util,
		})
		out.TotalBudget += row.Amount
		out.TotalSpent += spent.Total
		if over {
			out.BudgetsOverLimit++
		}
	}
	out.TotalRemaining = round2(out.TotalBudget - out.TotalSpent)
	if out.TotalBudget > 0 {
		out.OverallUtilizationPct = round2(out.TotalSpent / out.TotalBudget * 100)
	}
	return &out, nil
}

// --- helpers ---

func (s *Service) buildView(
	ctx context.Context, userID uuid.UUID, b *Budget, cat *CategoryRef, headerTZ string,
) (*BudgetView, error) {
	loc, _, err := s.ResolveTimezone(ctx, userID, headerTZ)
	if err != nil {
		return nil, err
	}
	pStart, pEnd := periodBounds(time.Now().In(loc), b.Period)

	spent, err := s.store.ComputeSpent(ctx, userID, b, pStart, pEnd)
	if err != nil {
		return nil, err
	}
	remaining := round2(b.Amount - spent.Total)
	util := 0.0
	if b.Amount > 0 {
		util = round2(spent.Total / b.Amount * 100)
	}
	return &BudgetView{
		Budget: *b,
		Category: CategorySummary{
			ID: cat.ID, Name: cat.Name, IconCode: cat.IconCode,
		},
		CurrentPeriod: CurrentPeriod{
			Start: pStart, End: pEnd,
			Spent:          round2(spent.Total),
			Remaining:      remaining,
			UtilizationPct: util,
			OverLimit:      spent.Total > b.Amount,
		},
		ChildBreakdown: spent.ChildBreakdown,
	}, nil
}

// periodBounds returns inclusive [start, end] dates (YYYY-MM-DD) for
// the given period containing `now`.
//
// - weekly: ISO week — Monday to Sunday.
// - monthly: 1st to last day of `now`'s month.
// - yearly: Jan 1 to Dec 31 of `now`'s year.
func periodBounds(now time.Time, period string) (string, string) {
	const layout = "2006-01-02"
	switch period {
	case PeriodWeekly:
		// Go's time.Weekday: Sunday=0, Monday=1, ..., Saturday=6.
		// Spec says Mon..Sun, so days-since-Monday = (weekday + 6) % 7.
		offset := (int(now.Weekday()) + 6) % 7
		monday := time.Date(now.Year(), now.Month(), now.Day()-offset, 0, 0, 0, 0, now.Location())
		sunday := monday.AddDate(0, 0, 6)
		return monday.Format(layout), sunday.Format(layout)
	case PeriodYearly:
		yearStart := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location())
		yearEnd := time.Date(now.Year(), 12, 31, 0, 0, 0, 0, now.Location())
		return yearStart.Format(layout), yearEnd.Format(layout)
	default: // monthly
		monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		monthEnd := monthStart.AddDate(0, 1, -1)
		return monthStart.Format(layout), monthEnd.Format(layout)
	}
}

func round2(f float64) float64 {
	return math.Round(f*100) / 100
}
