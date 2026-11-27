package recurring

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/institutions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// ---- validation of a recurring / rule input ----

// applyItemInput — the recurring's own fields (a full replace). Checks what needs no DB.
func applyItemInput(it *itemRecord, in *RecurringInput) error {
	if in.Name == nil {
		return ErrValidation
	}
	name := strings.TrimSpace(*in.Name)
	if name == "" || len([]rune(name)) > 100 {
		return ErrValidation
	}
	if in.Description != nil && len([]rune(*in.Description)) > 200 || in.Note != nil && len([]rune(*in.Note)) > 500 {
		return ErrValidation
	}
	tt := "other"
	if in.TrackingType != nil {
		if !trackingTypes[*in.TrackingType] {
			return ErrValidation
		}
		tt = *in.TrackingType
	}
	it.Name, it.Description, it.Note, it.IconCode, it.TrackingType = name, in.Description, in.Note, in.IconCode, tt
	return nil
}

// resolveTargetTx — a debt rule's counterparty name (a contact's live name, an institution's name)
// and plan, checked to be mine and active.
func resolveTargetTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, t *ruleTarget) error {
	t.counterpartName = ""
	if t.DebtPlanID != nil {
		p, err := getPlan(ctx, tx, userID, *t.DebtPlanID, false)
		if err != nil {
			return err
		}
		if p.isCard() { // a card plan is paid by its card's bill
			return apierr.Wrap(ErrValidation, "a card plan takes no rule: pay the card bill",
				apierr.Field("debt_plan_id", apierr.FieldInvalid))
		}
		if p.ClosedAt != nil {
			return ErrInvalidTransition
		}
		t.setPlan(p)
	}
	switch {
	case t.InstitutionID != nil:
		if err := institutions.CheckOwned(ctx, tx, userID, *t.InstitutionID, true); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT name FROM institutions WHERE id = $1`, *t.InstitutionID).Scan(&t.counterpartName)
	case t.ContactID != nil:
		r := &Rule{ContactID: t.ContactID}
		if err := checkDebtPersonTx(ctx, tx, userID, r); err != nil {
			return err
		}
		t.counterpartName = *r.PersonName
		t.PersonName = r.PersonName // the name is kept with the contact (a deleted contact leaves it)
	case t.PersonName != nil:
		t.counterpartName = *t.PersonName
	}
	return nil
}

// applyLineInputTx — a rule input onto r (a new rule, or the rule it updates; a full replace). The
// schedule is required on a new rule; absent on an update = kept.
func (s *Service) applyLineInputTx(ctx context.Context, tx pgx.Tx, it *itemRecord, r *Rule, in LineInput, today time.Time) error {
	isNew := r.ID == uuid.Nil
	var t ruleTarget
	if err := applyTargetInput(&t, &in); err != nil {
		return err
	}
	if err := resolveTargetTx(ctx, tx, it.UserID, &t); err != nil {
		return err
	}
	// repay_paid_by: pays back the person of a person-paid rule of the same recurring
	r.RepaysRuleID = nil
	if in.RepaysRuleID != nil {
		if t.Type != TypeExpense || in.Source.PaidBy != nil || (r.ID != uuid.Nil && *in.RepaysRuleID == r.ID) {
			return ErrSourceInvalid
		}
		o, err := getRule(ctx, tx, it.UserID, *in.RepaysRuleID, false)
		if errors.Is(err, ErrNotFound) || (err == nil && (o.RecurringID != it.ID || o.PaidByPersonName == nil)) {
			return apierr.Wrap(ErrValidation, "repays_rule_id is not a person-paid rule of this recurring",
				apierr.Field("repays_rule_id", apierr.FieldInvalid))
		}
		if err != nil {
			return err
		}
		r.RepaysRuleID, r.ContactID, r.PersonName = &o.ID, o.PaidByContactID, o.PaidByPersonName
	}
	r.DebtKind = nil
	if t.Type == TypeDebt && t.DebtPlanID == nil && in.DebtKind != nil {
		if *in.DebtKind != DebtKindDebt && *in.DebtKind != DebtKindRepayment {
			return ErrValidation
		}
		r.DebtKind = in.DebtKind
	}
	itemToLine(it, &t, r)
	// source
	src := in.Source
	if src.AccountID != nil && src.PaidBy != nil {
		return ErrSourceInvalid
	}
	r.AccountID, r.PaidByContactID, r.PaidByPersonName = src.AccountID, nil, nil
	if src.PaidBy != nil {
		if !paidByAllowed(t.Type) {
			return ErrSourceInvalid
		}
		p := src.PaidBy
		switch {
		case p.ContactID != nil:
			pr := &Rule{ContactID: p.ContactID}
			if err := checkDebtPersonTx(ctx, tx, it.UserID, pr); err != nil {
				return err
			}
			r.PaidByContactID, r.PaidByPersonName = p.ContactID, pr.PersonName
		case p.PersonName != nil && strings.TrimSpace(*p.PersonName) != "" && len([]rune(strings.TrimSpace(*p.PersonName))) <= 100:
			n := strings.TrimSpace(*p.PersonName)
			r.PaidByPersonName = &n
		default:
			return ErrSourceInvalid
		}
	}
	// neither: a rule without a wallet (confirm asks for one, auto-accept leaves it out; a debt rule
	// books ledger-only rows; saving answers SAVING_NEEDS_WALLET)
	// amount: fixed, or asked on each round (expense / transfer / a person repayment; no amount sent =
	// ask; an amount sent with ask is the hint each round starts with). A plan's amount follows its
	// installments, a payback the latest round of the rule it repays.
	mode := AmountFixed
	switch {
	case in.AmountMode != nil && *in.AmountMode != AmountFixed && *in.AmountMode != AmountAsk:
		return ErrValidation
	case r.RepaysRuleID != nil || t.DebtPlanID != nil:
	case in.AmountMode != nil:
		mode = *in.AmountMode
	case in.Amount == nil:
		mode = AmountAsk
	}
	if mode == AmountAsk {
		personRepay := t.Type == TypeDebt && r.DebtKind != nil && *r.DebtKind == DebtKindRepayment && t.InstitutionID == nil
		if t.Type != TypeExpense && t.Type != TypeTransfer && !personRepay {
			return ErrValidation
		}
	}
	r.AmountMode = mode
	switch {
	case in.Amount != nil && *in.Amount < 0:
		return ErrValidation
	case t.DebtPlanID != nil:
		v, err := nextInstallmentAmount(ctx, tx, *t.DebtPlanID)
		if err != nil {
			return err
		}
		r.Amount = v
	case in.Amount != nil:
		r.Amount = math.Round(*in.Amount*100) / 100 // ask: a hint the round starts with
	case mode == AmountAsk || r.RepaysRuleID != nil:
		r.Amount = 0
	default:
		return ErrValidation
	}
	r.ToAccountID = nil
	if t.Type == TypeTransfer {
		r.ToAccountID = in.ToAccountID
	}
	// switches: null = the user's default; auto-accept never runs on an asked amount
	r.Notify = in.Notify
	auto := in.AutoAccept
	if mode == AmountAsk {
		auto = nil
	}
	if isNew || !sameBool(auto, r.AutoAccept) {
		yesterday := today.AddDate(0, 0, -1)
		r.AutoAcceptedUntil = &yesterday // switching on / off never back-books
	}
	r.AutoAccept = auto
	if isNew {
		if in.Schedule == nil {
			return ErrValidation
		}
		r.Status, r.TrackedFrom, r.DueNotifiedUntil = "active", today, &today
		if r.TagIDs == nil {
			r.TagIDs = []uuid.UUID{}
		}
	}
	if sc := in.Schedule; sc != nil {
		start, err := parseDate(sc.StartDate)
		if err != nil {
			return ErrValidation
		}
		r.StartDate = start
		if err := r.applyRule(sc.RecurrenceRule); err != nil {
			return err
		}
		if err := r.applyEnd(sc.End); err != nil {
			return err
		}
		r.GeneratedUntil, r.TotalCount = nil, nil
	}
	r.Kind = lineKind(&t, r.EndKind, r.AmountMode)
	if r.RepaysRuleID != nil {
		r.Kind = KindDebt
	}
	if err := r.finishKind(&r.Kind); err != nil {
		return err
	}
	if err := r.limits(today); err != nil {
		return err
	}
	if r.PaidByPersonName != nil {
		// a person-paid round moves no wallet money: the rule carries no wallet
		if err := checkRefsTx(ctx, tx, it.UserID, "expense", nil, nil, r.CategoryID, r.TagIDs); err != nil {
			return err
		}
		if r.Type == typeDebt {
			return checkDebtPersonTx(ctx, tx, it.UserID, r)
		}
		return nil
	}
	return checkRuleRefsTx(ctx, tx, it.UserID, r)
}

func sameBool(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ---- create / update ----

// CreateRecurring — a recurring with its rules, in one call (the create page saves its draft rules
// together).
func (s *Service) CreateRecurring(ctx context.Context, userID uuid.UUID, in RecurringInput, tz string) (*ItemPage, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	it := &itemRecord{UserID: userID, Status: "active"}
	if err := applyItemInput(it, &in); err != nil {
		return nil, err
	}
	if len(in.Lines) == 0 || len(in.Lines) > 20 {
		return nil, ErrValidation
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := saveItemRecordTx(ctx, tx, it); err != nil {
		return nil, err
	}
	for i, li := range in.Lines {
		r := &Rule{}
		if err := s.applyLineInputTx(ctx, tx, it, r, li, today); err != nil {
			return nil, err
		}
		if i == 0 {
			r.ID = it.ID // the first rule shares the recurring's id (old links open the right one)
			if err := insertRuleTx(ctx, tx, r); err != nil {
				return nil, err
			}
		} else if err := saveRuleTx(ctx, tx, r); err != nil {
			return nil, err
		}
	}
	if err := refreshItemStatusTx(ctx, tx, userID, it.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.afterItemWrite(ctx, userID, it.ID, tz, nil)
}

// UpdateRecurring — the recurring's own fields (full replace) and, when sent, its rules (with id =
// update, without = new, missing = deleted).
func (s *Service) UpdateRecurring(ctx context.Context, userID, id uuid.UUID, in RecurringInput, tz string) (*ItemPage, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	it, err := getItemRecord(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	if err := applyItemInput(it, &in); err != nil {
		return nil, err
	}
	if err := saveItemRecordTx(ctx, tx, it); err != nil {
		return nil, err
	}
	if in.has("rules") {
		if len(in.Lines) == 0 || len(in.Lines) > 20 {
			return nil, ErrValidation
		}
		lines, err := linesOf(ctx, tx, userID, []uuid.UUID{id}, true)
		if err != nil {
			return nil, err
		}
		byID := map[uuid.UUID]*Rule{}
		for _, r := range lines {
			byID[r.ID] = r
		}
		keep := map[uuid.UUID]bool{}
		for _, li := range in.Lines {
			r := &Rule{}
			if li.ID != nil {
				old, ok := byID[*li.ID]
				if !ok {
					return nil, ErrLineNotFound
				}
				r = old
				keep[r.ID] = true
			}
			if err := s.applyLineInputTx(ctx, tx, it, r, li, today); err != nil {
				return nil, err
			}
			if err := saveRuleTx(ctx, tx, r); err != nil {
				return nil, err
			}
		}
		for _, r := range lines {
			if !keep[r.ID] {
				if _, err := tx.Exec(ctx, `DELETE FROM recurring_rules WHERE id = $1 AND user_id = $2`, r.ID, userID); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := refreshItemStatusTx(ctx, tx, userID, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.afterItemWrite(ctx, userID, id, tz, nil)
}

func (s *Service) afterItemWrite(ctx context.Context, userID, id uuid.UUID, tz string, touched []uuid.UUID) (*ItemPage, error) {
	if err := s.DueCheck(ctx, userID, tz); err != nil {
		return nil, err
	}
	p, err := s.GetRecurring(ctx, userID, id, tz)
	if err != nil {
		return nil, err
	}
	if p.AccountBalances, err = shared.LoadWalletBalances(ctx, s.db, uniqueIDs(touched)); err != nil {
		return nil, err
	}
	p.Effects.AccountBalances = p.AccountBalances
	return p, nil
}

// ---- reads ----

func (s *Service) GetRecurring(ctx context.Context, userID, id uuid.UUID, tz string) (*ItemPage, error) {
	ctx = shared.WithRefCache(ctx)
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	it, err := getItemRecord(ctx, s.db, userID, id, false)
	if err != nil {
		return nil, err
	}
	pages, err := s.itemPages(ctx, userID, today, []*itemRecord{it}, true)
	if err != nil {
		return nil, err
	}
	return &pages[0], nil
}

// userDefaults — the user's rule switch defaults (notify, auto-accept).
func userDefaults(ctx context.Context, q rowQuerier, userID uuid.UUID) (bool, bool, error) {
	var n, a bool
	err := q.QueryRow(ctx, `SELECT recurring_notify_default, recurring_auto_accept_default FROM users WHERE id = $1`, userID).Scan(&n, &a)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, false, nil
	}
	return n, a, err
}

// notifyOn / autoAcceptOn — a rule's switches in effect (auto-accept never on an asked amount).
func (r *Rule) notifyOn(def bool) bool {
	if r.Notify != nil {
		return *r.Notify
	}
	return def
}

func (r *Rule) autoAcceptOn(def bool) bool {
	if r.amountMode() == AmountAsk {
		return false
	}
	if r.AutoAccept != nil {
		return *r.AutoAccept
	}
	return def
}

// ruleViews — the page-ready views of rules (any recurrings), in rules' order, each with its Rule n.
type ruleViews struct {
	lines []Line
	plans map[uuid.UUID]*planRecord
}

// complete — rules holds every rule of its recurrings, oldest first (Rule n is worked out here, no read).
func (s *Service) ruleViewsOf(ctx context.Context, userID uuid.UUID, today time.Time, rules []*Rule, complete bool) (*ruleViews, []Recurring, error) {
	out := &ruleViews{lines: make([]Line, 0, len(rules)), plans: map[uuid.UUID]*planRecord{}}
	if len(rules) == 0 {
		return out, nil, nil
	}
	views, err := s.recurringViews(ctx, userID, today, rules)
	if err != nil {
		return nil, nil, err
	}
	var catIDs, tagIDs, goalIDs, planIDs []uuid.UUID
	for _, r := range rules {
		if r.CategoryID != nil {
			catIDs = append(catIDs, *r.CategoryID)
		}
		if r.GoalID != nil {
			goalIDs = append(goalIDs, *r.GoalID)
		}
		if r.DebtPlanID != nil {
			planIDs = append(planIDs, *r.DebtPlanID)
		}
		tagIDs = append(tagIDs, r.TagIDs...)
	}
	cats, err := shared.LoadCategoryRefs(ctx, s.db, catIDs)
	if err != nil {
		return nil, nil, err
	}
	tags, err := shared.LoadTagRefs(ctx, s.db, userID, tagIDs)
	if err != nil {
		return nil, nil, err
	}
	goals, err := shared.LoadSavingGoalRefs(ctx, s.db, goalIDs)
	if err != nil {
		return nil, nil, err
	}
	if out.plans, err = plansByID(ctx, s.db, userID, planIDs); err != nil {
		return nil, nil, err
	}
	cps, err := s.counterparties(ctx, userID, rules)
	if err != nil {
		return nil, nil, err
	}
	paidBy, err := s.paidByPersons(ctx, userID, rules)
	if err != nil {
		return nil, nil, err
	}
	defNotify, defAuto, err := userDefaults(ctx, s.db, userID)
	if err != nil {
		return nil, nil, err
	}
	seqs := map[uuid.UUID]int{}
	if complete {
		n := map[uuid.UUID]int{}
		for _, r := range rules {
			n[r.RecurringID]++
			seqs[r.ID] = n[r.RecurringID]
		}
	} else if seqs, err = ruleSeqs(ctx, s.db, userID, rules); err != nil {
		return nil, nil, err
	}
	// an asked person repayment fills the net only when it is that person's only rule (spec §2.1)
	perPerson := map[string]int{}
	if err := s.personRuleCounts(ctx, userID, rules, perPerson); err != nil {
		return nil, nil, err
	}
	for i, r := range rules {
		v := &views[i]
		ln := lineOf(r, v, paidBy)
		ln.Seq = seqs[r.ID]
		ln.NotifyOn, ln.AutoAcceptOn = r.notifyOn(defNotify), r.autoAcceptOn(defAuto)
		if r.CategoryID != nil {
			if x, ok := cats[*r.CategoryID]; ok {
				ln.Category = &x
			}
		}
		for _, t := range r.TagIDs {
			if x, ok := tags[t]; ok {
				ln.Tags = append(ln.Tags, x)
			}
		}
		if r.GoalID != nil {
			if x, ok := goals[*r.GoalID]; ok {
				ln.Goal = &x
			}
			ln.EndText = ptr("goal_reached")
		}
		if c, ok := cps[r.ID]; ok {
			ln.Counterparty = &c
			t := targetOf(r)
			ln.DebtDirection = t.DebtDirection
			if r.DebtKind != nil && *r.DebtKind == DebtKindRepayment && r.DebtPlanID == nil {
				ln.EndText = ptr("debt_cleared")
			}
			if r.amountMode() == AmountAsk && perPerson[c.Key] > 1 {
				net := math.Abs(c.Net)
				ln.PrefillHint = &net
			}
		}
		out.lines = append(out.lines, ln)
	}
	return out, views, nil
}

// ruleSeqs — each rule's place among its recurring's rules (1-based, oldest first).
func ruleSeqs(ctx context.Context, q shared.Querier, userID uuid.UUID, rules []*Rule) (map[uuid.UUID]int, error) {
	var items []uuid.UUID
	for _, r := range rules {
		items = append(items, r.RecurringID)
	}
	out := map[uuid.UUID]int{}
	rows, err := q.Query(ctx, `SELECT id, ROW_NUMBER() OVER (PARTITION BY recurring_id ORDER BY created_at, id)
		FROM recurring_rules WHERE user_id = $1 AND recurring_id = ANY($2)`, userID, uniqueIDs(items))
	if err != nil {
		return nil, fmt.Errorf("rule seqs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id uuid.UUID
			n  int64
		)
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = int(n)
	}
	return out, rows.Err()
}

// personRuleCounts — the user's live debt rules per person key, for the keys these rules touch.
func (s *Service) personRuleCounts(ctx context.Context, userID uuid.UUID, rules []*Rule, out map[string]int) error {
	var keys []string
	for _, r := range rules {
		if r.Type == typeDebt && r.InstitutionID == nil && r.DebtPlanID == nil && r.amountMode() == AmountAsk {
			keys = append(keys, shared.DebtKey(r.ContactID, derefStr(r.PersonName)))
		}
	}
	if len(keys) == 0 {
		return nil
	}
	rows, err := s.db.Query(ctx, `SELECT k, COUNT(*) FROM (SELECT CASE WHEN contact_id IS NOT NULL THEN 'c:' || contact_id::text
			ELSE 'n:' || LOWER(TRIM(person_name)) END AS k
		FROM recurring_rules WHERE user_id = $1 AND type = 'debt' AND institution_id IS NULL AND debt_plan_id IS NULL
			AND status <> 'archived') x WHERE k = ANY($2) GROUP BY k`, userID, keys)
	if err != nil {
		return fmt.Errorf("person rules: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			k string
			n int
		)
		if err := rows.Scan(&k, &n); err != nil {
			return err
		}
		out[k] = n
	}
	return rows.Err()
}

// touchesOf — what a recurring's rules touch on other pages (spec §3 "รายการประจำ has 2 sections").
func touchesOf(lines []Line, plans map[uuid.UUID]*planRecord) []Touch {
	out := []Touch{}
	seen := map[string]bool{}
	add := func(t Touch) {
		k := t.Kind + t.ID.String() + t.Key
		if !seen[k] {
			seen[k] = true
			out = append(out, t)
		}
	}
	for _, ln := range lines {
		if ln.Goal != nil {
			add(Touch{Kind: "goal", ID: ln.Goal.ID, Name: ln.Goal.Name})
		}
		if ln.DebtPlanID != nil {
			name := ""
			if p, ok := plans[*ln.DebtPlanID]; ok {
				name = p.Name
			}
			add(Touch{Kind: "plan", ID: *ln.DebtPlanID, Name: name})
		} else if c := ln.Counterparty; c != nil {
			t := Touch{Kind: c.Kind, Key: c.Key, Name: c.Name}
			if c.Institution != nil {
				t.ID = c.Institution.ID
			} else if c.Person != nil && c.Person.ContactID != nil {
				t.ID = *c.Person.ContactID
			}
			add(t)
		}
		if ln.ToAccount != nil && (ln.ToAccount.Type == "credit_card" || ln.ToAccount.Type == "pay_later") {
			add(Touch{Kind: "card", ID: ln.ToAccount.ID, Name: ln.ToAccount.Name})
		}
	}
	return out
}

// itemPages — the page of each recurring (full = stats and plan: the detail page; else the list
// row's needs).
func (s *Service) itemPages(ctx context.Context, userID uuid.UUID, today time.Time, items []*itemRecord, full bool) ([]ItemPage, error) {
	out := make([]ItemPage, 0, len(items))
	if len(items) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	lines, err := linesOf(ctx, s.db, userID, ids, false)
	if err != nil {
		return nil, err
	}
	rv, views, err := s.ruleViewsOf(ctx, userID, today, lines, true)
	if err != nil {
		return nil, err
	}
	byItem := map[uuid.UUID][]int{}
	for i, r := range lines {
		byItem[r.RecurringID] = append(byItem[r.RecurringID], i)
	}
	for _, it := range items {
		p := ItemPage{ID: it.ID, Name: it.Name, Description: it.Description, Note: it.Note, IconCode: it.IconCode,
			TrackingType: it.TrackingType, Status: it.Status, Lines: []Line{}, CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt,
			Stats: ItemStats{Series: []Point{}}, AccountBalances: []shared.WalletBalance{}, Effects: shared.NewEffects()}
		allDone := len(byItem[it.ID]) > 0
		var own []*Rule
		var plan *planRecord
		for _, i := range byItem[it.ID] {
			own = append(own, lines[i])
			if r := lines[i]; plan == nil && r.DebtPlanID != nil {
				plan = rv.plans[*r.DebtPlanID]
			}
		}
		it.Status = effectiveStatus(it.Status, own)
		p.Status = it.Status
		for _, i := range byItem[it.ID] {
			r, v, ln := lines[i], views[i], rv.lines[i]
			p.Lines = append(p.Lines, ln)
			p.DueCount += v.DueCount
			if v.MonthlyAmount != nil && r.Status == "active" {
				p.MonthTotal += *v.MonthlyAmount
			}
			if ln.Status != "done" {
				allDone = false
			}
			if v.NextDate != nil && r.Status == "active" {
				d := *v.DaysUntil
				if p.Next == nil || *v.NextDate < p.Next.Date {
					p.Next = &NextRound{RecurringRuleID: r.ID, Date: *v.NextDate, Amount: v.NextAmount, DaysUntil: d}
				}
			}
		}
		p.Touches = touchesOf(p.Lines, rv.plans)
		p.MonthTotal = math.Round(p.MonthTotal*100) / 100
		if allDone && it.Status != "archived" {
			p.Status = "done"
		}
		p.Actions = ItemActions{
			Pause: it.Status == "active", Resume: it.Status == "paused", Archive: it.Status != "archived",
			Restore: it.Status == "archived", Delete: true, AddLine: it.Status != "archived",
		}
		if full {
			if err := s.fillItemFull(ctx, userID, today, lines, byItem[it.ID], &p); err != nil {
				return nil, err
			}
		}
		if plan != nil {
			if p.Plan, err = s.planView(ctx, s.db, userID, plan, today); err != nil {
				return nil, err
			}
		}
		out = append(out, p)
	}
	return out, nil
}

func (s *Service) fillItemFull(ctx context.Context, userID uuid.UUID, today time.Time, lines []*Rule, idx []int, p *ItemPage) error {
	var pts []Point
	left, allEnd := 0.0, len(idx) > 0
	for _, i := range idx {
		r := lines[i]
		stored, err := s.storedRows(ctx, s.db, userID, []uuid.UUID{r.ID}, nil, nil)
		if err != nil {
			return err
		}
		st, err := s.stats(ctx, userID, r, stored, today)
		if err != nil {
			return err
		}
		p.Stats.PaidCount += st.PaidCount
		p.Stats.PaidTotal += st.PaidTotal
		if st.RemainingTotal == nil {
			allEnd = false
		} else {
			left += *st.RemainingTotal
		}
		pts = append(pts, st.Series...)
	}
	p.Stats.PaidTotal = math.Round(p.Stats.PaidTotal*100) / 100
	if allEnd {
		l := math.Round(left*100) / 100
		p.Stats.LeftTotal = &l
	}
	sort.SliceStable(pts, func(a, b int) bool { return pts[a].Date < pts[b].Date })
	if n := len(pts); n > 0 {
		var sum, mx float64
		for _, x := range pts {
			sum += x.Amount
			mx = math.Max(mx, x.Amount)
		}
		avg := math.Round(sum/float64(n)*100) / 100
		last := pts[n-1].Amount
		ld := pts[n-1].Date
		p.Stats.AvgAmount, p.Stats.LastAmount, p.Stats.MaxAmount, p.Stats.LastDate = &avg, &last, &mx, &ld
		if n > 24 {
			pts = pts[n-24:]
		}
		p.Stats.Series = pts
	}
	return nil
}

// lineOf — a rule's view from the engine's view of it.
func lineOf(r *Rule, v *Recurring, paidBy map[uuid.UUID]shared.PersonRef) Line {
	end := v.End
	mode := r.amountMode()
	switch {
	case r.DebtPlanID != nil:
		mode = "schedule"
	case r.RepaysRuleID != nil:
		mode = "follow"
	}
	ln := Line{ID: r.ID, RecurringID: r.RecurringID, Type: r.Type, CategoryID: r.CategoryID, TagIDs: append([]uuid.UUID{}, r.TagIDs...),
		Tags: []shared.TagRef{}, GoalID: r.GoalID, DebtPlanID: r.DebtPlanID,
		Amount: v.Amount, AmountMode: mode, Action: r.action(), RepaysRuleID: r.RepaysRuleID, Account: v.Account,
		ToAccountID: r.ToAccountID, ToAccount: v.ToAccount,
		DebtKind: r.DebtKind, Schedule: Schedule{RecurrenceRule: v.Rule, StartDate: v.StartDate, End: &end},
		Notify: r.Notify, AutoAccept: r.AutoAccept, Status: r.Status, PauseReason: v.PauseReason, NextDate: v.NextDate,
		NextAmount: v.NextAmount, DaysUntil: v.DaysUntil, DueCount: v.DueCount, MonthlyAmount: v.MonthlyAmount, YearlyCost: v.YearlyCost,
		Progress: v.Progress, Chain: v.Chain, EndingSoon: v.EndingSoon, Counts: v.Counts, CurrentOccurrence: v.CurrentOccurrence,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		Source: LineSource{AccountID: r.AccountID},
	}
	if r.PaidByPersonName != nil {
		ln.Source.PaidBy = &PersonInput{ContactID: r.PaidByContactID, PersonName: r.PaidByPersonName}
		pr := shared.NamePerson(*r.PaidByPersonName)
		if r.PaidByContactID != nil {
			if x, ok := paidBy[*r.PaidByContactID]; ok {
				pr = x
			}
		}
		ln.PaidBy = &pr
	}
	if r.TotalCount != nil && v.Counts.Upcoming == 0 && v.DueCount == 0 && r.Status != "archived" {
		ln.Status = "done" // ครบแล้ว: every round recorded or skipped
	}
	ln.Actions = LineActions{Pause: r.Status == "active", Resume: r.Status == "paused", Archive: r.Status != "archived",
		Restore: r.Status == "archived", Extend: v.Actions.Extend, Delete: true}
	return ln
}

func (s *Service) paidByPersons(ctx context.Context, userID uuid.UUID, lines []*Rule) (map[uuid.UUID]shared.PersonRef, error) {
	var ids []uuid.UUID
	for _, r := range lines {
		if r.PaidByContactID != nil {
			ids = append(ids, *r.PaidByContactID)
		}
	}
	return shared.LoadPersons(ctx, s.db, userID, ids)
}

// counterparties — each debt rule's counterparty (a plan's: its creditor), with its balance now.
func (s *Service) counterparties(ctx context.Context, userID uuid.UUID, rules []*Rule) (map[uuid.UUID]Counterparty, error) {
	out := map[uuid.UUID]Counterparty{}
	var contacts, insts []uuid.UUID
	var keys []string
	for _, r := range rules {
		if r.Type != typeDebt {
			continue
		}
		keys = append(keys, ruleKey(r))
		if r.ContactID != nil {
			contacts = append(contacts, *r.ContactID)
		}
		if r.InstitutionID != nil {
			insts = append(insts, *r.InstitutionID)
		}
	}
	if len(keys) == 0 {
		return out, nil
	}
	persons, err := shared.LoadPersons(ctx, s.db, userID, contacts)
	if err != nil {
		return nil, err
	}
	irefs, err := shared.LoadInstitutionRefs(ctx, s.db, insts)
	if err != nil {
		return nil, err
	}
	nets := map[string]float64{}
	rows, err := s.db.Query(ctx, `SELECT l.key, COALESCE(SUM(l.signed), 0)::float8
		FROM (`+shared.DebtLedgerSQL("$1")+`) l WHERE l.key = ANY($2) GROUP BY l.key`, userID, keys)
	if err != nil {
		return nil, fmt.Errorf("counterparty nets: %w", err)
	}
	for rows.Next() {
		var (
			k string
			v float64
		)
		if err := rows.Scan(&k, &v); err != nil {
			rows.Close()
			return nil, err
		}
		nets[k] = math.Round(v*100) / 100
	}
	rows.Close()
	for _, r := range rules {
		if r.Type != typeDebt {
			continue
		}
		key := ruleKey(r)
		c := Counterparty{Key: key, Kind: "person", Net: nets[key]}
		switch {
		case r.InstitutionID != nil:
			c.Kind = "institution"
			if x, ok := irefs[*r.InstitutionID]; ok {
				c.Institution, c.Name = &x, x.Name
			}
		case r.ContactID != nil:
			pr := shared.ResolvePerson(persons, shared.PersonKey{ContactID: r.ContactID, Snapshot: derefStr(r.PersonName)})
			c.Person, c.Name = &pr, pr.Name
		default:
			pr := shared.NamePerson(derefStr(r.PersonName))
			c.Person, c.Name = &pr, pr.Name
		}
		out[r.ID] = c
	}
	return out, rows.Err()
}

func ruleKey(r *Rule) string {
	if r.InstitutionID != nil {
		return shared.InstitutionKey(*r.InstitutionID)
	}
	return shared.DebtKey(r.ContactID, derefStr(r.PersonName))
}

// ListRecurring — the recurring list: recurrings, not rules. status current (active + paused,
// default) | archived | all; tracking = one tracking type; for_type = the quick-create picker
// (recurrings with an active rule that can take a transaction of that money type, likely first).
func (s *Service) ListRecurring(ctx context.Context, userID uuid.UUID, status, tracking, q, forType string, hint PickerHint,
	page, perPage int, tz string) (*SummaryList, error) {
	page, perPage = clampPage(page, perPage)
	return s.listRecurring(ctx, userID, status, tracking, q, forType, hint, page, perPage, tz)
}

// ListAll — every recurring in one page (the sync dataset); no 100-row cap.
func (s *Service) ListAll(ctx context.Context, userID uuid.UUID, tz string) (*SummaryList, error) {
	return s.listRecurring(ctx, userID, "all", "", "", "", PickerHint{}, 1, unlimitedPerPage, tz)
}

func (s *Service) listRecurring(ctx context.Context, userID uuid.UUID, status, tracking, q, forType string, hint PickerHint,
	page, perPage int, tz string) (*SummaryList, error) {
	ctx = shared.WithRefCache(ctx)
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	cond := `status IN ('active', 'paused')`
	switch status {
	case "archived":
		cond = `status = 'archived'`
	case "all":
		cond = `TRUE`
	}
	args := []any{userID}
	if tracking != "" {
		args = append(args, tracking)
		cond += fmt.Sprintf(` AND tracking_type = $%d`, len(args))
	}
	if forType != "" {
		args = append(args, forType)
		cond = fmt.Sprintf(`status = 'active' AND EXISTS (SELECT 1 FROM recurring_rules l WHERE l.recurring_id = recurrings.id
			AND l.status = 'active' AND l.type = $%d)`, len(args))
	}
	if q = strings.TrimSpace(q); q != "" {
		args = append(args, "%"+shared.EscapeLike(q)+"%")
		cond += fmt.Sprintf(` AND name ILIKE $%d`, len(args))
	}
	out := &SummaryList{Data: []RecurringSummary{}}
	if out.Summary, err = s.summary(ctx, userID, today); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE status <> 'archived'),
		COUNT(*) FILTER (WHERE status = 'archived'), COUNT(*) FROM recurrings WHERE user_id = $1`, userID).Scan(
		&out.Counts.Current, &out.Counts.Archived, &out.Counts.All); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+itemRecordColumns+` FROM recurrings WHERE user_id = $1 AND `+cond, args...)
	if err != nil {
		return nil, err
	}
	var items []*itemRecord
	for rows.Next() {
		it, err := scanItemRecord(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sums, keys, err := s.summaries(ctx, userID, today, items)
	if err != nil {
		return nil, err
	}
	likely := map[uuid.UUID]bool{}
	if forType != "" {
		for i, it := range items {
			var cat *uuid.UUID
			if sums[i].Category != nil {
				cat = &sums[i].Category.ID
			}
			likely[it.ID] = hint.likely(cat, sums[i].Amount)
		}
	}
	idx := make([]int, len(items))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ia, ib := items[idx[a]], items[idx[b]]
		if forType != "" && likely[ia.ID] != likely[ib.ID] {
			return likely[ia.ID]
		}
		if keys[idx[a]] != keys[idx[b]] {
			return keys[idx[a]] < keys[idx[b]]
		}
		return strings.ToLower(ia.Name) < strings.ToLower(ib.Name)
	})
	total := len(idx)
	lo, hi := min((page-1)*perPage, total), min(page*perPage, total)
	for _, i := range idx[lo:hi] {
		sums[i].Likely = likely[items[i].ID]
		out.Data = append(out.Data, sums[i])
	}
	out.Pagination = shared.NewPage(page, perPage, total)
	return out, nil
}

// occKey — a round order key: due ones first, then by date.
func occKey(c *Occurrence) string {
	if c.Status == StatusDue {
		return "0" + c.Date
	}
	return "1" + c.Date
}

// summaries — the list rows of recurrings (+ each one's order key: due first, then next date).
func (s *Service) summaries(ctx context.Context, userID uuid.UUID, today time.Time, items []*itemRecord) ([]RecurringSummary, []string, error) {
	pages, err := s.itemPages(ctx, userID, today, items, false)
	if err != nil {
		return nil, nil, err
	}
	out := make([]RecurringSummary, len(pages))
	keys := make([]string, len(pages))
	for i, p := range pages {
		sm := RecurringSummary{ID: p.ID, Name: p.Name, TrackingType: p.TrackingType, IconCode: p.IconCode, Status: p.Status,
			LinesCount: len(p.Lines), Related: len(p.Touches) > 0, Touches: p.Touches, Chain: []ChainStep{}, MonthTotal: p.MonthTotal,
			Next: p.Next, DueCount: p.DueCount, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
		if p.Plan != nil {
			sm.Plan = &PlanBrief{Done: p.Plan.Done, Count: p.Plan.Count}
		}
		keys[i] = "2"
		for _, ln := range p.Lines {
			if ln.PauseReason != nil && sm.PauseReason == nil {
				sm.PauseReason = ln.PauseReason
			}
			if ln.EndingSoon {
				sm.EndingSoon = true
			}
			if c := ln.CurrentOccurrence; c != nil {
				k := occKey(c)
				if k < keys[i] {
					keys[i] = k
				}
				if best := sm.CurrentOccurrence; best == nil || k < occKey(best) {
					sm.CurrentOccurrence = c
				}
			} else if ln.NextDate != nil && "1"+*ln.NextDate < keys[i] {
				keys[i] = "1" + *ln.NextDate
			}
		}
		if len(p.Lines) == 1 {
			ln := p.Lines[0]
			pr := ln.Progress
			sm.Amount, sm.Account, sm.PaidBy, sm.Chain, sm.Progress, sm.Category = ln.Amount, ln.Account, ln.PaidBy, ln.Chain, &pr, ln.Category
		}
		out[i] = sm
	}
	return out, keys, nil
}

// ---- delete / transitions ----

// DeleteRecurring — the item and its lines (their rounds go with them). What the rounds wrote
// stays: the transactions as they are, its debt rows become general debts of the counterparty.
func (s *Service) DeleteRecurring(ctx context.Context, userID, id uuid.UUID) (*RecurringDeleteResult, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM recurrings WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return nil, fmt.Errorf("delete recurring item: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return &RecurringDeleteResult{ID: id, AccountBalances: []shared.WalletBalance{}, Effects: shared.NewEffects()}, nil
}

// itemTransition — fn on every line of the item (the ones it applies to), then the item's status.
func (s *Service) itemTransition(ctx context.Context, userID, id uuid.UUID, tz string, itemFn func(it *itemRecord) error,
	lineFn func(tx pgx.Tx, r *Rule, today time.Time) (bool, error)) (*ItemPage, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	it, err := getItemRecord(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	lines, err := linesOf(ctx, tx, userID, []uuid.UUID{id}, true)
	if err != nil {
		return nil, err
	}
	it.Status = effectiveStatus(it.Status, lines)
	if err := itemFn(it); err != nil {
		return nil, err
	}
	for _, r := range lines {
		changed, err := lineFn(tx, r, today)
		if err != nil {
			return nil, err
		}
		if changed {
			if err := saveRuleTx(ctx, tx, r); err != nil {
				return nil, err
			}
		}
	}
	if err := saveItemRecordTx(ctx, tx, it); err != nil {
		return nil, err
	}
	if err := refreshItemStatusTx(ctx, tx, userID, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.afterItemWrite(ctx, userID, id, tz, nil)
}

func (s *Service) PauseRecurring(ctx context.Context, userID, id uuid.UUID, tz string) (*ItemPage, error) {
	return s.itemTransition(ctx, userID, id, tz, func(it *itemRecord) error {
		if it.Status != "active" {
			return ErrInvalidTransition
		}
		return nil
	}, func(tx pgx.Tx, r *Rule, today time.Time) (bool, error) {
		if r.Status != "active" {
			return false, nil
		}
		due, err := s.dueOccs(ctx, tx, r, today)
		if err != nil {
			return false, err
		}
		return true, s.pauseTx(ctx, tx, r, due, today, PauseManual)
	})
}

func (s *Service) ResumeRecurring(ctx context.Context, userID, id uuid.UUID, tz string) (*ItemPage, error) {
	return s.itemTransition(ctx, userID, id, tz, func(it *itemRecord) error {
		if it.Status != "paused" {
			return ErrInvalidTransition
		}
		return nil
	}, func(tx pgx.Tx, r *Rule, today time.Time) (bool, error) {
		if r.Status != "paused" {
			return false, nil
		}
		if r.Type == typeSaving {
			if err := checkSavingTx(ctx, tx, r.UserID, r.AccountID, r.GoalID); err != nil {
				return false, err
			}
		}
		return true, r.resume(today)
	})
}

func (s *Service) ArchiveRecurring(ctx context.Context, userID, id uuid.UUID, tz string) (*ItemPage, error) {
	return s.itemTransition(ctx, userID, id, tz, func(it *itemRecord) error {
		if it.Status == "archived" {
			return ErrInvalidTransition
		}
		it.Status = "archived"
		return nil
	}, func(tx pgx.Tx, r *Rule, today time.Time) (bool, error) {
		if r.Status == "archived" {
			return false, nil
		}
		if r.Status == "active" {
			due, err := s.dueOccs(ctx, tx, r, today)
			if err != nil {
				return false, err
			}
			if err := s.pauseTx(ctx, tx, r, due, today, PauseManual); err != nil {
				return false, err
			}
		}
		r.Status = "archived"
		return true, nil
	})
}

// RestoreRecurring — the item and its lines come back paused (resume them to run).
func (s *Service) RestoreRecurring(ctx context.Context, userID, id uuid.UUID, tz string) (*ItemPage, error) {
	return s.itemTransition(ctx, userID, id, tz, func(it *itemRecord) error {
		if it.Status != "archived" {
			return ErrInvalidTransition
		}
		it.Status = "paused"
		return nil
	}, func(_ pgx.Tx, r *Rule, _ time.Time) (bool, error) {
		if r.Status != "archived" {
			return false, nil
		}
		r.Status = "paused"
		if r.PauseReason == nil {
			r.PauseReason = ptr(PauseManual)
		}
		return true, nil
	})
}

// DuplicateRecurring — a copy of the item and its lines, starting today (no plan, no rounds).
func (s *Service) DuplicateRecurring(ctx context.Context, userID, id uuid.UUID, tz string) (*ItemPage, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	src, err := getItemRecord(ctx, tx, userID, id, false)
	if err != nil {
		return nil, err
	}
	lines, err := linesOf(ctx, tx, userID, []uuid.UUID{id}, false)
	if err != nil {
		return nil, err
	}
	cp := *src
	cp.ID, cp.Status, cp.Name = uuid.Nil, "active", copyName(src.Name)
	if err := saveItemRecordTx(ctx, tx, &cp); err != nil {
		return nil, err
	}
	for _, r := range lines {
		l := *r
		l.ID, l.Status, l.PauseReason, l.PausedFrom, l.Gaps, l.ZeroRounds = uuid.Nil, "active", nil, nil, nil, 0
		l.GeneratedUntil, l.TotalCount = nil, nil
		l.StartDate, l.TrackedFrom, l.DueNotifiedUntil = today, today, &today
		yesterday := today.AddDate(0, 0, -1)
		l.AutoAcceptedUntil = &yesterday
		l.Weekdays = append([]int(nil), r.Weekdays...)
		if l.Freq == "dates" {
			l.Dates = nil
			for _, d := range r.Dates {
				if !d.Before(today) {
					l.Dates = append(l.Dates, d)
				}
			}
			if len(l.Dates) == 0 {
				continue // nothing left to copy of this line
			}
		}
		t := targetOf(r)
		itemToLine(&cp, &t, &l)
		if err := l.limits(today); err != nil {
			return nil, err
		}
		if err := saveRuleTx(ctx, tx, &l); err != nil {
			return nil, err
		}
	}
	if err := refreshItemStatusTx(ctx, tx, userID, cp.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.afterItemWrite(ctx, userID, cp.ID, tz, nil)
}

// ---- lines ----

func (s *Service) AddLine(ctx context.Context, userID, id uuid.UUID, in LineInput, tz string) (*ItemPage, error) {
	in.ID = nil
	return s.lineWrite(ctx, userID, id, nil, in, tz)
}

func (s *Service) UpdateLine(ctx context.Context, userID, id, lineID uuid.UUID, in LineInput, tz string) (*ItemPage, error) {
	return s.lineWrite(ctx, userID, id, &lineID, in, tz)
}

func (s *Service) lineWrite(ctx context.Context, userID, id uuid.UUID, lineID *uuid.UUID, in LineInput, tz string) (*ItemPage, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	it, err := getItemRecord(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	if it.Status == "archived" {
		return nil, ErrInvalidTransition
	}
	r := &Rule{}
	if lineID != nil {
		if err := lineOfItem(ctx, tx, userID, id, *lineID); err != nil {
			return nil, err
		}
		if r, err = getRule(ctx, tx, userID, *lineID, true); err != nil {
			return nil, err
		}
	}
	if err := s.applyLineInputTx(ctx, tx, it, r, in, today); err != nil {
		return nil, err
	}
	if err := saveRuleTx(ctx, tx, r); err != nil {
		return nil, err
	}
	if err := refreshItemStatusTx(ctx, tx, userID, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.afterItemWrite(ctx, userID, id, tz, nil)
}

// DeleteLine — the rule and its rounds; what they wrote stays. A recurring keeps at least one rule.
func (s *Service) DeleteLine(ctx context.Context, userID, id, lineID uuid.UUID, tz string) (*ItemPage, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := getItemRecord(ctx, tx, userID, id, true); err != nil {
		return nil, err
	}
	if err := lineOfItem(ctx, tx, userID, id, lineID); err != nil {
		return nil, err
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM recurring_rules WHERE recurring_id = $1`, id).Scan(&n); err != nil {
		return nil, err
	}
	if n <= 1 {
		return nil, ErrLastLine
	}
	if _, err := tx.Exec(ctx, `DELETE FROM recurring_rules WHERE id = $1 AND user_id = $2`, lineID, userID); err != nil {
		return nil, err
	}
	if err := refreshItemStatusTx(ctx, tx, userID, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.afterItemWrite(ctx, userID, id, tz, nil)
}

// lineTransition — pause / resume / archive / restore / extend of one line (today's rule
// transitions), then the item's status.
func (s *Service) lineTransition(ctx context.Context, userID, id, lineID uuid.UUID, tz string, op func() (*Detail, error)) (*ItemPage, error) {
	if err := lineOfItem(ctx, s.db, userID, id, lineID); err != nil {
		return nil, err
	}
	var status string
	if err := s.db.QueryRow(ctx, `SELECT status FROM recurrings WHERE id = $1`, id).Scan(&status); err != nil {
		return nil, err
	}
	if status == "archived" {
		return nil, ErrInvalidTransition
	}
	if _, err := op(); err != nil {
		return nil, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := refreshItemStatusTx(ctx, tx, userID, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetRecurring(ctx, userID, id, tz)
}

func (s *Service) PauseLine(ctx context.Context, userID, id, lineID uuid.UUID, tz string) (*ItemPage, error) {
	return s.lineTransition(ctx, userID, id, lineID, tz, func() (*Detail, error) { return s.Pause(ctx, userID, lineID, tz) })
}

func (s *Service) ResumeLine(ctx context.Context, userID, id, lineID uuid.UUID, tz string) (*ItemPage, error) {
	return s.lineTransition(ctx, userID, id, lineID, tz, func() (*Detail, error) { return s.Resume(ctx, userID, lineID, tz) })
}

func (s *Service) ArchiveLine(ctx context.Context, userID, id, lineID uuid.UUID, tz string) (*ItemPage, error) {
	return s.lineTransition(ctx, userID, id, lineID, tz, func() (*Detail, error) { return s.Archive(ctx, userID, lineID, tz) })
}

func (s *Service) RestoreLine(ctx context.Context, userID, id, lineID uuid.UUID, tz string) (*ItemPage, error) {
	return s.lineTransition(ctx, userID, id, lineID, tz, func() (*Detail, error) { return s.Restore(ctx, userID, lineID, tz) })
}

func (s *Service) ExtendLine(ctx context.Context, userID, id, lineID uuid.UUID, req ExtendRequest, tz string) (*ItemPage, error) {
	return s.lineTransition(ctx, userID, id, lineID, tz, func() (*Detail, error) { return s.Extend(ctx, userID, lineID, req, tz) })
}

// ---- rounds over every line ----

// ItemOccurrences — every line's rounds in one dated list. With from / to: every round dated in
// it (at most 400 days); without: the quick-create picker (each line's due ones, `past` earlier
// and `upcoming` next ones), oldest first, suggested = the oldest due one, else the free one
// nearest tx_date.
func (s *Service) ItemOccurrences(ctx context.Context, userID, id uuid.UUID, from, to, txDate *time.Time, past, upcoming int, tz string) (*OccurrencesResult, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	if _, err := getItemRecord(ctx, s.db, userID, id, false); err != nil {
		return nil, err
	}
	lines, err := linesOf(ctx, s.db, userID, []uuid.UUID{id}, false)
	if err != nil {
		return nil, err
	}
	if from != nil || to != nil {
		lo, hi := today.AddDate(0, -1, 0), today.AddDate(0, 3, 0)
		if from != nil {
			lo = *from
		}
		if to != nil {
			hi = *to
		}
		if hi.Before(lo) || hi.Sub(lo) > 400*24*time.Hour {
			return nil, ErrValidation
		}
		occs, err := s.occsInRange(ctx, s.db, userID, lines, lo, hi, today, false)
		if err != nil {
			return nil, err
		}
		sort.SliceStable(occs, func(a, b int) bool { return occs[a].date.Before(occs[b].date) })
		items, err := s.viewOccs(ctx, s.db, userID, today, occs)
		if err != nil {
			return nil, err
		}
		return &OccurrencesResult{Data: items}, nil
	}
	out := &OccurrencesResult{Data: []Item{}}
	var oldestDue *Item
	for _, r := range lines {
		one, err := s.Occurrences(ctx, userID, r.ID, txDate, past, upcoming, tz)
		if err != nil {
			return nil, err
		}
		for i := range one.Data {
			if one.Data[i].Status == StatusDue && (oldestDue == nil || one.Data[i].Date < oldestDue.Date) {
				x := one.Data[i]
				oldestDue = &x
			}
		}
		out.Data = append(out.Data, one.Data...)
	}
	sort.SliceStable(out.Data, func(a, b int) bool { return out.Data[a].Date < out.Data[b].Date })
	switch {
	case oldestDue != nil:
		out.SuggestedSlot, out.SuggestedRuleID = &oldestDue.Slot, &oldestDue.RuleID
	case txDate != nil:
		var best *Item
		for i := range out.Data {
			o := &out.Data[i]
			if o.Status != StatusUpcoming && o.Status != StatusDue {
				continue
			}
			d, _ := parseDate(o.Date)
			if best == nil {
				best = o
				continue
			}
			bd, _ := parseDate(best.Date)
			if absDays(d, *txDate) < absDays(bd, *txDate) {
				best = o
			}
		}
		if best != nil {
			out.SuggestedSlot, out.SuggestedRuleID = &best.Slot, &best.RuleID
		}
	}
	return out, nil
}

// itemBulk — a "ready" action over every line of the item, merged.
func (s *Service) itemBulk(ctx context.Context, userID, id uuid.UUID, tz string, op func(lineID uuid.UUID) (*BulkResult, error)) (*BulkResult, error) {
	if _, err := getItemRecord(ctx, s.db, userID, id, false); err != nil {
		return nil, err
	}
	lines, err := linesOf(ctx, s.db, userID, []uuid.UUID{id}, false)
	if err != nil {
		return nil, err
	}
	out := &BulkResult{Items: []Item{}, LeftOut: []LeftOut{}, AccountBalances: []shared.WalletBalance{}, Effects: shared.NewEffects()}
	for _, r := range lines {
		res, err := op(r.ID)
		if errors.Is(err, ErrInvalidTransition) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, res.Items...)
		for _, l := range res.LeftOut {
			l.RuleID = r.ID
			out.LeftOut = append(out.LeftOut, l)
		}
		out.AccountBalances = append(out.AccountBalances, res.AccountBalances...)
		out.Effects.Merge(res.Effects)
	}
	var err2 error
	if out.Setting, err2 = s.GetRecurring(ctx, userID, id, tz); err2 != nil {
		return nil, err2
	}
	return out, nil
}

func (s *Service) ConfirmReadyItem(ctx context.Context, userID, id uuid.UUID, tz string) (*BulkResult, error) {
	return s.itemBulk(ctx, userID, id, tz, func(l uuid.UUID) (*BulkResult, error) { return s.ConfirmReady(ctx, userID, l, tz) })
}

func (s *Service) MarkPaidReadyItem(ctx context.Context, userID, id uuid.UUID, tz string) (*BulkResult, error) {
	return s.itemBulk(ctx, userID, id, tz, func(l uuid.UUID) (*BulkResult, error) { return s.MarkPaidReady(ctx, userID, l, tz) })
}

func (s *Service) SkipReadyItem(ctx context.Context, userID, id uuid.UUID, tz string) (*BulkResult, error) {
	return s.itemBulk(ctx, userID, id, tz, func(l uuid.UUID) (*BulkResult, error) { return s.SkipReady(ctx, userID, l, tz) })
}

// ItemRounds — GET /recurring/{id}/items: every line's rounds in one list. tab produced = the
// history (recorded, paid, skipped, and the due ones), newest first, paged; tab upcoming = what is
// still to come, oldest first: a line with an end shows all of it, an open-ended line `months`
// months from `from` (the window then covers every line).
func (s *Service) ItemRounds(ctx context.Context, userID, id uuid.UUID, tab string, page, perPage int, from *time.Time, months int, tz string) (*ItemList, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	if _, err := getItemRecord(ctx, s.db, userID, id, false); err != nil {
		return nil, err
	}
	lines, err := linesOf(ctx, s.db, userID, []uuid.UUID{id}, false)
	if err != nil {
		return nil, err
	}
	page, perPage = clampPage(page, perPage)
	lang := s.lang(ctx, userID)
	ids := make([]uuid.UUID, len(lines))
	for i, r := range lines {
		ids[i] = r.ID
	}
	stored, err := s.storedRows(ctx, s.db, userID, ids, nil, nil)
	if err != nil {
		return nil, err
	}
	byLine := map[uuid.UUID][]*itemRow{}
	for _, it := range stored {
		byLine[it.RuleID] = append(byLine[it.RuleID], it)
	}
	var occs []*occ
	if tab != "upcoming" {
		for _, r := range lines {
			seqs := seqIndex(r)
			for _, it := range byLine[r.ID] {
				if statusOfRow(it, today) != StatusUpcoming {
					occs = append(occs, rowOcc(r, it, seqs, today, lang))
				}
			}
			if r.Status == "active" && !r.TrackedFrom.After(today) {
				for _, o := range occsOf(r, byLine[r.ID], r.TrackedFrom, today, today, lang) {
					if !o.stored() && o.status == StatusDue {
						occs = append(occs, o)
					}
				}
			}
		}
		sort.SliceStable(occs, func(i, j int) bool { return occs[i].date.After(occs[j].date) })
		return s.pageOccs(ctx, userID, today, occs, page, perPage)
	}
	start := today.AddDate(0, 0, 1)
	if from != nil {
		start = *from
	}
	if months <= 0 {
		months = 12
	}
	if months > 24 {
		months = 24
	}
	end := start.AddDate(0, months, -1)
	var w *Window
	for _, r := range lines {
		lo, hi := start, end
		if r.TotalCount != nil && r.GeneratedUntil != nil {
			lo, hi = r.StartDate, *r.GeneratedUntil
		} else {
			if w == nil {
				w = &Window{From: start.Format(dateLayout), To: end.Format(dateLayout)}
			}
			if r.Status == "active" && w.NextFrom == nil {
				nf := end.AddDate(0, 0, 1).Format(dateLayout)
				w.NextFrom = &nf
			}
		}
		if hi.Before(lo) {
			continue
		}
		for _, o := range occsOf(r, byLine[r.ID], lo, hi, today, lang) {
			if o.status == StatusUpcoming {
				occs = append(occs, o)
			}
		}
	}
	sort.SliceStable(occs, func(i, j int) bool { return occs[i].date.Before(occs[j].date) })
	if w == nil { // every line has an end: paged
		return s.pageOccs(ctx, userID, today, occs, page, perPage)
	}
	items, err := s.viewOccs(ctx, s.db, userID, today, occs)
	if err != nil {
		return nil, err
	}
	return &ItemList{Data: items, Pagination: shared.NewPage(1, len(items)+1, len(items)), Window: w}, nil
}
