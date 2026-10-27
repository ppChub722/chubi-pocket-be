package personal_debts

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

func (s *Store) Pool() *pgxpool.Pool { return s.db }

var (
	ErrEntryNotFound      = errors.New("debt entry not found")
	ErrPersonNotFound     = errors.New("no entries with this person")
	ErrContactNotFound    = errors.New("contact not found")
	ErrTagsNeedWallet     = errors.New("tags go on the wallet transaction: pick a wallet first")
	ErrLedgerOnlyNoWallet = errors.New("a ledger-only entry has no wallet or tags: turn create_transaction on to use them")
	ErrMarkPaidNotFound   = errors.New("an entry to mark paid is not one of this person's")
	ErrManagedBySource    = errors.New("this entry comes from a split: only its description, note and paid label can change here")
	ErrBillNotFound       = errors.New("bill not found in an event you are a member of")
	// ErrManagedByTx — a paid-by debt is one record with its expense: edit or delete the transaction.
	ErrManagedByTx   = errors.New("this debt is the paid-by part of a transaction: edit or delete the transaction instead")
	ErrDueBeforeDate = errors.New("due_date can't be before the entry's date")
	// ErrTxNotLinkable — the transaction's type is not the entry's direction (paid = expense,
	// received = income), or it is not a plain transaction of mine (a debt, repayment, event or system row).
	ErrTxNotLinkable = errors.New("this transaction can't be linked to this entry")
	// ErrTxLinkedElsewhere — one transaction is linked to one entry.
	ErrTxLinkedElsewhere = errors.New("this transaction is already linked to another debt entry")
)

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// assertContactOwned returns ErrContactNotFound unless contactID is one of userID's own contacts.
func assertContactOwned(ctx context.Context, q querier, userID uuid.UUID, contactID *uuid.UUID) error {
	if contactID == nil {
		return nil
	}
	var ok bool
	if err := q.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM contacts WHERE id = $1 AND user_id = $2)`,
		*contactID, userID).Scan(&ok); err != nil {
		return fmt.Errorf("check contact owner: %w", err)
	}
	if !ok {
		return ErrContactNotFound
	}
	return nil
}

const entryColumns = `id, user_id, kind, direction,
	counterparty_contact_id, counterparty_person_name,
	source_transaction_id, source_project_transaction_id, project_id,
	amount, currency, to_char(date, 'YYYY-MM-DD'), paid, source_kind, description, note,
	created_at, updated_at, counterpart_debt_id, paid_for_transaction_id, to_char(due_date, 'YYYY-MM-DD'),
	disconnected_at`

func scanEntry(row pgx.Row) (*Entry, error) {
	var e Entry
	err := row.Scan(
		&e.ID, &e.UserID, &e.Kind, &e.Direction,
		&e.ContactID, &e.PersonName,
		&e.SourceTransactionID, &e.SourceProjectTransactionID, &e.ProjectID,
		&e.Amount, &e.Currency, &e.Date, &e.Paid, &e.SourceKind, &e.Description, &e.Note,
		&e.CreatedAt, &e.UpdatedAt, &e.CounterpartDebtID, &e.PaidForTransactionID, &e.DueDate,
		&e.DisconnectedAt,
	)
	return &e, err
}

func getEntry(ctx context.Context, q querier, userID, id uuid.UUID, forUpdate bool) (*Entry, error) {
	sql := `SELECT ` + entryColumns + ` FROM personal_debts WHERE id = $1 AND user_id = $2`
	if forUpdate {
		sql += ` FOR UPDATE`
	}
	e, err := scanEntry(q.QueryRow(ctx, sql, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrEntryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("entry: %w", err)
	}
	return e, nil
}

func getAnyEntryTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*Entry, error) {
	e, err := scanEntry(tx.QueryRow(ctx,
		`SELECT `+entryColumns+` FROM personal_debts WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrEntryNotFound
	}
	return e, err
}

func insertEntryTx(ctx context.Context, tx pgx.Tx, e *Entry) (*Entry, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}
	if e.Currency == "" {
		if err := tx.QueryRow(ctx, `SELECT currency FROM users WHERE id = $1`, e.UserID).Scan(&e.Currency); err != nil {
			return nil, fmt.Errorf("user currency: %w", err)
		}
	}
	if e.Kind == "" {
		e.Kind = KindDebt
	}
	row, err := scanEntry(tx.QueryRow(ctx, `INSERT INTO personal_debts
		(id, user_id, direction, counterparty_contact_id, counterparty_person_name,
		 source_transaction_id, source_project_transaction_id, project_id,
		 amount, currency, date, paid, source_kind, description, note,
		 created_by_user_id, updated_by_user_id, paid_for_transaction_id, due_date, kind)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::date, $12, $13, $14, $15, $2, $2, $16, $17::date, $18)
		RETURNING `+entryColumns,
		id, e.UserID, e.Direction, e.ContactID, strings.TrimSpace(e.PersonName),
		e.SourceTransactionID, e.SourceProjectTransactionID, e.ProjectID,
		e.Amount, strings.ToUpper(e.Currency), e.Date, e.Paid, e.SourceKind,
		shared.CleanText(e.Description), shared.CleanText(e.Note), e.PaidForTransactionID, e.DueDate, e.Kind))
	if err != nil {
		return nil, fmt.Errorf("insert entry: %w", err)
	}
	if e.ContactID != nil {
		_, _ = tx.Exec(ctx, `UPDATE contacts SET last_used_at = NOW() WHERE id = $1 AND user_id = $2`,
			*e.ContactID, e.UserID)
	}
	return row, nil
}

// ledgerSQL — the caller's ($1) person ledger: every person-book row (shared.DebtLedgerSQL).
var ledgerSQL = `(` + shared.DebtLedgerSQL("$1") + `)`

// peopleSQL — one row per person over the ledger (debts AND repayments, signed); keyFilter is
// appended to the ledger's WHERE (alias l).
func peopleSQL(keyFilter string) string {
	return `
		WITH e AS (
			SELECT l.* FROM ` + ledgerSQL + ` l WHERE TRUE ` + keyFilter + `
		), e2 AS (
			-- the running balance in book order; a cycle ends where it hits 0
			SELECT e.*, ROW_NUMBER() OVER w AS rn, SUM(signed) OVER w AS run
			FROM e WINDOW w AS (PARTITION BY key ORDER BY date, created_at, id)
		), e3 AS (
			SELECT e2.*, MAX(CASE WHEN ABS(run) < 0.005 THEN rn END) OVER (PARTITION BY key) AS cleared_rn
			FROM e2
		), g AS (
			SELECT key, (ARRAY_AGG(cid))[1] AS cid,
			       (ARRAY_AGG(snap ORDER BY created_at DESC))[1] AS snap,
			       SUM(signed) AS net,
			       COUNT(*) FILTER (WHERE NOT paid) AS open_count,
			       COUNT(*) AS entry_count, MAX(date) AS last,
			       MAX(date) FILTER (WHERE rn = cleared_rn) AS cleared,
			       MIN(date) FILTER (WHERE NOT paid AND (cleared_rn IS NULL OR rn > cleared_rn)) AS oldest_open,
			       MIN(date) AS first,
			       -- the earliest due date on each side; the outer select keeps the current side's
			       MIN(due_date) FILTER (WHERE NOT paid AND signed > 0) AS due_pos,
			       MIN(due_date) FILTER (WHERE NOT paid AND signed < 0) AS due_neg,
			       (ARRAY_AGG(kind ORDER BY rn DESC))[1] AS last_kind,
			       (ARRAY_AGG(id ORDER BY rn DESC))[1] AS last_id,
			       (ARRAY_AGG(signed ORDER BY rn DESC))[1] AS last_signed,
			       (ARRAY_AGG(descr ORDER BY rn DESC))[1] AS last_descr
			FROM e3 GROUP BY key
		)
		SELECT g.key, g.cid, COALESCE(` + shared.PersonNameSQL("c", "lu") + `, g.snap) AS name,
		       CASE WHEN c.id IS NOT NULL THEN ` + shared.PersonIconSQL("c", "lu") + ` END AS icon,
		       lu.id AS linked, g.net, g.open_count, g.entry_count, g.last,
		       to_char(g.cleared, 'YYYY-MM-DD') AS cleared_at,
		       (` + todayInUserTZ + ` - g.oldest_open) AS days_out,
		       to_char(g.first, 'YYYY-MM-DD') AS first_at, g.last_kind, g.last_id, g.last_signed, g.last_descr,
		       to_char(CASE WHEN g.net > 0.005 THEN g.due_pos WHEN g.net < -0.005 THEN g.due_neg END,
		               'YYYY-MM-DD') AS next_due
		FROM g
		LEFT JOIN contacts c ON c.id = g.cid AND c.user_id = $1
		LEFT JOIN users lu ON lu.id = c.linked_user_id`
}

const todayInUserTZ = `(NOW() AT TIME ZONE COALESCE(
	(SELECT NULLIF(up.preferences->>'timezone', '') FROM user_preferences up
	 WHERE up.user_id = $1), 'Asia/Bangkok'))::date`

const TodayInUserTZSQL = todayInUserTZ

const openSQL = `(ABS(p.net) > 0.005 OR p.open_count > 0)`

// peopleCols — the columns scanPerson reads from peopleSQL (alias p).
const peopleCols = `key, cid, name, icon, linked, net, open_count, entry_count, to_char(last, 'YYYY-MM-DD'), cleared_at, days_out,
	first_at, last_kind, last_id, last_signed, last_descr, next_due`

func scanPerson(rows pgx.Rows) (DebtPerson, error) {
	var (
		p      DebtPerson
		cid    *uuid.UUID
		name   string
		icon   []byte
		linked *uuid.UUID
		last   *string
		la     LastActivity
		lkind  *string
		lid    *uuid.UUID
		signed *float64
	)
	if err := rows.Scan(&p.Key, &cid, &name, &icon, &linked, &p.Net, &p.OpenCount, &p.EntryCount, &last, &p.ClearedAt, &p.DaysOutstanding,
		&p.FirstActivityAt, &lkind, &lid, &signed, &la.Title, &p.NextDueDate); err != nil {
		return p, err
	}
	p.Net = round2(p.Net)
	p.Side = shared.DebtSide(p.Net)
	p.Person = shared.PersonRef{Name: name, ContactID: cid, Linked: linked != nil, LinkedUserID: linked}
	if cid != nil {
		p.Person.IconCode = shared.IconFromJSON(icon)
	}
	p.LastActivityAt = last
	if lkind != nil && lid != nil && signed != nil && last != nil {
		la.Kind, la.ID, la.SignedAmount, la.Date = *lkind, *lid, round2(*signed), *last
		la.Amount = math.Abs(la.SignedAmount)
		la.Direction = DirectionPaid
		if la.SignedAmount < 0 {
			la.Direction = DirectionReceived
		}
		p.LastActivity = &la
	}
	return p, nil
}

func (s *Store) listPeople(ctx context.Context, userID uuid.UUID, f PeopleFilter) ([]DebtPerson, int, error) {
	where := []string{"TRUE"}
	args := []any{userID}
	if !f.All {
		where = append(where, openSQL)
	}
	switch f.Side {
	case "owed_to_me", "receivable":
		where = append(where, "p.net > 0.005")
	case "i_owe", "payable":
		where = append(where, "p.net < -0.005")
	case "overdue_30":
		where = append(where, "p.days_out > 30")
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		args = append(args, "%"+shared.EscapeLike(q)+"%")
		where = append(where, fmt.Sprintf("p.name ILIKE $%d", len(args)))
	}
	base := `SELECT ` + peopleCols + `
		FROM (` + peopleSQL("") + `) p WHERE ` + strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM (`+base+`) x`, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("people count: %w", err)
	}
	args = append(args, f.PerPage, (f.Page-1)*f.PerPage)
	rows, err := s.db.Query(ctx, base+fmt.Sprintf(` ORDER BY `+peopleOrder(f.Sort)+`, p.key
		LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("people: %w", err)
	}
	defer rows.Close()
	out := make([]DebtPerson, 0)
	for rows.Next() {
		p, err := scanPerson(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

func (s *Store) totals(ctx context.Context, q shared.Querier, userID uuid.UUID) (DebtTotals, error) {
	var t DebtTotals
	rows, err := q.Query(ctx, `
		SELECT COALESCE(SUM(p.net) FILTER (WHERE p.net > 0.005), 0),
		       COALESCE(SUM(-p.net) FILTER (WHERE p.net < -0.005), 0),
		       COUNT(*) FILTER (WHERE `+openSQL+`),
		       COUNT(*) FILTER (WHERE p.net > 0.005),
		       COUNT(*) FILTER (WHERE p.net < -0.005),
		       COUNT(*) FILTER (WHERE p.days_out > 30)
		FROM (`+peopleSQL("")+`) p`, userID)
	if err != nil {
		return t, fmt.Errorf("debt totals: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&t.OwedToMe, &t.IOwe, &t.OpenCount, &t.OwedToMePeople, &t.IOwePeople, &t.Over30DaysPeople); err != nil {
			return t, err
		}
	}
	t.OwedToMe, t.IOwe = round2(t.OwedToMe), round2(t.IOwe)
	t.Net = round2(t.OwedToMe - t.IOwe)
	t.ReceivableTotal, t.PayableTotal = t.OwedToMe, t.IOwe
	t.Counts = PeopleCounts{All: t.OpenCount, Receivable: t.OwedToMePeople, Payable: t.IOwePeople, Overdue30: t.Over30DaysPeople}
	return t, rows.Err()
}

func (s *Store) personBalance(ctx context.Context, q shared.Querier, userID uuid.UUID, k shared.DebtPersonKey) (DebtPerson, bool, error) {
	key := k.Key()
	rows, err := q.Query(ctx, `SELECT `+peopleCols+`
		FROM (`+peopleSQL("AND l.key = $2")+`) p`, userID, key)
	if err != nil {
		return DebtPerson{}, false, fmt.Errorf("person balance: %w", err)
	}
	var (
		p     DebtPerson
		found bool
	)
	if rows.Next() {
		if p, err = scanPerson(rows); err != nil {
			rows.Close()
			return DebtPerson{}, false, err
		}
		found = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return DebtPerson{}, false, err
	}
	if found {
		return p, true, nil
	}
	p = DebtPerson{Key: key, Person: shared.PersonRef{Name: k.Name}, Side: shared.DebtSide(0)}
	if k.ContactID != nil {
		persons, err := shared.LoadPersons(ctx, q, userID, []uuid.UUID{*k.ContactID})
		if err != nil {
			return DebtPerson{}, false, err
		}
		if ref, ok := persons[*k.ContactID]; ok {
			p.Person = ref
		}
	}
	return p, false, nil
}

const entryViewSQL = `SELECT pd.id, pd.kind, pd.direction, pd.counterparty_contact_id, pd.counterparty_person_name,
		pd.amount, to_char(pd.date, 'YYYY-MM-DD'), pd.description, pd.note, pd.paid, pd.source_kind,
		CASE pd.source_kind WHEN 'transaction' THEN pd.source_transaction_id
		                    WHEN 'event' THEN pd.project_id
		                    WHEN 'paid_by' THEN pd.paid_for_transaction_id
		                    WHEN 'partner' THEN pd.paid_for_transaction_id END,
		CASE pd.source_kind WHEN 'transaction' THEN COALESCE(st.description, sc.name)
		                    WHEN 'event' THEN sp.name
		                    WHEN 'paid_by' THEN COALESCE(ft.description, fc.name)
		                    WHEN 'partner' THEN ou.display_name END,
		t.id, t.account_id, COALESCE(t.debt_connected, FALSE), pd.created_at, pd.updated_at,
		to_char(pd.due_date, 'YYYY-MM-DD'), pd.counterpart_debt_id IS NOT NULL, pd.disconnected_at,
		-- origin (the มาจาก card, debts only): kind, transaction, bill, event, title, amount, date, wallet
		CASE WHEN pd.kind = 'debt' THEN
		  CASE pd.source_kind WHEN 'transaction' THEN 'split' WHEN 'paid_by' THEN 'paid_by'
		                      WHEN 'partner' THEN 'partner' WHEN 'event' THEN 'event'
		                      ELSE CASE WHEN t.id IS NOT NULL THEN 'cash' END END END,
		CASE pd.source_kind WHEN 'transaction' THEN st.id WHEN 'paid_by' THEN ft.id WHEN 'partner' THEN ft.id
		                    WHEN 'event' THEN NULL ELSE t.id END,
		CASE WHEN pd.source_kind = 'event' THEN pd.source_project_transaction_id END,
		CASE WHEN pd.source_kind = 'event' THEN pd.project_id END,
		CASE pd.source_kind WHEN 'transaction' THEN COALESCE(st.description, sc.name)
		                    WHEN 'paid_by' THEN COALESCE(ft.description, fc.name)
		                    WHEN 'partner' THEN COALESCE(ft.description, fc.name, ou.display_name)
		                    WHEN 'event' THEN COALESCE(bp.description, sp.name)
		                    ELSE COALESCE(t.description, tc.name) END,
		(CASE pd.source_kind WHEN 'transaction' THEN st.amount WHEN 'paid_by' THEN ft.amount WHEN 'partner' THEN ft.amount
		                     WHEN 'event' THEN bp.amount ELSE t.amount END)::float8,
		to_char(CASE pd.source_kind WHEN 'transaction' THEN st.date WHEN 'paid_by' THEN ft.date WHEN 'partner' THEN ft.date
		                            WHEN 'event' THEN bp.date ELSE t.date END, 'YYYY-MM-DD'),
		CASE pd.source_kind WHEN 'transaction' THEN st.account_id WHEN 'paid_by' THEN ft.account_id
		                    WHEN 'partner' THEN ft.account_id WHEN 'event' THEN NULL ELSE t.account_id END
	FROM personal_debts pd
	LEFT JOIN transactions t ON t.source_personal_debt_id = pd.id
	LEFT JOIN categories tc ON tc.id = t.category_id
	LEFT JOIN transactions st ON st.id = pd.source_transaction_id
	LEFT JOIN categories sc ON sc.id = st.category_id
	LEFT JOIN transactions ft ON ft.id = pd.paid_for_transaction_id
	LEFT JOIN categories fc ON fc.id = ft.category_id
	LEFT JOIN projects sp ON sp.id = pd.project_id
	LEFT JOIN project_transactions bp ON bp.id = pd.source_project_transaction_id
	LEFT JOIN personal_debts op ON op.id = pd.counterpart_debt_id
	LEFT JOIN users ou ON ou.id = op.user_id`

func loadViews(ctx context.Context, q shared.Querier, tail string, args ...any) ([]DebtEntry, error) {
	rows, err := q.Query(ctx, entryViewSQL+" "+tail, args...)
	if err != nil {
		return nil, fmt.Errorf("entries: %w", err)
	}
	const (
		accTx = iota
		accOrigin
	)
	type acc struct {
		i, of int
		id    uuid.UUID
	}
	var (
		out  = make([]DebtEntry, 0)
		accs []acc
		ids  []uuid.UUID
	)
	for rows.Next() {
		var (
			v         DebtEntry
			cid       *uuid.UUID
			name      string
			kind      *string
			sourceID  *uuid.UUID
			title     *string
			txID      *uuid.UUID
			accID     *uuid.UUID
			o         DebtOrigin
			oKind     *string
			oAccount  *uuid.UUID
			connected bool
		)
		if err := rows.Scan(&v.ID, &v.Kind, &v.Direction, &cid, &name, &v.Amount, &v.Date, &v.Description, &v.Note,
			&v.Paid, &kind, &sourceID, &title, &txID, &accID, &connected, &v.CreatedAt, &v.UpdatedAt, &v.DueDate, &v.PartnerLinked, &v.DisconnectedAt,
			&oKind, &o.TransactionID, &o.BillID, &o.EventID, &o.Title, &o.Amount, &o.Date, &oAccount); err != nil {
			rows.Close()
			return nil, fmt.Errorf("entries scan: %w", err)
		}
		v.Key = shared.DebtKey(cid, name)
		if kind != nil {
			v.Source = &DebtSource{Kind: *kind, ID: sourceID, Title: title}
		}
		if txID != nil {
			v.Transaction = &EntryTx{ID: *txID, Connected: connected}
			if accID != nil {
				accs = append(accs, acc{len(out), accTx, *accID})
				ids = append(ids, *accID)
			}
		}
		if oKind != nil {
			o.Kind = *oKind
			v.Origin = &o
			if oAccount != nil {
				accs = append(accs, acc{len(out), accOrigin, *oAccount})
				ids = append(ids, *oAccount)
			}
		}
		out = append(out, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	refs, err := shared.LoadAccountRefs(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	for _, a := range accs {
		r, ok := refs[a.id]
		if !ok {
			continue
		}
		switch a.of {
		case accOrigin:
			out[a.i].Origin.Account = &r
		default:
			out[a.i].Transaction.Account = &r
		}
	}
	return out, nil
}

func entryView(ctx context.Context, q shared.Querier, userID, id uuid.UUID) (*DebtEntry, error) {
	vs, err := loadViews(ctx, q, `WHERE pd.id = $1 AND pd.user_id = $2`, id, userID)
	if err != nil {
		return nil, err
	}
	if len(vs) == 0 {
		return nil, ErrEntryNotFound
	}
	return &vs[0], nil
}

func (s *Store) listEntries(ctx context.Context, userID uuid.UUID, key string, f EntryFilter) ([]DebtEntry, int, error) {
	where := []string{"pd.user_id = $1", shared.DebtKeySQL("pd") + " = $2"}
	args := []any{userID, key}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	switch f.Kind {
	case "all":
	case KindRepayment:
		where = append(where, "pd.kind = 'repayment'")
	default:
		where = append(where, "pd.kind = 'debt'")
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		add("(pd.description ILIKE $%[1]d OR pd.note ILIKE $%[1]d)", "%"+shared.EscapeLike(q)+"%")
	}
	if f.Direction != "" {
		add("pd.direction = $%d", f.Direction)
	}
	if f.Paid != nil {
		add("pd.paid = $%d", *f.Paid)
	}
	if f.Money {
		where = append(where, "t.id IS NOT NULL")
	}
	if f.AccountID != nil {
		add("t.account_id = $%d", *f.AccountID)
	}
	if f.From != nil {
		add("pd.date >= $%d::date", *f.From)
	}
	if f.To != nil {
		add("pd.date <= $%d::date", *f.To)
	}
	mark, err := s.cycleBoundary(ctx, s.db, userID, key)
	if err != nil {
		return nil, 0, err
	}
	if mark != nil && (f.Cycle == "current" || f.Cycle == "earlier") {
		op := ">"
		if f.Cycle == "earlier" {
			op = "<="
		}
		args = append(args, mark.Date, mark.Created, mark.ID)
		where = append(where, fmt.Sprintf("(pd.date, pd.created_at, pd.id) %s ($%d::date, $%d::timestamptz, $%d::uuid)",
			op, len(args)-2, len(args)-1, len(args)))
	} else if mark == nil && f.Cycle == "earlier" {
		where = append(where, "FALSE")
	}
	w := strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM personal_debts pd
		LEFT JOIN transactions t ON t.source_personal_debt_id = pd.id WHERE `+w, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("entries count: %w", err)
	}
	order := "pd.date DESC, pd.created_at DESC"
	switch f.Sort {
	case "date_asc":
		order = "pd.date ASC, pd.created_at DESC"
	case "amount_desc":
		order = "pd.amount DESC, pd.created_at DESC"
	case "amount_asc":
		order = "pd.amount ASC, pd.created_at DESC"
	}
	args = append(args, f.PerPage, (f.Page-1)*f.PerPage)
	out, err := loadViews(ctx, s.db, fmt.Sprintf(`WHERE %s ORDER BY %s, pd.id LIMIT $%d OFFSET $%d`,
		w, order, len(args)-1, len(args)), args...)
	if err == nil {
		for i := range out {
			cur := mark.after(out[i].Date, out[i].CreatedAt, out[i].ID)
			out[i].InCurrentCycle = &cur
		}
	}
	return out, total, err
}

// statementDays — the page's items grouped by day (in the items' order): each day's balance change
// and the balance at its end. A day split across pages shows its part on each page.
func statementDays(items []StatementItem) []StatementDay {
	out := []StatementDay{}
	idx := map[string]int{}
	for _, it := range items {
		i, ok := idx[it.Date]
		if !ok {
			i = len(out)
			idx[it.Date] = i
			out = append(out, StatementDay{Date: it.Date, BalanceAfter: it.RunningBalance})
		}
		d := &out[i]
		d.Change = round2(d.Change + it.SignedAmount)
		d.Count++
		// the day's last line in book order holds the day's closing balance
		if d.lastSeen == nil || it.CreatedAt.After(*d.lastSeen) {
			t := it.CreatedAt
			d.lastSeen, d.BalanceAfter = &t, it.RunningBalance
		}
	}
	return out
}

// debtItem — a person-book row as a statement line (debt or repayment; a repayment has no paid
// label).
func debtItem(d DebtEntry) StatementItem {
	signed := d.Amount
	if d.Direction != DirectionPaid {
		signed = -d.Amount
	}
	it := StatementItem{
		Kind: d.Kind, ID: d.ID, Key: d.Key, Direction: d.Direction, Amount: d.Amount, SignedAmount: signed,
		Date: d.Date, Description: d.Description, Note: d.Note, Source: d.Source,
		Transaction: d.Transaction, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt, Origin: d.Origin,
	}
	if d.Kind != KindRepayment {
		paid := d.Paid
		it.Paid = &paid
	}
	return it
}

func entryTxID(ctx context.Context, q querier, entryID uuid.UUID) (*uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM transactions WHERE source_personal_debt_id = $1`, entryID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("entry tx: %w", err)
	}
	return &id, nil
}

func (s *Store) UnlinkedNames(ctx context.Context, userID uuid.UUID) ([]struct {
	Name  string
	Count int
}, error) {
	rows, err := s.db.Query(ctx, `SELECT counterparty_person_name, COUNT(*) FROM personal_debts
		WHERE user_id = $1 AND counterparty_contact_id IS NULL AND counterparty_person_name <> ''
		GROUP BY counterparty_person_name
		ORDER BY COUNT(*) DESC, LOWER(counterparty_person_name)`, userID)
	if err != nil {
		return nil, fmt.Errorf("unlinked names: %w", err)
	}
	defer rows.Close()
	out := make([]struct {
		Name  string
		Count int
	}, 0)
	for rows.Next() {
		var name string
		var count int
		if err := rows.Scan(&name, &count); err != nil {
			return nil, err
		}
		out = append(out, struct {
			Name  string
			Count int
		}{name, count})
	}
	return out, rows.Err()
}

func (s *Store) AbsorbForContact(ctx context.Context, userID, contactID uuid.UUID, names []string) (int, error) {
	if len(names) == 0 {
		return 0, nil
	}
	lowered := make([]string, len(names))
	for i, n := range names {
		lowered[i] = strings.ToLower(strings.TrimSpace(n))
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	var displayName string
	if err := tx.QueryRow(ctx,
		`SELECT display_name FROM contacts WHERE id = $1 AND user_id = $2`,
		contactID, userID).Scan(&displayName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("contact not found or not owned: %s", contactID)
		}
		return 0, fmt.Errorf("contact lookup: %w", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE personal_debts
		SET counterparty_contact_id = $1, counterparty_person_name = $2, updated_by_user_id = $3
		WHERE user_id = $3 AND counterparty_contact_id IS NULL
		  AND LOWER(TRIM(counterparty_person_name)) = ANY($4::text[])`,
		contactID, displayName, userID, lowered)
	if err != nil {
		return 0, fmt.Errorf("absorb: %w", err)
	}
	if tag.RowsAffected() > 0 {
		_, _ = tx.Exec(ctx, `UPDATE contacts SET last_used_at = NOW() WHERE id = $1 AND user_id = $2`,
			contactID, userID)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (s *Store) RestoreOnContactDeleteTx(ctx context.Context, tx pgx.Tx, userID, contactID uuid.UUID) (int, error) {
	var displayName string
	if err := tx.QueryRow(ctx,
		`SELECT `+shared.PersonNameSQL("c", "u")+` FROM contacts c
		 LEFT JOIN users u ON u.id = c.linked_user_id
		 WHERE c.id = $1 AND c.user_id = $2`,
		contactID, userID).Scan(&displayName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("contact lookup: %w", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE personal_debts
		SET counterparty_person_name = $1, updated_by_user_id = $2
		WHERE user_id = $2 AND counterparty_contact_id = $3`,
		displayName, userID, contactID)
	if err != nil {
		return 0, fmt.Errorf("restore on delete: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func round2(f float64) float64 {
	if f < 0 {
		return -float64(int64(-f*100+0.5)) / 100
	}
	return float64(int64(f*100+0.5)) / 100
}

func peopleOrder(sort string) string {
	switch sort {
	case "amount":
		return `ABS(p.net) DESC, p.last DESC NULLS LAST`
	case "recent":
		return `p.last DESC NULLS LAST, ABS(p.net) DESC`
	}
	return `p.days_out DESC NULLS LAST, ABS(p.net) DESC, p.last DESC NULLS LAST`
}
