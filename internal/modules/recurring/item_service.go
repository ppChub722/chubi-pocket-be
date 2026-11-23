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

// ---- validation of an item input ----

// applyItemInput — the item fields of in onto it (create: it is new; update: a full replace of the
// item's own fields, lines handled apart). Checks what needs no DB.
func applyItemInput(it *itemRecord, in *RecurringInput, creating bool) error {
	if in.Name == nil {
		return ErrValidation
	}
	name := strings.TrimSpace(*in.Name)
	if name == "" || len([]rune(name)) > 100 {
		return ErrValidation
	}
	if in.Type == nil || !validItemType(*in.Type) {
		return ErrValidation
	}
	if !creating && *in.Type != it.Type {
		return ErrTypeFixed // the type is fixed: delete the item and create a new one
	}
	if in.Description != nil && len([]rune(*in.Description)) > 200 || in.Note != nil && len([]rune(*in.Note)) > 500 {
		return ErrValidation
	}
	it.Name, it.Type, it.Description, it.Note, it.IconCode = name, *in.Type, in.Description, in.Note, in.IconCode
	it.CategoryID, it.GoalID = nil, nil
	it.TagIDs = uniqueIDs(append([]uuid.UUID{}, in.TagIDs...))
	switch it.Type {
	case TypeSaving:
		it.GoalID, it.TagIDs = in.GoalID, []uuid.UUID{}
	case TypeTransfer:
	default:
		it.CategoryID = in.CategoryID
	}
	it.ContactID, it.PersonName, it.InstitutionID, it.AccountID, it.DebtDirection = nil, nil, nil, nil, nil
	it.DebtPlanID, it.Plan = nil, nil
	if it.Type == TypeDebt {
		c := in.Counterparty
		switch {
		case in.DebtPlanID != nil: // a plan: its creditor and direction (resolveCounterpartyTx)
			if c != nil {
				return ErrValidation
			}
			it.DebtPlanID = in.DebtPlanID
			return nil
		case c != nil && c.AccountID != nil: // a card / PayLater bill: I owe the card
			if c.ContactID != nil || c.PersonName != nil || c.InstitutionID != nil {
				return ErrValidation
			}
			it.AccountID, it.DebtDirection = c.AccountID, ptr("received")
			return nil
		}
		if c == nil || in.DebtDirection == nil || (*in.DebtDirection != "paid" && *in.DebtDirection != "received") {
			return ErrValidation
		}
		n := 0
		if c.ContactID != nil {
			n++
		}
		if c.InstitutionID != nil {
			n++
		}
		if c.PersonName != nil && strings.TrimSpace(*c.PersonName) != "" && c.ContactID == nil {
			n++
		}
		if n != 1 {
			return ErrValidation
		}
		it.ContactID, it.InstitutionID, it.DebtDirection = c.ContactID, c.InstitutionID, in.DebtDirection
		if c.ContactID == nil && c.InstitutionID == nil {
			pn := strings.TrimSpace(*c.PersonName)
			if len([]rune(pn)) > 100 {
				return ErrValidation
			}
			it.PersonName = &pn
		}
	} else if in.Counterparty != nil || in.DebtPlanID != nil {
		return ErrValidation
	}
	return nil
}

// sameFamily — the expense types switch freely; the others never change.
// resolveCounterpartyTx — the counterparty's name (a contact's live name, an institution's name),
// checked to be mine and active.
func resolveCounterpartyTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, it *itemRecord) error {
	it.counterpartName = ""
	if it.DebtPlanID != nil {
		p, err := getPlan(ctx, tx, userID, *it.DebtPlanID, false)
		if err != nil {
			return err
		}
		if p.isCard() { // a card plan is paid by its card's bill
			return apierr.Wrap(ErrValidation, "a card plan takes no reminder: pay the card bill",
				apierr.Field("debt_plan_id", apierr.FieldInvalid))
		}
		it.setPlan(p)
	}
	switch {
	case it.AccountID != nil:
		var typ string
		err := tx.QueryRow(ctx, `SELECT a.type, a.name FROM accounts a WHERE a.id = $1 AND `+shared.ActiveMembershipPredicate("a.id", "$2"),
			*it.AccountID, userID).Scan(&typ, &it.counterpartName)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAccountNotFound
		}
		if err != nil {
			return err
		}
		if typ != "credit_card" && typ != "pay_later" {
			return apierr.Wrap(ErrValidation, "not a card / PayLater wallet", apierr.Field("counterparty.account_id", apierr.FieldInvalid))
		}
	case it.InstitutionID != nil:
		if err := institutions.CheckOwned(ctx, tx, userID, *it.InstitutionID, true); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT name FROM institutions WHERE id = $1`, *it.InstitutionID).Scan(&it.counterpartName)
	case it.ContactID != nil:
		r := &Rule{ContactID: it.ContactID}
		if err := checkDebtPersonTx(ctx, tx, userID, r); err != nil {
			return err
		}
		it.counterpartName = *r.PersonName
		it.PersonName = r.PersonName // the name is kept with the contact (a deleted contact leaves it)
	case it.PersonName != nil:
		it.counterpartName = *it.PersonName
	}
	return nil
}

// checkItemRefsTx — category (my expense / income one), tags, goal.
func checkItemRefsTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, it *itemRecord) error {
	catType := "expense"
	if it.Type == TypeIncome {
		catType = "income"
	}
	if err := checkRefsTx(ctx, tx, userID, catType, nil, nil, it.CategoryID, it.TagIDs); err != nil {
		return err
	}
	return nil
}

// applyLineInput — a line input onto r (a new line, or the line it updates). The schedule is
// required on a new line; absent on an update = kept.
func (s *Service) applyLineInputTx(ctx context.Context, tx pgx.Tx, it *itemRecord, r *Rule, in LineInput, today time.Time) error {
	isNew := r.ID == uuid.Nil
	if in.NotifyOnly != nil {
		r.NotifyOnly = *in.NotifyOnly
	}
	if r.NotifyOnly && (in.Source.PaidBy != nil || in.RepaysReminderID != nil) {
		return ErrSourceInvalid // a notice moves no money: no one pays, nothing is paid back
	}
	// repay_paid_by: pays back the person of a person-paid reminder of the same (expense) item
	r.RepaysReminderID = nil
	if in.RepaysReminderID != nil {
		if it.Type != TypeExpense || in.Source.PaidBy != nil || (r.ID != uuid.Nil && *in.RepaysReminderID == r.ID) {
			return ErrSourceInvalid
		}
		t, err := getRule(ctx, tx, it.UserID, *in.RepaysReminderID, false)
		if errors.Is(err, ErrNotFound) || (err == nil && (t.RecurringID != it.ID || t.PaidByPersonName == nil)) {
			return apierr.Wrap(ErrValidation, "repays_reminder_id is not a person-paid reminder of this item",
				apierr.Field("repays_reminder_id", apierr.FieldInvalid))
		}
		if err != nil {
			return err
		}
		r.RepaysReminderID, r.ContactID, r.PersonName = &t.ID, t.PaidByContactID, t.PaidByPersonName
	}
	itemToLine(it, r)
	// source
	src := in.Source
	if src.AccountID != nil && src.PaidBy != nil {
		return ErrSourceInvalid
	}
	r.AccountID, r.PaidByContactID, r.PaidByPersonName = src.AccountID, nil, nil
	if src.PaidBy != nil {
		if !paidByAllowed(it.Type) {
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
	// neither: a line without a wallet (as a 0.4.2 setting: confirm asks for one, auto-create
	// leaves it out; a debt line books ledger-only rows; saving answers SAVING_NEEDS_WALLET)
	// amount: fixed, or asked on each round (expense / transfer only; no amount sent = ask; an amount
	// sent with ask is the hint each round starts with)
	mode := AmountFixed
	switch {
	case in.AmountMode != nil && *in.AmountMode != AmountFixed && *in.AmountMode != AmountAsk:
		return ErrValidation
	case in.AmountMode != nil:
		mode = *in.AmountMode
	case in.Amount == nil:
		mode = AmountAsk
	}
	switch {
	case r.NotifyOnly:
		if in.AmountMode == nil {
			mode = AmountFixed // a notice may show an amount; never asked
		}
	case r.RepaysReminderID != nil || it.DebtPlanID != nil:
		if in.AmountMode == nil {
			mode = AmountFixed // its amount comes from what it pays back / the plan's installment
		}
	}
	if mode == AmountAsk && lineType(it) != TypeExpense && lineType(it) != TypeTransfer || mode == AmountAsk && r.RepaysReminderID != nil {
		return ErrValidation
	}
	r.AmountMode = mode
	switch {
	case in.Amount != nil && *in.Amount < 0:
		return ErrValidation
	case in.Amount != nil:
		r.Amount = math.Round(*in.Amount*100) / 100 // ask: a hint the round starts with
	case mode == AmountAsk || r.RepaysReminderID != nil || r.NotifyOnly:
		r.Amount = 0
	case it.DebtPlanID != nil:
		v, err := nextInstallmentAmount(ctx, tx, *it.DebtPlanID)
		if err != nil {
			return err
		}
		r.Amount = v
	default:
		return ErrValidation
	}
	r.ToAccountID = nil
	switch {
	case it.isCardBill():
		r.ToAccountID = it.AccountID
	case it.Type == TypeTransfer:
		r.ToAccountID = in.ToAccountID
	}
	if it.Type == TypeDebt && !it.isCardBill() {
		if in.DebtKind != nil && *in.DebtKind != DebtKindRepayment {
			return ErrValidation // a debt item pays its debt back (repay / collect)
		}
		r.DebtKind = ptr(DebtKindRepayment)
		r.DebtDirection = lineDirection(it.DebtDirection, r.DebtKind)
	}
	if r.NotifyOnly {
		f := false
		in.AutoCreate = &f // nothing to create
	}
	if in.AutoCreate != nil && *in.AutoCreate != r.AutoCreate {
		r.AutoCreate = *in.AutoCreate
		yesterday := today.AddDate(0, 0, -1)
		r.AutoCreatedUntil = &yesterday // switching on / off never back-creates
	}
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
		r.GeneratedUntil, r.EndingNotified, r.TotalCount = nil, false, nil
	}
	r.Kind = lineKind(it, r.EndKind, r.AmountMode)
	if r.RepaysReminderID != nil {
		r.Kind = KindDebt
	}
	if err := r.finishKind(&r.Kind); err != nil {
		return err
	}
	if err := r.limits(today); err != nil {
		return err
	}
	if r.PaidByPersonName != nil {
		// a person-paid round moves no wallet money: the line carries no wallet
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

// ---- create / update ----

func (s *Service) CreateRecurring(ctx context.Context, userID uuid.UUID, in RecurringInput, tz string) (*ItemPage, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	it := &itemRecord{UserID: userID, Status: "active"}
	if err := applyItemInput(it, &in, true); err != nil {
		return nil, err
	}
	if len(in.Lines) == 0 {
		return nil, ErrValidation
	}
	if len(in.Lines) > 20 {
		return nil, ErrValidation
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := resolveCounterpartyTx(ctx, tx, userID, it); err != nil {
		return nil, err
	}
	if err := checkItemRefsTx(ctx, tx, userID, it); err != nil {
		return nil, err
	}
	if err := saveItemRecordTx(ctx, tx, it); err != nil {
		return nil, err
	}
	for i, li := range in.Lines {
		r := &Rule{}
		if err := s.applyLineInputTx(ctx, tx, it, r, li, today); err != nil {
			return nil, err
		}
		if i == 0 {
			r.ID = it.ID // the first reminder shares the item's id (as every migrated rule does)
			if err := insertRuleTx(ctx, tx, r); err != nil {
				return nil, err
			}
		} else if err := saveRuleTx(ctx, tx, r); err != nil {
			return nil, err
		}
		if err := saveAlertsTx(ctx, tx, r, li.Alerts); err != nil {
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

// UpdateRecurring — the item's own fields (full replace), its lines (with id = update, without =
// new, missing = deleted), plan and reminders when sent. The item's fields reach every line.
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
	if err := applyItemInput(it, &in, false); err != nil {
		return nil, err
	}
	if err := resolveCounterpartyTx(ctx, tx, userID, it); err != nil {
		return nil, err
	}
	if err := checkItemRefsTx(ctx, tx, userID, it); err != nil {
		return nil, err
	}
	lines, err := linesOf(ctx, tx, userID, []uuid.UUID{id}, true)
	if err != nil {
		return nil, err
	}
	byID := map[uuid.UUID]*Rule{}
	for _, r := range lines {
		byID[r.ID] = r
	}
	if in.has("reminders") {
		if len(in.Lines) == 0 || len(in.Lines) > 20 {
			return nil, ErrValidation
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
			if err := saveAlertsTx(ctx, tx, r, li.Alerts); err != nil {
				return nil, err
			}
		}
		for _, r := range lines {
			if !keep[r.ID] {
				if _, err := tx.Exec(ctx, `DELETE FROM recurring_reminders WHERE id = $1 AND user_id = $2`, r.ID, userID); err != nil {
					return nil, err
				}
			}
		}
	} else {
		if err := syncLinesTx(ctx, tx, it, lines); err != nil {
			return nil, err
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

// syncLinesTx — the item's own fields onto every line (no line input).
func syncLinesTx(ctx context.Context, tx pgx.Tx, it *itemRecord, lines []*Rule) error {
	for _, r := range lines {
		itemToLine(it, r)
		if err := saveRuleTx(ctx, tx, r); err != nil {
			return err
		}
	}
	return nil
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

// itemPages — the page of each item (full = stats, plan, reminders: the item page; else the list
// row's needs).
func (s *Service) itemPages(ctx context.Context, userID uuid.UUID, today time.Time, items []*itemRecord, full bool) ([]ItemPage, error) {
	out := make([]ItemPage, 0, len(items))
	if len(items) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(items))
	var catIDs, tagIDs, goalIDs []uuid.UUID
	for i, it := range items {
		ids[i] = it.ID
		if it.CategoryID != nil {
			catIDs = append(catIDs, *it.CategoryID)
		}
		if it.GoalID != nil {
			goalIDs = append(goalIDs, *it.GoalID)
		}
		tagIDs = append(tagIDs, it.TagIDs...)
	}
	lines, err := linesOf(ctx, s.db, userID, ids, false)
	if err != nil {
		return nil, err
	}
	views, err := s.recurringViews(ctx, userID, today, lines)
	if err != nil {
		return nil, err
	}
	byItem := map[uuid.UUID][]int{}
	for i, r := range lines {
		byItem[r.RecurringID] = append(byItem[r.RecurringID], i)
	}
	cats, err := shared.LoadCategoryRefs(ctx, s.db, catIDs)
	if err != nil {
		return nil, err
	}
	tags, err := shared.LoadTagRefs(ctx, s.db, userID, tagIDs)
	if err != nil {
		return nil, err
	}
	goals, err := shared.LoadSavingGoalRefs(ctx, s.db, goalIDs)
	if err != nil {
		return nil, err
	}
	cps, err := s.counterparties(ctx, userID, items)
	if err != nil {
		return nil, err
	}
	paidBy, err := s.paidByPersons(ctx, userID, lines)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		p := ItemPage{ID: it.ID, Name: it.Name, Description: it.Description, Note: it.Note, IconCode: it.IconCode, Type: it.Type,
			CategoryID: it.CategoryID, TagIDs: it.TagIDs, Tags: []shared.TagRef{}, GoalID: it.GoalID, DebtDirection: it.DebtDirection,
			Status: it.Status, Lines: []Line{}, CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt,
			Stats: ItemStats{Series: []Point{}}, AccountBalances: []shared.WalletBalance{}, Effects: shared.NewEffects()}
		if it.CategoryID != nil {
			if x, ok := cats[*it.CategoryID]; ok {
				p.Category = &x
			}
		}
		for _, t := range it.TagIDs {
			if x, ok := tags[t]; ok {
				p.Tags = append(p.Tags, x)
			}
		}
		if it.GoalID != nil {
			if x, ok := goals[*it.GoalID]; ok {
				p.Goal = &x
			}
		}
		if c, ok := cps[it.ID]; ok {
			p.Counterparty = &c
		}
		allDone := len(byItem[it.ID]) > 0
		var own []*Rule
		for _, i := range byItem[it.ID] {
			own = append(own, lines[i])
		}
		it.Status = effectiveStatus(it.Status, own)
		p.Status = it.Status
		var dueKeyMin string
		for _, i := range byItem[it.ID] {
			r, v := lines[i], views[i]
			ln := lineOf(r, &v, paidBy)
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
					p.Next = &NextRound{RecurringReminderID: r.ID, Date: *v.NextDate, Amount: v.NextAmount, DaysUntil: d}
				}
			}
			if k := dueKey(&v); dueKeyMin == "" || k < dueKeyMin {
				dueKeyMin = k
			}
		}
		p.MonthTotal = math.Round(p.MonthTotal*100) / 100
		if allDone && it.Status != "archived" {
			p.Status = "done"
		}
		p.Actions = ItemActions{
			Pause: it.Status == "active", Resume: it.Status == "paused", Archive: it.Status != "archived",
			Restore: it.Status == "archived", Delete: true, AddLine: it.Status != "archived",
		}
		if full {
			if err := s.fillItemFull(ctx, userID, today, it, lines, byItem[it.ID], &p); err != nil {
				return nil, err
			}
		} else if it.Plan != nil {
			if p.Plan, err = s.planView(ctx, s.db, userID, it.Plan, today); err != nil {
				return nil, err
			}
		}
		_ = dueKeyMin
		out = append(out, p)
	}
	return out, nil
}

func (s *Service) fillItemFull(ctx context.Context, userID uuid.UUID, today time.Time, it *itemRecord, lines []*Rule, idx []int, p *ItemPage) error {
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
	var err error
	if it.Plan != nil {
		if p.Plan, err = s.planView(ctx, s.db, userID, it.Plan, today); err != nil {
			return err
		}
	}
	for i := range p.Lines {
		if p.Lines[i].Alerts, err = loadAlerts(ctx, s.db, p.Lines[i].ID); err != nil {
			return err
		}
	}
	return nil
}

// lineOf — a line's view from the engine's view of it.
func lineOf(r *Rule, v *Recurring, paidBy map[uuid.UUID]shared.PersonRef) Line {
	end := v.End
	ln := Line{ID: r.ID, Amount: v.Amount, AmountMode: r.amountMode(), Action: r.action(), RepaysReminderID: r.RepaysReminderID, Alerts: []Alert{}, Account: v.Account, ToAccountID: r.ToAccountID, ToAccount: v.ToAccount,
		DebtKind: r.DebtKind, Schedule: Schedule{RecurrenceRule: v.Rule, StartDate: v.StartDate, End: &end},
		AutoCreate: r.AutoCreate, Status: r.Status, PauseReason: v.PauseReason, NextDate: v.NextDate, NextAmount: v.NextAmount,
		DaysUntil: v.DaysUntil, DueCount: v.DueCount, MonthlyAmount: v.MonthlyAmount, YearlyCost: v.YearlyCost,
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

// counterparties — each debt item's counterparty, with its balance now.
func (s *Service) counterparties(ctx context.Context, userID uuid.UUID, items []*itemRecord) (map[uuid.UUID]Counterparty, error) {
	out := map[uuid.UUID]Counterparty{}
	var contacts, insts, cards []uuid.UUID
	var keys []string
	for _, it := range items {
		if it.Type != TypeDebt {
			continue
		}
		if it.isCardBill() {
			cards = append(cards, *it.AccountID)
			continue
		}
		keys = append(keys, itemKey(it))
		if it.ContactID != nil {
			contacts = append(contacts, *it.ContactID)
		}
		if it.InstitutionID != nil {
			insts = append(insts, *it.InstitutionID)
		}
	}
	if len(keys) == 0 && len(cards) == 0 {
		return out, nil
	}
	arefs, err := shared.LoadAccountRefs(ctx, s.db, cards)
	if err != nil {
		return nil, err
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
	for _, it := range items {
		if it.Type != TypeDebt {
			continue
		}
		key := itemKey(it)
		c := Counterparty{Key: key, Kind: "person", Net: nets[key]}
		switch {
		case it.isCardBill():
			c.Kind = "card"
			if a, ok := arefs[*it.AccountID]; ok {
				c.Account, c.Name = &a, a.Name
			}
		case it.InstitutionID != nil:
			c.Kind = "institution"
			if x, ok := irefs[*it.InstitutionID]; ok {
				c.Institution, c.Name = &x, x.Name
			}
		case it.ContactID != nil:
			pr := shared.ResolvePerson(persons, shared.PersonKey{ContactID: it.ContactID, Snapshot: derefStr(it.PersonName)})
			c.Person, c.Name = &pr, pr.Name
		default:
			pr := shared.NamePerson(derefStr(it.PersonName))
			c.Person, c.Name = &pr, pr.Name
		}
		out[it.ID] = c
	}
	return out, rows.Err()
}

func itemKey(it *itemRecord) string {
	if it.isCardBill() {
		return "" // a card's bill: the card holds the debt, no debt rows
	}
	if it.InstitutionID != nil {
		return shared.InstitutionKey(*it.InstitutionID)
	}
	return shared.DebtKey(it.ContactID, derefStr(it.PersonName))
}

// ListRecurring — the recurring list: items, not lines. status current (active + paused, default) |
// archived | all; type = one of the 7; for_type = the quick-create picker (items with an active line
// that can take a transaction of that money type, likely first).
func (s *Service) ListRecurring(ctx context.Context, userID uuid.UUID, status, typ, q, forType string, hint PickerHint,
	page, perPage int, tz string) (*SummaryList, error) {
	page, perPage = clampPage(page, perPage)
	return s.listRecurring(ctx, userID, status, typ, q, forType, hint, page, perPage, tz)
}

// ListAll — every item in one page (the sync dataset); no 100-row cap.
func (s *Service) ListAll(ctx context.Context, userID uuid.UUID, tz string) (*SummaryList, error) {
	return s.listRecurring(ctx, userID, "all", "", "", "", PickerHint{}, 1, unlimitedPerPage, tz)
}

func (s *Service) listRecurring(ctx context.Context, userID uuid.UUID, status, typ, q, forType string, hint PickerHint,
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
	if typ != "" {
		args = append(args, typ)
		cond += fmt.Sprintf(` AND type = $%d`, len(args))
	}
	if forType != "" {
		args = append(args, forType)
		cond = fmt.Sprintf(`status = 'active' AND EXISTS (SELECT 1 FROM recurring_reminders l WHERE l.recurring_id = recurrings.id
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
	if err := attachPlans(ctx, s.db, userID, items...); err != nil {
		return nil, err
	}
	sums, keys, err := s.summaries(ctx, userID, today, items)
	if err != nil {
		return nil, err
	}
	likely := map[uuid.UUID]bool{}
	if forType != "" {
		for i, it := range items {
			likely[it.ID] = hint.likely(it.CategoryID, sums[i].Amount)
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

// summaries — the list rows of items (+ each one's order key: due first, then next date).
func (s *Service) summaries(ctx context.Context, userID uuid.UUID, today time.Time, items []*itemRecord) ([]RecurringSummary, []string, error) {
	pages, err := s.itemPages(ctx, userID, today, items, false)
	if err != nil {
		return nil, nil, err
	}
	out := make([]RecurringSummary, len(pages))
	keys := make([]string, len(pages))
	for i, p := range pages {
		sm := RecurringSummary{ID: p.ID, Name: p.Name, Type: p.Type, IconCode: p.IconCode, Status: p.Status, Category: p.Category,
			LinesCount: len(p.Lines), Chain: []ChainStep{}, MonthTotal: p.MonthTotal, Next: p.Next, DueCount: p.DueCount,
			Counterparty: p.Counterparty, DebtDirection: p.DebtDirection, Goal: p.Goal, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
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
				k := "1" + c.Date
				if c.Status == StatusDue {
					k = "0" + c.Date
				}
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
			sm.Amount, sm.Account, sm.PaidBy, sm.Chain, sm.Progress = ln.Amount, ln.Account, ln.PaidBy, ln.Chain, &pr
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
	cp.ID, cp.Status, cp.Plan, cp.Name = uuid.Nil, "active", nil, copyName(src.Name)
	if err := resolveCounterpartyTx(ctx, tx, userID, &cp); err != nil {
		return nil, err
	}
	if err := saveItemRecordTx(ctx, tx, &cp); err != nil {
		return nil, err
	}
	for _, r := range lines {
		l := *r
		l.ID, l.Status, l.PauseReason, l.PausedFrom, l.Gaps, l.ZeroRounds = uuid.Nil, "active", nil, nil, nil, 0
		l.GeneratedUntil, l.EndingNotified, l.TotalCount = nil, false, nil
		l.StartDate, l.TrackedFrom, l.DueNotifiedUntil = today, today, &today
		yesterday := today.AddDate(0, 0, -1)
		l.AutoCreatedUntil = &yesterday
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
		itemToLine(&cp, &l)
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
	if err := resolveCounterpartyTx(ctx, tx, userID, it); err != nil {
		return nil, err
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
	if err := saveAlertsTx(ctx, tx, r, in.Alerts); err != nil {
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

// DeleteLine — the line and its rounds; what they wrote stays. The last line goes only from an
// item whose card plan needs none.
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
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM recurring_reminders WHERE recurring_id = $1`, id).Scan(&n); err != nil {
		return nil, err
	}
	if n <= 1 {
		return nil, ErrLastLine
	}
	if _, err := tx.Exec(ctx, `DELETE FROM recurring_reminders WHERE id = $1 AND user_id = $2`, lineID, userID); err != nil {
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
