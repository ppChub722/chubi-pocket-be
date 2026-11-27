package recurring

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Rules live everywhere (spec §3): a wallet, goal or debt page lists the rules whose own columns
// touch it, grouped by their recurring. Nothing records where a rule was made.

// RuleFilter — GET /recurring-rules: a rule matches when ANY given column matches (a wallet page
// sends account_id and to_account_id with the same id). DebtKey — a debt page's counterparty key
// (c: / n: / i:).
type RuleFilter struct {
	AccountID     *uuid.UUID
	ToAccountID   *uuid.UUID
	GoalID        *uuid.UUID
	ContactID     *uuid.UUID
	InstitutionID *uuid.UUID
	DebtPlanID    *uuid.UUID
	DebtKey       string
}

func (f RuleFilter) empty() bool {
	return f.AccountID == nil && f.ToAccountID == nil && f.GoalID == nil && f.ContactID == nil &&
		f.InstitutionID == nil && f.DebtPlanID == nil && f.DebtKey == ""
}

// RecurringBrief — a group's head: the recurring the rules sit under.
type RecurringBrief struct {
	ID           uuid.UUID        `json:"id"`
	Name         string           `json:"name"`
	IconCode     *shared.IconCode `json:"icon_code"`
	TrackingType string           `json:"tracking_type"`
	Status       string           `json:"status"`
	RulesCount   int              `json:"rules_count"` // all its rules ("rule 1 จาก 2")
}

// RuleGroup — the matching rules of one recurring.
type RuleGroup struct {
	Recurring  RecurringBrief `json:"recurring"`
	Rules      []Line         `json:"rules"`
	MonthTotal float64        `json:"month_total"` // these rules' per-month total (active ones)
}

type RuleGroupList struct {
	Data []RuleGroup `json:"data"`
}

// ListRules — the rules (not archived) matching the filter, grouped by recurring, oldest recurring
// first; each group's rules in Rule n order.
func (s *Service) ListRules(ctx context.Context, userID uuid.UUID, f RuleFilter, tz string) (*RuleGroupList, error) {
	out := &RuleGroupList{Data: []RuleGroup{}}
	if f.empty() {
		return nil, ErrValidation
	}
	ctx = shared.WithRefCache(ctx)
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	var or []string
	args := []any{userID}
	add := func(col string, v any) {
		args = append(args, v)
		or = append(or, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if f.AccountID != nil {
		add("r.account_id", *f.AccountID)
	}
	if f.ToAccountID != nil {
		add("r.to_account_id", *f.ToAccountID)
	}
	if f.GoalID != nil {
		add("r.goal_id", *f.GoalID)
	}
	if f.ContactID != nil {
		add("r.contact_id", *f.ContactID)
	}
	if f.InstitutionID != nil {
		add("r.institution_id", *f.InstitutionID)
	}
	if f.DebtPlanID != nil {
		add("r.debt_plan_id", *f.DebtPlanID)
	}
	if f.DebtKey != "" {
		args = append(args, f.DebtKey)
		or = append(or, fmt.Sprintf("(r.type = 'debt' AND r.debt_plan_id IS NULL AND %s = $%d)", ruleKeySQL, len(args)))
	}
	rows, err := s.db.Query(ctx, `WITH m AS (SELECT r.id, ROW_NUMBER() OVER (ORDER BY g.created_at, g.id, r.created_at, r.id) AS n
			FROM recurring_rules r JOIN recurrings g ON g.id = r.recurring_id
			WHERE r.user_id = $1 AND r.status <> 'archived' AND g.status <> 'archived' AND (`+strings.Join(or, " OR ")+`))
		SELECT `+ruleColumns+` FROM recurring_rules JOIN m USING (id) ORDER BY m.n`, args...)
	if err != nil {
		return nil, fmt.Errorf("rules: %w", err)
	}
	var rules []*Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		rules = append(rules, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return out, nil
	}
	rv, views, err := s.ruleViewsOf(ctx, userID, today, rules, false)
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	for _, r := range rules {
		ids = append(ids, r.RecurringID)
	}
	heads := map[uuid.UUID]RecurringBrief{}
	hrows, err := s.db.Query(ctx, `SELECT g.id, g.name, g.icon_code, g.tracking_type, g.status,
		(SELECT COUNT(*) FROM recurring_rules x WHERE x.recurring_id = g.id)
		FROM recurrings g WHERE g.user_id = $1 AND g.id = ANY($2)`, userID, uniqueIDs(ids))
	if err != nil {
		return nil, fmt.Errorf("rule groups: %w", err)
	}
	for hrows.Next() {
		var (
			b    RecurringBrief
			icon []byte
		)
		if err := hrows.Scan(&b.ID, &b.Name, &icon, &b.TrackingType, &b.Status, &b.RulesCount); err != nil {
			hrows.Close()
			return nil, err
		}
		b.IconCode = shared.IconFromJSON(icon)
		heads[b.ID] = b
	}
	hrows.Close()
	if err := hrows.Err(); err != nil {
		return nil, err
	}
	at := map[uuid.UUID]int{}
	for i, r := range rules {
		k, ok := at[r.RecurringID]
		if !ok {
			k = len(out.Data)
			at[r.RecurringID] = k
			out.Data = append(out.Data, RuleGroup{Recurring: heads[r.RecurringID], Rules: []Line{}})
		}
		g := &out.Data[k]
		g.Rules = append(g.Rules, rv.lines[i])
		if v := views[i].MonthlyAmount; v != nil && r.Status == "active" {
			g.MonthTotal = math.Round((g.MonthTotal+*v)*100) / 100
		}
	}
	return out, nil
}

// CreateRule — a rule made from another page: into an existing recurring, or a new one named for
// that page (spec §3 "อยู่ในรายการประจำ").
func (s *Service) CreateRule(ctx context.Context, userID uuid.UUID, in RuleCreateInput, tz string) (*ItemPage, error) {
	switch {
	case in.RecurringID != nil && in.NewRecurring == nil:
		return s.AddLine(ctx, userID, *in.RecurringID, in.Rule, tz)
	case in.RecurringID == nil && in.NewRecurring != nil:
		nr := *in.NewRecurring
		nr.Lines = []LineInput{in.Rule}
		return s.CreateRecurring(ctx, userID, nr, tz)
	}
	return nil, ErrValidation
}

// ---- handlers ----

func (h *Handler) ListRules(c *gin.Context) {
	uid, ok := user(c)
	if !ok {
		return
	}
	var f RuleFilter
	for _, p := range []struct {
		name string
		dst  **uuid.UUID
	}{{"account_id", &f.AccountID}, {"to_account_id", &f.ToAccountID}, {"goal_id", &f.GoalID}, {"contact_id", &f.ContactID},
		{"institution_id", &f.InstitutionID}, {"debt_plan_id", &f.DebtPlanID}} {
		id, ok := optionalID(c, p.name)
		if !ok {
			return
		}
		*p.dst = id
	}
	if k := c.Query("debt_key"); k != "" {
		if _, err := shared.ParseDebtKey(k); err != nil && !strings.HasPrefix(k, "i:") {
			response.Validation(c, "Invalid debt_key", apierr.Field("debt_key", apierr.FieldInvalid))
			return
		}
		f.DebtKey = k
	}
	if f.empty() {
		response.Validation(c, "Send at least one filter", apierr.Field("account_id", apierr.FieldRequired))
		return
	}
	out, err := h.service.ListRules(c.Request.Context(), uid, f, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) CreateRule(c *gin.Context) {
	uid, ok := user(c)
	if !ok {
		return
	}
	var in RuleCreateInput
	if _, ok := bind(c, &in); !ok {
		return
	}
	out, err := h.service.CreateRule(c.Request.Context(), uid, in, tz(c))
	reply(c, http.StatusCreated, out, err)
}
