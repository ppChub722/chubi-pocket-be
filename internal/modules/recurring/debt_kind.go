package recurring

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// ErrNothingOwed — a จ่ายหนี้ occurrence confirmed while the person owes nothing that way (and no
// amount was typed).
var ErrNothingOwed = errors.New("nothing is owed to or by this person now")

// WithDebts wires the person book: debt settings book their entries through it.
func (s *Service) WithDebts(d *personal_debts.Service) *Service {
	s.debts = d
	return s
}

// recordDebtTx — K4: the occurrence's person-book entry, in the caller's DB transaction: withTx
// (confirm / auto-create) gives it its transaction when the setting has a wallet; mark-paid books
// it without one (Q-K2). A จ่ายหนี้ occurrence not edited by hand asks min(amount, what is owed)
// now (K5); with nothing owed: ErrNothingOwed. Answers the wallets it moved.
func (s *Service) recordDebtTx(ctx context.Context, tx pgx.Tx, r *Rule, row *itemRow, date *time.Time, auto, withTx bool,
	lang string) ([]uuid.UUID, any, error) {
	if s.debts == nil || !r.isDebt() || r.PersonName == nil || r.DebtKind == nil || r.DebtDirection == nil {
		return nil, nil, ErrValidation
	}
	if row.filled() {
		return nil, nil, ErrAlreadyRecorded
	}
	d := row.Draft
	amount := 0.0
	if d.Amount != nil {
		amount = *d.Amount
	}
	if r.isRepayment() && !row.Edited {
		people, err := loadDebtPeople(ctx, tx, r.UserID, []*Rule{r})
		if err != nil {
			return nil, nil, err
		}
		amount = r.plannedDebtAmount(people.net(r))
		if amount <= 0 {
			return nil, nil, ErrNothingOwed
		}
	}
	if amount <= 0 {
		return nil, nil, ErrAmountRequired
	}
	slot := row.Date
	if row.Slot != nil {
		slot = *row.Slot
	}
	when := row.Date
	if date != nil {
		when = *date
	}
	desc := d.Description
	if desc == nil || *desc == "" {
		desc = ptr(occName(r, slot, seqPtr(seqIndex(r), slot), lang))
	}
	in := personal_debts.EntryInput{
		Kind: *r.DebtKind, Direction: *r.DebtDirection, ContactID: r.ContactID, PersonName: *r.PersonName,
		Amount: amount, Date: when.Format(dateLayout), Description: truncRunes(desc, 200), Note: d.Note,
	}
	var touched []uuid.UUID
	if withTx && d.AccountID != nil {
		in.CreateTransaction, in.AccountID = true, d.AccountID
		touched = append(touched, *d.AccountID)
	}
	e, _, err := s.debts.CreateEntryTx(ctx, tx, r.UserID, in)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	row.DebtEntryID, row.ProducedAt = &e.ID, &now
	row.Draft.Amount = &amount
	row.Archived, row.ClearedAt, row.AutoCreatedAt = false, nil, nil
	if auto {
		row.AutoCreatedAt = &now
	}
	if err := saveItemTx(ctx, tx, row); err != nil {
		return nil, nil, err
	}
	return touched, nil, nil
}

// debtFills — the entries that fill debt occurrences (amount, date, their transaction).
func debtFills(ctx context.Context, q shared.Querier, ids []uuid.UUID) (map[uuid.UUID]*DebtEntryRef, error) {
	out := map[uuid.UUID]*DebtEntryRef{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT pd.id, pd.kind, pd.direction, pd.amount::float8, to_char(pd.date, 'YYYY-MM-DD'),
			(SELECT t.id FROM transactions t WHERE t.source_personal_debt_id = pd.id LIMIT 1)
		FROM personal_debts pd WHERE pd.id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("item debt entries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e DebtEntryRef
		if err := rows.Scan(&e.ID, &e.Kind, &e.Direction, &e.Amount, &e.Date, &e.TransactionID); err != nil {
			return nil, err
		}
		out[e.ID] = &e
	}
	return out, rows.Err()
}

// debtItemViews — K3 / K5 on a page of items: the person, the entry that fills one, and a
// จ่ายหนี้ occurrence still to book asks min(amount, what is owed now).
func debtItemViews(ctx context.Context, q shared.Querier, userID uuid.UUID, occs []*occ, items []Item) error {
	var (
		rules []*Rule
		ids   []uuid.UUID
		seen  = map[uuid.UUID]bool{}
	)
	for _, o := range occs {
		if !o.rule.isDebt() {
			continue
		}
		if !seen[o.rule.ID] {
			seen[o.rule.ID] = true
			rules = append(rules, o.rule)
		}
		if o.row != nil && o.row.DebtEntryID != nil {
			ids = append(ids, *o.row.DebtEntryID)
		}
	}
	if len(rules) == 0 {
		return nil
	}
	people, err := loadDebtPeople(ctx, q, userID, rules)
	if err != nil {
		return err
	}
	fills, err := debtFills(ctx, q, ids)
	if err != nil {
		return err
	}
	for i, o := range occs {
		r, it := o.rule, &items[i]
		if !r.isDebt() {
			continue
		}
		it.Person, it.DebtKind, it.Direction = people.person(r), r.DebtKind, r.DebtDirection
		if o.row != nil && o.row.DebtEntryID != nil {
			if e, ok := fills[*o.row.DebtEntryID]; ok {
				it.DebtEntry = e
				amt := e.Amount
				it.Amount, it.Date = &amt, e.Date
				if e.TransactionID != nil {
					it.Transaction = &LinkedTx{ID: *e.TransactionID, Amount: &amt, Date: &e.Date}
				}
			}
			continue
		}
		if r.isRepayment() && o.expected() && (o.row == nil || !o.row.Edited) {
			it.Amount, it.NeedsAmount = amountPtr(r.plannedDebtAmount(people.net(r))), false
		}
	}
	return nil
}

// ruleKeySQL — a debt setting's person key (alias r), as shared.DebtKey writes it.
const ruleKeySQL = `(CASE WHEN r.contact_id IS NOT NULL THEN 'c:' || r.contact_id::text
	ELSE 'n:' || LOWER(TRIM(r.person_name)) END)`

// noBalanceOwedSQL — TRUE when a no_balance setting's person owes something its way again
// (the due-check guard and the resume read it; $1 = the user).
const noBalanceOwedSQL = `((SELECT COALESCE(SUM(l.signed), 0) FROM (` + "%s" + `) l WHERE l.key = ` + ruleKeySQL + `)
	* (CASE r.debt_direction WHEN 'paid' THEN -1 ELSE 1 END) > 0.005)`

func owedAgainSQL() string { return fmt.Sprintf(noBalanceOwedSQL, shared.DebtLedgerSQL("$1")) }

// repaymentRoundsTx — K5 at the due check, for an active จ่ายหนี้ setting: nothing owed → each due
// occurrence (not edited by hand) is skipped and counts a zero round; the 3rd pauses it
// (no_balance). Something owed → the count restarts and one occurrence waits at a time (older
// due ones are skipped: missed rounds are dropped, Q-K3).
func (s *Service) repaymentRoundsTx(ctx context.Context, tx pgx.Tx, r *Rule, today time.Time) error {
	if !r.isRepayment() || r.Status != "active" {
		return nil
	}
	due, err := s.dueOccs(ctx, tx, r, today)
	if err != nil || len(due) == 0 {
		return err
	}
	people, err := loadDebtPeople(ctx, tx, r.UserID, []*Rule{r})
	if err != nil {
		return err
	}
	skip := func(o *occ) error {
		row, err := s.store(ctx, tx, o)
		if err != nil {
			return err
		}
		row.Archived, row.ClearedAt, row.AutoCreatedAt = true, nil, nil
		return saveItemTx(ctx, tx, row)
	}
	changed := false
	if r.owedFor(people.net(r)) <= 0 {
		for _, o := range due {
			if o.row != nil && o.row.Edited {
				continue // a typed amount stays for the user to book
			}
			if err := skip(o); err != nil {
				return err
			}
			r.ZeroRounds++
			changed = true
			if r.ZeroRounds >= zeroRoundsLimit {
				if err := s.pauseTx(ctx, tx, r, nil, o.date.AddDate(0, 0, 1), PauseNoBalance); err != nil {
					return err
				}
				break
			}
		}
	} else {
		if r.ZeroRounds != 0 {
			r.ZeroRounds, changed = 0, true
		}
		for _, o := range due[:len(due)-1] { // the newest one waits
			if o.row != nil && o.row.Edited {
				continue
			}
			if err := skip(o); err != nil {
				return err
			}
		}
	}
	if !changed {
		return nil
	}
	return saveRuleTx(ctx, tx, r)
}

// resumeNoBalanceTx — default 5: a no_balance setting runs again by itself once a new debt makes
// its person owe something its way; the next occurrence is the current period's.
func (s *Service) resumeNoBalanceTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, today time.Time) error {
	rows, err := tx.Query(ctx, `SELECT `+ruleColumns+` FROM recurring_rules r WHERE r.user_id = $1
		AND r.status = 'paused' AND r.pause_reason = '`+PauseNoBalance+`' AND `+owedAgainSQL()+`
		ORDER BY r.created_at, r.id FOR UPDATE SKIP LOCKED`, userID)
	if err != nil {
		return fmt.Errorf("no_balance rules: %w", err)
	}
	var rules []*Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			rows.Close()
			return err
		}
		rules = append(rules, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range rules {
		if err := r.resume(today); err != nil {
			return err
		}
		if err := saveRuleTx(ctx, tx, r); err != nil {
			return err
		}
	}
	return nil
}

// linkDebtOcc — K3: an existing entry of the setting's person (same kind and direction) fills a
// debt occurrence. One entry ↔ one occurrence: a taken side needs replace (else 409 with the other).
func (s *Service) linkDebtOcc(ctx context.Context, userID, ruleID uuid.UUID, slot time.Time, entryID uuid.UUID, replace bool,
	tz string, byRow *itemRow) (*LinkResult, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	r, o, err := s.occOf(ctx, tx, userID, ruleID, slot, tz, byRow)
	if err != nil {
		return nil, err
	}
	if !r.isDebt() {
		return nil, ErrValidation
	}
	var (
		key, kind, dir string
		amount         float64
	)
	err = tx.QueryRow(ctx, `SELECT `+shared.DebtKeySQL("pd")+`, pd.kind, pd.direction, pd.amount::float8
		FROM personal_debts pd WHERE pd.id = $1 AND pd.user_id = $2`, entryID, userID).Scan(&key, &kind, &dir, &amount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrItemNotFound
	}
	if err != nil {
		return nil, err
	}
	if key != r.debtKey() || kind != *r.DebtKind || dir != *r.DebtDirection {
		return nil, ErrValidation
	}
	var otherID *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM recurring_items WHERE debt_entry_id = $1 AND user_id = $2 FOR UPDATE`,
		entryID, userID).Scan(&otherID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if otherID != nil && (o.row == nil || *otherID != o.row.ID) {
		other, err := getItem(ctx, tx, userID, *otherID, true)
		if err != nil {
			return nil, err
		}
		if !replace {
			os := other.Date
			if other.Slot != nil {
				os = *other.Slot
			}
			return nil, &LinkConflict{Other: &OccRef{RuleID: other.RuleID, Slot: os.Format(dateLayout)}}
		}
		other.DebtEntryID = nil // the other occurrence is due again
		if err := saveItemTx(ctx, tx, other); err != nil {
			return nil, err
		}
	}
	if o.row != nil && o.row.DebtEntryID != nil && *o.row.DebtEntryID != entryID && !replace {
		return nil, &LinkConflict{}
	}
	row, err := s.store(ctx, tx, o)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	row.DebtEntryID, row.AutoCreatedAt, row.Archived, row.ClearedAt = &entryID, nil, false, nil
	if row.ProducedAt == nil {
		row.ProducedAt = &now
	}
	if err := saveItemTx(ctx, tx, row); err != nil {
		return nil, err
	}
	out := &LinkResult{TransactionAmount: math.Round(amount*100) / 100}
	if r.Amount > 0 {
		ra := r.Amount
		out.RuleAmount = &ra
		out.AmountDiffers = math.Abs(r.Amount-amount) > 0.004
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	it, err := s.itemAfter(ctx, userID, ruleID, slot, byRow)
	if err != nil {
		return nil, err
	}
	out.Item = *it
	out.Setting = s.settingOf(ctx, userID, ruleOf(it))
	out.AccountBalances = []shared.WalletBalance{}
	out.Effects = s.effectsOf(ctx, userID, []Item{out.Item}, nil, nil, out.AccountBalances)
	return out, nil
}

// PersonRecurring — K13: the person's "หนี้" settings (not archived) for the person page, newest
// first. One query when there are none.
func (s *Service) PersonRecurring(ctx context.Context, userID uuid.UUID, key string) ([]shared.PersonRecurring, error) {
	out := []shared.PersonRecurring{}
	rows, err := s.db.Query(ctx, `SELECT `+ruleColumns+` FROM recurring_rules r
		WHERE r.user_id = $1 AND r.type = 'debt' AND r.status <> 'archived' AND `+ruleKeySQL+` = $2
		ORDER BY r.created_at DESC, r.id`, userID, key)
	if err != nil {
		return nil, fmt.Errorf("person recurring: %w", err)
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
	if err := rows.Err(); err != nil || len(rules) == 0 {
		return out, err
	}
	today, err := s.today(ctx, userID, shared.HeaderTZ(ctx))
	if err != nil {
		return nil, err
	}
	views, err := s.recurringViews(ctx, userID, today, rules)
	if err != nil {
		return nil, err
	}
	for _, v := range views {
		p := shared.PersonRecurring{ID: v.ID, Name: v.Name, IconCode: v.IconCode, NextDate: v.NextDate,
			NextAmount: v.NextAmount, DueCount: v.DueCount, Status: v.Status, PauseReason: v.PauseReason}
		if v.DebtKind != nil {
			p.DebtKind = *v.DebtKind
		}
		if v.Direction != nil {
			p.Direction = *v.Direction
		}
		if v.Amount != nil {
			p.Amount = *v.Amount
		}
		out = append(out, p)
	}
	return out, nil
}

// markPaidDebt — mark-paid on a debt occurrence books its entry without a transaction (Q-K2).
func (s *Service) markPaidDebt(ctx context.Context, userID uuid.UUID, r *Rule, o *occ, amount *float64) ([]uuid.UUID, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	row, err := s.store(ctx, tx, o)
	if err != nil {
		return nil, err
	}
	if amount != nil {
		row.Draft.Amount, row.Edited = amount, true
	}
	touched, _, err := s.recordDebtTx(ctx, tx, r, row, nil, false, false, s.lang(ctx, userID))
	if err != nil {
		return nil, err
	}
	return touched, tx.Commit(ctx)
}

// The "หนี้" kind (0.4.1-2 K): a setting tied to a person whose occurrences book person-book
// entries (personal_debts) instead of plain transactions.

// zeroRoundsLimit — a จ่ายหนี้ setting pauses (no_balance) after this many rounds in a row with
// nothing owed (Q-K4).
const zeroRoundsLimit = 3

// PauseNoBalance — a จ่ายหนี้ setting whose person owed nothing for zeroRoundsLimit rounds.
const PauseNoBalance = "no_balance"

func (r *Rule) isDebt() bool { return r.Type == typeDebt }

func (r *Rule) isRepayment() bool {
	return r.isDebt() && r.DebtKind != nil && *r.DebtKind == DebtKindRepayment
}

// debtKey — the person key (c:<contact> | n:<name>) of a debt setting; "" for other kinds.
func (r *Rule) debtKey() string {
	if !r.isDebt() {
		return ""
	}
	name := ""
	if r.PersonName != nil {
		name = *r.PersonName
	}
	return shared.DebtKey(r.ContactID, name)
}

// moneyWay — how a setting counts in the pay / receive totals (K8): its type, and for a debt
// setting its direction (paid = money out like an expense, received = in like an income).
func (r *Rule) moneyWay() string {
	if !r.isDebt() {
		return r.Type
	}
	if r.DebtDirection != nil && *r.DebtDirection == "paid" {
		return "expense"
	}
	return "income"
}

// owedFor — what a จ่ายหนี้ setting can still pay given the person's net (> 0 = they owe me): paid
// (I pay them back) needs me owing, received (they pay me back) needs them owing.
func (r *Rule) owedFor(net float64) float64 {
	v := net
	if r.DebtDirection != nil && *r.DebtDirection == "paid" {
		v = -net
	}
	if v < 0.005 {
		return 0
	}
	return math.Round(v*100) / 100
}

// plannedDebtAmount — K5: a จ่ายหนี้ occurrence asks min(the rule's amount, what is owed now);
// a จดหนี้ one the rule's amount.
func (r *Rule) plannedDebtAmount(net float64) float64 {
	if !r.isRepayment() {
		return r.Amount
	}
	return math.Min(r.Amount, r.owedFor(net))
}

// debtPeople — the person refs and nets of the debt settings among rules, in two queries at most
// (none without a debt setting).
type debtPeople struct {
	persons map[uuid.UUID]shared.PersonRef // by contact id
	nets    map[string]float64             // by person key
}

func loadDebtPeople(ctx context.Context, q shared.Querier, userID uuid.UUID, rules []*Rule) (debtPeople, error) {
	out := debtPeople{persons: map[uuid.UUID]shared.PersonRef{}, nets: map[string]float64{}}
	var (
		contacts []uuid.UUID
		keys     []string
	)
	for _, r := range rules {
		if !r.isDebt() {
			continue
		}
		keys = append(keys, r.debtKey())
		if r.ContactID != nil {
			contacts = append(contacts, *r.ContactID)
		}
	}
	if len(keys) == 0 {
		return out, nil
	}
	var err error
	if out.persons, err = shared.LoadPersons(ctx, q, userID, contacts); err != nil {
		return out, err
	}
	rows, err := q.Query(ctx, `SELECT l.key, COALESCE(SUM(l.signed), 0)
		FROM (`+shared.DebtLedgerSQL("$1")+`) l WHERE l.key = ANY($2) GROUP BY l.key`, userID, keys)
	if err != nil {
		return out, fmt.Errorf("debt nets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			k string
			v float64
		)
		if err := rows.Scan(&k, &v); err != nil {
			return out, err
		}
		out.nets[k] = math.Round(v*100) / 100
	}
	return out, rows.Err()
}

func (p debtPeople) person(r *Rule) *shared.PersonRef {
	if !r.isDebt() {
		return nil
	}
	if r.ContactID != nil {
		if x, ok := p.persons[*r.ContactID]; ok {
			return &x
		}
	}
	name := ""
	if r.PersonName != nil {
		name = *r.PersonName
	}
	return &shared.PersonRef{Name: name}
}

func (p debtPeople) net(r *Rule) float64 { return p.nets[r.debtKey()] }

// fillDebtView — the debt fields of a setting's view; nothing for other kinds.
func (p debtPeople) fillDebtView(v *Recurring, r *Rule) {
	if !r.isDebt() {
		return
	}
	net := p.net(r)
	v.Person, v.DebtKind, v.Direction, v.PersonBalance, v.ZeroRounds = p.person(r), r.DebtKind, r.DebtDirection, &net, r.ZeroRounds
}
