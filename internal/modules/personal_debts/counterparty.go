package personal_debts

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// 0.4.3: a debt counterparty is a person or an institution; its page lists its recurring items, the
// list shows each one's first open debts, an institution's page its card debts.

// WithCounterpartyRecurring wires the recurring module (it imports this one): a counterparty's
// recurring items for its page, and this month's pay / receive of the debt items for the hero.
func (s *Service) WithCounterpartyRecurring(items func(ctx context.Context, userID uuid.UUID, key string) (any, error),
	month func(ctx context.Context, userID uuid.UUID) (pay, receive float64, err error)) {
	s.recurringsFor, s.monthFor = items, month
}

const previewRows = 3

// fillPreviews — each row's first open debts (nearest due, then the largest), one query.
func (s *Store) fillPreviews(ctx context.Context, q shared.Querier, userID uuid.UUID, people []DebtPerson) error {
	if len(people) == 0 {
		return nil
	}
	keys := make([]string, len(people))
	at := map[string]int{}
	for i, p := range people {
		keys[i], at[p.Key] = p.Key, i
	}
	rows, err := q.Query(ctx, `SELECT x.key, x.id FROM (
			SELECT `+shared.DebtKeySQL("pd")+` AS key, pd.id,
			       ROW_NUMBER() OVER (PARTITION BY `+shared.DebtKeySQL("pd")+`
			                          ORDER BY pd.due_date ASC NULLS LAST, pd.amount DESC, pd.date DESC, pd.id) AS rn
			FROM personal_debts pd
			WHERE pd.user_id = $1 AND pd.kind = 'debt' AND NOT pd.paid AND `+shared.DebtKeySQL("pd")+` = ANY($2)
		) x WHERE x.rn <= $3 ORDER BY x.key, x.rn`, userID, keys, previewRows)
	if err != nil {
		return fmt.Errorf("debt previews: %w", err)
	}
	var (
		ids   []uuid.UUID
		owner = map[uuid.UUID]string{}
	)
	for rows.Next() {
		var (
			k  string
			id uuid.UUID
		)
		if err := rows.Scan(&k, &id); err != nil {
			rows.Close()
			return err
		}
		ids, owner[id] = append(ids, id), k
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return err
	}
	views, err := loadViews(ctx, q, `WHERE pd.user_id = $1 AND pd.id = ANY($2)`, userID, ids)
	if err != nil {
		return err
	}
	byID := map[uuid.UUID]DebtEntry{}
	for _, v := range views {
		byID[v.ID] = v
	}
	for _, id := range ids { // the query's order
		v, ok := byID[id]
		if !ok {
			continue
		}
		i := at[owner[id]]
		people[i].EntriesPreview = append(people[i].EntriesPreview, EntryPreview{ID: v.ID, Direction: v.Direction,
			Amount: v.Amount, Date: v.Date, DueDate: v.DueDate, Description: v.Description, Recurring: v.Recurring})
	}
	return nil
}

// cardDebts — "หนี้บัตรเครดิต" of an institution: each credit_card / pay_later wallet of it, what is
// owed on it now minus the principal of its card installments not due yet (the plans show those).
func (s *Store) cardDebts(ctx context.Context, q shared.Querier, userID, institutionID uuid.UUID) ([]CardDebt, error) {
	rows, err := q.Query(ctx, `SELECT a.id, -(`+shared.EffectiveBalanceSQL("a.balance", "a.id", "$1")+`)::float8,
			COALESCE((SELECT SUM(di.principal) FROM debt_installments di JOIN recurrings g ON g.id = di.recurring_id
			          WHERE g.card_account_id = a.id AND g.user_id = $1 AND di.paid_at IS NULL AND NOT di.skipped
			            AND di.due_date > `+todayInUserTZ+`), 0)::float8
		FROM accounts a
		WHERE a.institution_id = $2 AND a.type IN ('credit_card', 'pay_later') AND `+shared.ActiveMembershipPredicate("a.id", "$1")+`
		ORDER BY a.sort_order, a.created_at`, userID, institutionID)
	if err != nil {
		return nil, fmt.Errorf("card debts: %w", err)
	}
	type row struct {
		id             uuid.UUID
		owed, unbilled float64
	}
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.owed, &r.unbilled); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(list))
	for i, r := range list {
		ids[i] = r.id
	}
	refs, err := shared.LoadAccountRefs(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	out := []CardDebt{}
	for _, r := range list {
		ref, ok := refs[r.id]
		if !ok {
			continue
		}
		out = append(out, CardDebt{Account: ref, Amount: round2(math.Max(0, r.owed-r.unbilled))})
	}
	return out, nil
}

// monthTotals — the hero's this-month pay / receive of the debt items (recurring module).
func (s *Service) monthTotals(ctx context.Context, userID uuid.UUID, t *DebtTotals) error {
	if s.monthFor == nil {
		return nil
	}
	pay, receive, err := s.monthFor(ctx, userID)
	if err != nil {
		return err
	}
	t.PayThisMonth, t.ReceiveThisMonth = round2(pay), round2(receive)
	return nil
}

func isInstitutionKey(key string) bool { return strings.HasPrefix(key, "i:") }
