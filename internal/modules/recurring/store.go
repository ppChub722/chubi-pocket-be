package recurring

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
	"github.com/ppChub722/chubi-pocket-be/internal/shared/recurrence"
)

var (
	ErrNotFound          = errors.New("recurring setting not found")
	ErrItemNotFound      = errors.New("recurring item not found")
	ErrInvalidTransition = errors.New("this action doesn't apply in the current status")
	ErrAlreadyLinked     = errors.New("this item already has a pending draft or a transaction")
	ErrAccountNotFound   = errors.New("account not found")
	ErrInvalidCategory   = errors.New("invalid category")
	ErrTagNotFound       = errors.New("tag not found")
	ErrValidation        = errors.New("invalid request")
	ErrContactNotFound   = errors.New("contact not found")
	ErrContactArchived   = errors.New("the contact is archived")

	ErrAmountRequired  = errors.New("this occurrence has no amount yet: send one")
	ErrAlreadyRecorded = errors.New("this occurrence is already recorded")
)

// LinkConflict — a link that needs `replace`: the occurrence already has a
// transaction, or the transaction is already another occurrence's.
type LinkConflict struct {
	Linked *LinkedTx // the transaction the occurrence has now
	Other  *OccRef   // the other occurrence the transaction belongs to
}

// OccRef names an occurrence.
type OccRef struct {
	RuleID uuid.UUID `json:"rule_id"`
	Slot   string    `json:"slot"`
}

func (e *LinkConflict) Error() string { return ErrAlreadyLinked.Error() }
func (e *LinkConflict) Unwrap() error { return ErrAlreadyLinked }

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const ruleColumns = `id, user_id, name, description, note, icon_code, type, amount,
	account_id, to_account_id, category_id, tag_ids, freq, interval_n, weekdays,
	monthly_mode, month_day, month_nth, month_weekday, dates, start_date, end_kind,
	end_date, end_count, total_count, generated_until, status, ending_notified,
	created_at, updated_at, goal_id, kind, trial_until,
	tracked_from, due_notified_until, auto_create, auto_created_until, paused_from, pause_reason, gaps,
	contact_id, person_name, debt_kind, debt_direction, zero_rounds`

func scanRule(row pgx.Row) (*Rule, error) {
	var (
		r                                  Rule
		icon                               []byte
		weekdays                           []int32
		monthDay, monthNth, monthWD, count *int32
		total                              *int32
		interval                           int32
	)
	if err := row.Scan(&r.ID, &r.UserID, &r.Name, &r.Description, &r.Note, &icon, &r.Type, &r.Amount,
		&r.AccountID, &r.ToAccountID, &r.CategoryID, &r.TagIDs, &r.Freq, &interval, &weekdays,
		&r.MonthlyMode, &monthDay, &monthNth, &monthWD, &r.Dates, &r.StartDate, &r.EndKind,
		&r.EndDate, &count, &total, &r.GeneratedUntil, &r.Status, &r.EndingNotified,
		&r.CreatedAt, &r.UpdatedAt, &r.GoalID, &r.Kind, &r.TrialUntil,
		&r.TrackedFrom, &r.DueNotifiedUntil, &r.AutoCreate, &r.AutoCreatedUntil, &r.PausedFrom, &r.PauseReason, &r.Gaps,
		&r.ContactID, &r.PersonName, &r.DebtKind, &r.DebtDirection, &r.ZeroRounds); err != nil {
		return nil, err
	}
	r.IconCode = shared.IconFromJSON(icon)
	r.Interval = int(interval)
	for _, w := range weekdays {
		r.Weekdays = append(r.Weekdays, int(w))
	}
	toInt := func(p *int32) *int {
		if p == nil {
			return nil
		}
		v := int(*p)
		return &v
	}
	r.MonthDay, r.MonthNth, r.MonthWeekday, r.EndCount, r.TotalCount = toInt(monthDay), toInt(monthNth), toInt(monthWD), toInt(count), toInt(total)
	if r.TagIDs == nil {
		r.TagIDs = []uuid.UUID{}
	}
	// a setting migrated in without its end worked out (an old goal auto-save) gets it on read
	if r.TotalCount == nil && (r.EndKind == "date" || r.EndKind == "count" || r.Freq == "dates") {
		_ = r.Spec.Limits(r.StartDate)
	}
	return &r, nil
}

func getRule(ctx context.Context, q rowQuerier, userID, id uuid.UUID, lock bool) (*Rule, error) {
	sql := `SELECT ` + ruleColumns + ` FROM recurring_rules WHERE id = $1 AND user_id = $2`
	if lock {
		sql += ` FOR UPDATE`
	}
	r, err := scanRule(q.QueryRow(ctx, sql, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("rule: %w", err)
	}
	return r, nil
}

func saveRuleTx(ctx context.Context, tx pgx.Tx, r *Rule) error {
	icon, err := iconJSON(r.IconCode)
	if err != nil {
		return err
	}
	weekdays := make([]int32, 0, len(r.Weekdays))
	for _, w := range r.Weekdays {
		weekdays = append(weekdays, int32(w))
	}
	dates := r.Dates
	if dates == nil {
		dates = []time.Time{}
	}
	tags := r.TagIDs
	if tags == nil {
		tags = []uuid.UUID{}
	}
	args := []any{r.ID, r.UserID, r.Name, shared.CleanText(r.Description), shared.CleanText(r.Note), icon, r.Type, r.Amount,
		r.AccountID, r.ToAccountID, r.CategoryID, tags, r.Freq, r.Interval, weekdays,
		r.MonthlyMode, r.MonthDay, r.MonthNth, r.MonthWeekday, dates, r.StartDate, r.EndKind,
		r.EndDate, r.EndCount, r.TotalCount, r.GeneratedUntil, r.Status, r.EndingNotified, r.GoalID, r.Kind, r.TrialUntil,
		r.TrackedFrom, r.DueNotifiedUntil, r.AutoCreate, r.AutoCreatedUntil, r.PausedFrom, r.PauseReason, gapsJSON(r.Gaps),
		r.ContactID, r.PersonName, r.DebtKind, r.DebtDirection, r.ZeroRounds}
	if r.ID == uuid.Nil {
		r.ID, _ = uuid.NewV7()
		args[0] = r.ID
		_, err = tx.Exec(ctx, `INSERT INTO recurring_rules (id, user_id, name, description, note, icon_code, type, amount,
			account_id, to_account_id, category_id, tag_ids, freq, interval_n, weekdays,
			monthly_mode, month_day, month_nth, month_weekday, dates, start_date, end_kind,
			end_date, end_count, total_count, generated_until, status, ending_notified, goal_id, kind, trial_until,
			tracked_from, due_notified_until, auto_create, auto_created_until, paused_from, pause_reason, gaps,
			contact_id, person_name, debt_kind, debt_direction, zero_rounds)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $10, $11, $12, $13, $14, $15,
			        $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31,
			        $32, $33, $34, $35, $36, $37, $38::jsonb, $39, $40, $41, $42, $43)`, args...)
	} else {
		_, err = tx.Exec(ctx, `UPDATE recurring_rules SET name = $3, description = $4, note = $5, icon_code = $6::jsonb,
			type = $7, amount = $8, account_id = $9, to_account_id = $10, category_id = $11, tag_ids = $12,
			freq = $13, interval_n = $14, weekdays = $15, monthly_mode = $16, month_day = $17, month_nth = $18,
			month_weekday = $19, dates = $20, start_date = $21, end_kind = $22, end_date = $23, end_count = $24,
			total_count = $25, generated_until = $26, status = $27, ending_notified = $28, goal_id = $29, kind = $30, trial_until = $31,
			tracked_from = $32, due_notified_until = $33, auto_create = $34, auto_created_until = $35, paused_from = $36, pause_reason = $37, gaps = $38::jsonb,
			contact_id = $39, person_name = $40, debt_kind = $41, debt_direction = $42, zero_rounds = $43
			WHERE id = $1 AND user_id = $2`, args...)
	}
	if err != nil {
		return fmt.Errorf("save rule: %w", err)
	}
	return nil
}

func iconJSON(ic *shared.IconCode) ([]byte, error) {
	if ic == nil {
		return nil, nil
	}
	return json.Marshal(ic)
}

type itemRow struct {
	ID            uuid.UUID
	UserID        uuid.UUID
	RuleID        uuid.UUID // NOT NULL since 000068: every item belongs to a setting
	Seq           *int
	Slot          *time.Time // the rule date it was generated for (nil: a manual item)
	Date          time.Time
	Draft         pending.Draft
	Edited        bool
	Manual        bool
	Archived      bool
	ProducedAt    *time.Time
	PendingID     *uuid.UUID
	TransactionID *uuid.UUID
	SavingEntryID *uuid.UUID
	// DebtEntryID — kind debt (0.4.1-2 K3): the person-book entry that fills it.
	DebtEntryID *uuid.UUID
	// ClearedAt — paid without a transaction ("แม่จ่ายให้").
	ClearedAt *time.Time
	// AutoCreatedAt — the transaction was recorded by auto-create.
	AutoCreatedAt *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (it *itemRow) linked() bool {
	return it.PendingID != nil || it.filled()
}

// filled — a transaction, a saving entry or a debt entry fills it (status done).
func (it *itemRow) filled() bool {
	return it.TransactionID != nil || it.SavingEntryID != nil || it.DebtEntryID != nil
}

const itemColumns = `id, user_id, rule_id, seq, slot, date, draft, edited, manual, archived,
	produced_at, pending_id, transaction_id, saving_entry_id, cleared_at, auto_created_at, created_at, updated_at,
	debt_entry_id`

func scanItem(row pgx.Row) (*itemRow, error) {
	var (
		it    itemRow
		draft []byte
		seq   *int32
	)
	if err := row.Scan(&it.ID, &it.UserID, &it.RuleID, &seq, &it.Slot, &it.Date, &draft, &it.Edited, &it.Manual,
		&it.Archived, &it.ProducedAt, &it.PendingID, &it.TransactionID, &it.SavingEntryID, &it.ClearedAt, &it.AutoCreatedAt, &it.CreatedAt, &it.UpdatedAt,
		&it.DebtEntryID); err != nil {
		return nil, err
	}
	if seq != nil {
		v := int(*seq)
		it.Seq = &v
	}
	if err := json.Unmarshal(draft, &it.Draft); err != nil {
		return nil, fmt.Errorf("item draft: %w", err)
	}
	return &it, nil
}

func getItem(ctx context.Context, q rowQuerier, userID, id uuid.UUID, lock bool) (*itemRow, error) {
	sql := `SELECT ` + itemColumns + ` FROM recurring_items WHERE id = $1 AND user_id = $2`
	if lock {
		sql += ` FOR UPDATE`
	}
	it, err := scanItem(q.QueryRow(ctx, sql, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrItemNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("item: %w", err)
	}
	return it, nil
}

func insertItemTx(ctx context.Context, tx pgx.Tx, it *itemRow) (uuid.UUID, error) {
	id, _ := uuid.NewV7()
	if it.ID != uuid.Nil {
		id = it.ID
	}
	it.Draft.Date = ptr(it.Date.Format(dateLayout))
	draft, err := json.Marshal(it.Draft)
	if err != nil {
		return uuid.Nil, err
	}
	var seq *int32
	if it.Seq != nil {
		v := int32(*it.Seq)
		seq = &v
	}
	q := `INSERT INTO recurring_items (id, user_id, rule_id, seq, slot, date, draft, edited, manual, archived, produced_at,
			pending_id, transaction_id, saving_entry_id, cleared_at, auto_created_at, debt_entry_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`
	if it.Slot != nil {
		q += ` ON CONFLICT (rule_id, slot) WHERE slot IS NOT NULL DO NOTHING`
	}
	tag, err := tx.Exec(ctx, q, id, it.UserID, it.RuleID, seq, it.Slot, it.Date, draft, it.Edited, it.Manual, it.Archived, it.ProducedAt,
		it.PendingID, it.TransactionID, it.SavingEntryID, it.ClearedAt, it.AutoCreatedAt, it.DebtEntryID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert item: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return uuid.Nil, nil
	}
	it.ID = id
	return id, nil
}

func saveItemTx(ctx context.Context, tx pgx.Tx, it *itemRow) error {
	it.Draft.Date = ptr(it.Date.Format(dateLayout))
	draft, err := json.Marshal(it.Draft)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE recurring_items SET date = $3, draft = $4, edited = $5, archived = $6,
		produced_at = $7, pending_id = $8, transaction_id = $9, saving_entry_id = $10, cleared_at = $11,
		auto_created_at = $12, debt_entry_id = $13 WHERE id = $1 AND user_id = $2`,
		it.ID, it.UserID, it.Date, draft, it.Edited, it.Archived, it.ProducedAt, it.PendingID, it.TransactionID,
		it.SavingEntryID, it.ClearedAt, it.AutoCreatedAt, it.DebtEntryID)
	if err != nil {
		return fmt.Errorf("save item: %w", err)
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

func checkRefsTx(ctx context.Context, q rowQuerier, userID uuid.UUID, typ string, account, toAccount, category *uuid.UUID, tags []uuid.UUID) error {
	for _, acc := range []*uuid.UUID{account, toAccount} {
		if acc == nil {
			continue
		}
		var ok bool
		if err := q.QueryRow(ctx, `SELECT `+shared.ActiveMembershipPredicate("$1", "$2"), *acc, userID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return ErrAccountNotFound
		}
		var archived bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounts WHERE id = $1 AND status = 'archived')`, *acc).Scan(&archived); err != nil {
			return err
		}
		if archived {
			return transactions.ErrAccountArchived
		}
	}
	if typ == "transfer" && (account == nil || toAccount == nil || *account == *toAccount) {
		return ErrValidation
	}
	if category != nil {
		var ok bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM categories WHERE id = $1 AND user_id = $2
			AND NOT is_system AND type = $3)`, *category, userID, typ).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return ErrInvalidCategory
		}
	}
	if len(tags) > 0 {
		var n int
		if err := q.QueryRow(ctx, `SELECT COUNT(*) FROM tags WHERE user_id = $1 AND id = ANY($2)`, userID, tags).Scan(&n); err != nil {
			return err
		}
		if n != len(uniqueIDs(tags)) {
			return ErrTagNotFound
		}
	}
	return nil
}

func uniqueIDs(ids []uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// gapsJSON — the rule's gaps for the jsonb column ([] when none).
func gapsJSON(g []recurrence.Gap) []byte {
	if g == nil {
		g = []recurrence.Gap{}
	}
	b, _ := json.Marshal(g)
	return b
}
