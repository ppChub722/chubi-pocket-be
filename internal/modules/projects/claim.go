package projects

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Claims (0.4.1-2 V6 lump, V9 claim from my book, V11 debt-type bills, X7): the real pairs of each
// bill between me and one other member, booked into my book. A bill + person is booked once (the
// personal_debt_bills unique index; for a payback, its row's source).

var (
	ErrClaimMixedDirection = errors.New("the bills of one claim must all go the same way (all they owe me, or all I owe them)")
	ErrClaimLinkOne        = errors.New("link_transaction_id goes with one expense / income bill")
	ErrClaimTxNotLinkable  = errors.New("that transaction can't be connected to the bill: it must be my plain transaction of the same type")
)

// ClaimError — bills refused by a claim (details.bill_ids).
type ClaimError struct {
	Err     error
	BillIDs []uuid.UUID
}

func (e *ClaimError) Error() string { return e.Err.Error() }
func (e *ClaimError) Unwrap() error { return e.Err }

var (
	ErrNotInBill = errors.New("no pair with that member on the bill")
)

// ClaimableBill — one bill's pair between me and another member.
type ClaimableBill struct {
	PtID            uuid.UUID        `json:"pt_id"`
	Type            string           `json:"type"`
	PaybackKind     *string          `json:"payback_kind"`
	Date            string           `json:"date"`
	Description     *string          `json:"description"`
	BillAmount      float64          `json:"bill_amount"`
	Payer           shared.MemberRef `json:"payer"`
	Payers          []BillPayer      `json:"payers"`
	Other           shared.MemberRef `json:"other"`
	Direction       string           `json:"direction"`
	Amount          float64          `json:"amount"`
	MyTransactionID *uuid.UUID       `json:"my_transaction_id"`
	Claimed         bool             `json:"claimed"`
	ClaimedDebtID   *uuid.UUID       `json:"claimed_debt_id"`

	payer, other uuid.UUID
}

type ClaimableTotal struct {
	Member         shared.MemberRef `json:"member"`
	UnclaimedNet   float64          `json:"unclaimed_net"`
	UnclaimedCount int              `json:"unclaimed_count"`
}

type ClaimableBillList struct {
	Data   []ClaimableBill  `json:"data"`
	Totals []ClaimableTotal `json:"totals"`
}

type ClaimBillsRequest struct {
	MemberID          uuid.UUID   `json:"member_id" binding:"required"`
	BillIDs           []uuid.UUID `json:"bill_ids"  binding:"required,min=1"`
	CreateCopies      *bool       `json:"create_copies"`
	LinkTransactionID *uuid.UUID  `json:"link_transaction_id"`
	Description       *string     `json:"description" binding:"omitempty,max=200"`
	Note              *string     `json:"note"        binding:"omitempty,max=500"`
	DueDate           *string     `json:"due_date"    binding:"omitempty,datetime=2006-01-02"`
	Preview           bool        `json:"preview"`
}

type ClaimBillsResult struct {
	Debts   []any          `json:"debts"`
	Effects shared.Effects `json:"effects"`
	Bills   []BoardRow     `json:"bills"`
}

// claimablePairsTx — my pairs per bill of the event (memberID nil = with everyone).
func claimablePairsTx(ctx context.Context, q rowQuerier, userID, projectID, me uuid.UUID, memberID *uuid.UUID) ([]ClaimableBill, error) {
	// the real pairs (bill_pairs: multi-payer aware), netted per bill + other member (> 0 =
	// they owe me)
	rows, err := q.Query(ctx, `
		WITH mine AS (
			SELECT v.pt_id, CASE WHEN v.creditor = $3 THEN v.debtor ELSE v.creditor END AS other,
			       SUM(CASE WHEN v.creditor = $3 THEN v.amount ELSE -v.amount END) AS net
			FROM bill_pairs v
			WHERE v.project_id = $1 AND $3 IN (v.debtor, v.creditor)
			GROUP BY 1, 2
		)
		SELECT p.id, p.type, p.payback_kind, to_char(p.date, 'YYYY-MM-DD'), p.description, p.amount::float8,
		       p.transaction_member_id, m.other, m.net::float8,
		       (SELECT t.id FROM transactions t WHERE t.user_id = $2 AND (t.id = p.linked_transaction_id
		          OR (t.split_transaction_id = p.id AND t.source_project_transaction_id IS NOT NULL))
		        ORDER BY (t.id = p.linked_transaction_id) DESC LIMIT 1)
		FROM mine m JOIN project_transactions p ON p.id = m.pt_id
		WHERE ABS(m.net) > 0.005
		ORDER BY p.date DESC, p.created_at DESC, p.id, m.other`, projectID, userID, me)
	if err != nil {
		return nil, fmt.Errorf("claimable: %w", err)
	}
	var out []ClaimableBill
	for rows.Next() {
		var (
			b   ClaimableBill
			net float64
		)
		if err := rows.Scan(&b.PtID, &b.Type, &b.PaybackKind, &b.Date, &b.Description, &b.BillAmount,
			&b.payer, &b.other, &net, &b.MyTransactionID); err != nil {
			rows.Close()
			return nil, err
		}
		if memberID != nil && b.other != *memberID {
			continue
		}
		b.Direction = personal_debts.DirectionReceived
		if net > 0 {
			b.Direction = personal_debts.DirectionPaid
		}
		if b.Type == PTTypePayback && b.PaybackKind == nil {
			k := "payback"
			b.PaybackKind = &k
		}
		b.Amount = round2(math.Abs(net))
		out = append(out, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// V12: after I booked the simplified settle, my real pairs count as claimed
	booking, err := mySettleBooking(ctx, q, projectID, userID)
	if err != nil {
		return nil, err
	}
	// claimed: a debt part of that bill + person, or (a debt-type bill) a row of mine from it
	for i := range out {
		if booking != nil {
			out[i].Claimed = true
			continue
		}
		b := &out[i]
		var id uuid.UUID
		var err error
		if b.Type == PTTypePayback {
			err = q.QueryRow(ctx, `SELECT id FROM personal_debts WHERE user_id = $1 AND source_project_transaction_id = $2
				ORDER BY created_at LIMIT 1`, userID, b.PtID).Scan(&id)
		} else {
			err = q.QueryRow(ctx, `SELECT debt_id FROM personal_debt_bills WHERE user_id = $1 AND pt_id = $2 AND member_id = $3`,
				userID, b.PtID, b.other).Scan(&id)
		}
		switch {
		case err == nil:
			b.Claimed, b.ClaimedDebtID = true, &id
		case !errors.Is(err, pgx.ErrNoRows):
			return nil, fmt.Errorf("claimed: %w", err)
		}
	}
	return out, nil
}

// paybackKindOf — a payback parent's kind (NULL stored = payback); nil on other bills.
func paybackKindOf(p ProjectTransaction) *string {
	if p.Type != PTTypePayback || p.ParentProjectTransactionID != nil {
		return nil
	}
	if p.PaybackKind != nil {
		return p.PaybackKind
	}
	k := "payback"
	return &k
}

func signedOf(direction string, amount float64) float64 {
	if direction == personal_debts.DirectionPaid {
		return amount
	}
	return -amount
}

// ClaimablePerson — who to narrow the claimable list to: an event member, or the person as my book
// knows them (my contact → its linked user's member / same name; a typed name → same name).
type ClaimablePerson struct {
	MemberID   *uuid.UUID
	ContactID  *uuid.UUID
	PersonName string
}

// ListClaimable — GET /projects/{id}/claimable.
func (s *Service) ListClaimable(ctx context.Context, userID, projectID uuid.UUID, who ClaimablePerson) (*ClaimableBillList, error) {
	if _, err := s.store.GetByIDForCaller(ctx, userID, projectID); err != nil {
		return nil, err
	}
	var me uuid.UUID
	if err := s.store.db.QueryRow(ctx, `SELECT id FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, userID).Scan(&me); err != nil {
		return nil, ErrNotMember
	}
	memberID := who.MemberID
	if memberID == nil && (who.ContactID != nil || strings.TrimSpace(who.PersonName) != "") {
		var m uuid.UUID
		err := s.store.db.QueryRow(ctx, `SELECT pm.id FROM project_members pm
			LEFT JOIN users u ON u.id = pm.user_id
			LEFT JOIN contacts c ON c.id = $3 AND c.user_id = $4
			WHERE pm.project_id = $1 AND pm.id <> $5 AND (
			      (c.linked_user_id IS NOT NULL AND pm.user_id = c.linked_user_id)
			   OR LOWER(BTRIM(COALESCE(u.display_name, pm.display_name))) = LOWER(BTRIM(COALESCE(c.display_name, $2)))
			   OR LOWER(BTRIM(pm.display_name)) = LOWER(BTRIM(COALESCE(c.display_name, $2))))
			ORDER BY (c.linked_user_id IS NOT NULL AND pm.user_id = c.linked_user_id) DESC, pm.created_at LIMIT 1`,
			projectID, who.PersonName, who.ContactID, userID, me).Scan(&m)
		if errors.Is(err, pgx.ErrNoRows) {
			return &ClaimableBillList{Data: []ClaimableBill{}, Totals: []ClaimableTotal{}}, nil
		}
		if err != nil {
			return nil, fmt.Errorf("claimable person: %w", err)
		}
		memberID = &m
	}
	bills, err := claimablePairsTx(ctx, s.store.db, userID, projectID, me, memberID)
	if err != nil {
		return nil, err
	}
	ids, billIDs := []uuid.UUID{}, []uuid.UUID{}
	for _, b := range bills {
		ids = append(ids, b.payer, b.other)
		billIDs = append(billIDs, b.PtID)
	}
	payers, err := billPayersOf(ctx, s.store.db, uniqueIDs(billIDs))
	if err != nil {
		return nil, err
	}
	for _, ps := range payers {
		for _, p := range ps {
			ids = append(ids, p.member)
		}
	}
	refs, err := LoadMemberRefs(ctx, s.store.db, userID, ids)
	if err != nil {
		return nil, err
	}
	out := &ClaimableBillList{Data: []ClaimableBill{}, Totals: []ClaimableTotal{}}
	totals := map[uuid.UUID]*ClaimableTotal{}
	var order []uuid.UUID
	for _, b := range bills {
		b.Payer, b.Other = refs[b.payer], refs[b.other]
		b.Payers = []BillPayer{}
		for _, p := range payers[b.PtID] {
			b.Payers = append(b.Payers, BillPayer{Member: refs[p.member], Amount: p.amount})
		}
		out.Data = append(out.Data, b)
		if b.Claimed {
			continue
		}
		t := totals[b.other]
		if t == nil {
			t = &ClaimableTotal{Member: refs[b.other]}
			totals[b.other] = t
			order = append(order, b.other)
		}
		t.UnclaimedNet = round2(t.UnclaimedNet + signedOf(b.Direction, b.Amount))
		t.UnclaimedCount++
	}
	for _, id := range order {
		out.Totals = append(out.Totals, *totals[id])
	}
	sort.SliceStable(out.Totals, func(i, j int) bool {
		return math.Abs(out.Totals[i].UnclaimedNet) > math.Abs(out.Totals[j].UnclaimedNet)
	})
	return out, nil
}

// Claim — POST /projects/{id}/claim: one DB transaction (preview = rolled back).
func (s *Service) Claim(ctx context.Context, userID, projectID uuid.UUID, req ClaimBillsRequest) (*ClaimBillsResult, error) {
	if s.debts == nil || s.txs == nil {
		return nil, errors.New("claims need the debts and transactions modules")
	}
	if err := s.store.AssertWritable(ctx, projectID, "resolve"); err != nil {
		return nil, err
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	var (
		me   uuid.UUID
		name string
	)
	err = tx.QueryRow(ctx, `SELECT pm.id, p.name FROM project_members pm JOIN projects p ON p.id = pm.project_id
		WHERE pm.project_id = $1 AND pm.user_id = $2 AND pm.status = 'active' FOR SHARE OF p`, projectID, userID).Scan(&me, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProjectNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("claim member: %w", err)
	}
	all, err := claimablePairsTx(ctx, tx, userID, projectID, me, &req.MemberID)
	if err != nil {
		return nil, err
	}
	byID := map[uuid.UUID]ClaimableBill{}
	for _, b := range all {
		byID[b.PtID] = b
	}
	var (
		money, debtType []ClaimableBill
		missing, booked []uuid.UUID
	)
	seen := map[uuid.UUID]bool{}
	for _, id := range req.BillIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		b, ok := byID[id]
		switch {
		case !ok:
			missing = append(missing, id)
		case b.Claimed && b.ClaimedDebtID == nil:
			return nil, ErrSettleBooked
		case b.Claimed:
			booked = append(booked, id)
		case b.Type == PTTypePayback:
			debtType = append(debtType, b)
		default:
			money = append(money, b)
		}
	}
	if len(missing) > 0 {
		return nil, &ClaimError{Err: ErrNotInBill, BillIDs: missing}
	}
	if len(booked) > 0 {
		return nil, &ClaimError{Err: personal_debts.ErrBillAlreadyBooked, BillIDs: booked}
	}
	for _, b := range money[min(1, len(money)):] {
		if b.Direction != money[0].Direction {
			return nil, ErrClaimMixedDirection
		}
	}
	if req.LinkTransactionID != nil && (len(money) != 1 || len(debtType) > 0) {
		return nil, ErrClaimLinkOne
	}
	cid, person, err := personOfTx(ctx, tx, userID, req.MemberID)
	if err != nil {
		return nil, err
	}
	eff := shared.NewEffects()
	var debtIDs []uuid.UUID
	if len(money) > 0 {
		id, e, err := s.claimLumpTx(ctx, tx, userID, projectID, me, req, money, cid, person, name)
		if err != nil {
			return nil, err
		}
		debtIDs = append(debtIDs, id)
		eff.Merge(e)
	}
	for _, b := range debtType {
		kind := personal_debts.KindRepayment
		if b.PaybackKind != nil && *b.PaybackKind == "loan" {
			kind = personal_debts.KindDebt
		}
		desc := firstNonEmpty(req.Description, b.Description)
		in := personal_debts.EntryInput{Kind: kind, Direction: b.Direction, ContactID: cid, PersonName: person,
			Amount: b.Amount, Date: b.Date, Description: desc, Note: req.Note, BillID: &b.PtID}
		if kind == personal_debts.KindDebt {
			in.MemberID, in.DueDate = &req.MemberID, req.DueDate
		}
		e, ef, err := s.debts.CreateEntryTx(ctx, tx, userID, in)
		if err != nil {
			return nil, err
		}
		debtIDs = append(debtIDs, e.ID)
		eff.Merge(ef)
	}
	out := &ClaimBillsResult{Effects: eff, Debts: []any{}, Bills: []BoardRow{}}
	for _, d := range eff.Debts {
		out.Debts = append(out.Debts, d)
	}
	if req.Preview {
		return out, nil // rolled back
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	if out.Debts, _, err = s.debts.DebtEffects(ctx, userID, debtIDs, nil); err != nil {
		return nil, err
	}
	billIDs := make([]uuid.UUID, 0, len(seen))
	for id := range seen {
		billIDs = append(billIDs, id)
	}
	if out.Bills, err = s.boardRows(ctx, userID, projectID, billIDs); err != nil {
		return nil, err
	}
	for _, r := range out.Bills {
		out.Effects.Bills = append(out.Effects.Bills, r)
	}
	return out, nil
}

// claimLumpTx — the expense / income bills (one direction) as ONE debt with a part per bill, my copy
// of each bill made ("จ่ายโดย X", no wallet) or connected.
func (s *Service) claimLumpTx(ctx context.Context, tx pgx.Tx, userID, projectID, me uuid.UUID, req ClaimBillsRequest,
	bills []ClaimableBill, cid *uuid.UUID, person, eventName string) (uuid.UUID, shared.Effects, error) {
	eff := shared.NewEffects()
	copies := req.CreateCopies == nil || *req.CreateCopies
	txOf := map[uuid.UUID]*uuid.UUID{}
	total, date := 0.0, bills[0].Date
	for _, b := range bills {
		total += b.Amount
		if b.Date > date {
			date = b.Date
		}
		txOf[b.PtID] = b.MyTransactionID
		switch {
		case req.LinkTransactionID != nil:
			if b.MyTransactionID != nil {
				return uuid.Nil, eff, ErrClaimTxNotLinkable
			}
			if err := linkClaimTxTx(ctx, tx, userID, projectID, me, b, *req.LinkTransactionID); err != nil {
				return uuid.Nil, eff, err
			}
			txOf[b.PtID] = req.LinkTransactionID
		case b.MyTransactionID == nil && copies && b.Direction == personal_debts.DirectionReceived:
			// my copy = my share ("จ่ายโดย" the payers); with several payers it may differ from the pair
			var share float64
			if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(c.share), 0)::float8 FROM split_transaction_people c
				WHERE c.split_transaction_id = $1 AND c.event_member_id = $2`, b.PtID, me).Scan(&share); err != nil {
				return uuid.Nil, eff, fmt.Errorf("my share: %w", err)
			}
			if share <= 0.005 {
				break
			}
			created, err := s.txs.CreateInTx(ctx, tx, userID, transactions.CreateRequest{
				Type: transactions.TxType(b.Type), Amount: round2(share), Date: b.Date,
				Description: shared.CleanText(b.Description), SourceProjectTransactionID: &b.PtID,
			})
			if err != nil {
				return uuid.Nil, eff, err
			}
			d, ok := created.(*transactions.TransactionDetail)
			if !ok {
				return uuid.Nil, eff, ErrQuickTxNotBillable
			}
			txOf[b.PtID] = &d.ID
			eff.Transactions = append(eff.Transactions, *d)
		}
	}
	desc := req.Description
	if shared.CleanText(desc) == nil {
		if len(bills) == 1 && shared.CleanText(bills[0].Description) != nil {
			desc = bills[0].Description
		} else {
			desc = &eventName
		}
	}
	e, ef, err := s.debts.CreateEntryTx(ctx, tx, userID, personal_debts.EntryInput{
		Kind: personal_debts.KindDebt, Direction: bills[0].Direction, ContactID: cid, PersonName: person,
		Amount: round2(total), Date: date, Description: desc, Note: req.Note, DueDate: req.DueDate, EventID: &projectID,
	})
	if err != nil {
		return uuid.Nil, eff, err
	}
	for _, b := range bills {
		if err := s.debts.LinkBillPartTx(ctx, tx, userID, e.ID, personal_debts.BillPartInput{
			PtID: b.PtID, MemberID: b.other, TransactionID: txOf[b.PtID], Amount: b.Amount,
		}); err != nil {
			return uuid.Nil, eff, err
		}
	}
	if len(bills) == 1 { // a per-bill debt keeps the bill source during the move (old readers)
		if _, err := tx.Exec(ctx, `UPDATE personal_debts SET source_project_transaction_id = $2 WHERE id = $1`,
			e.ID, bills[0].PtID); err != nil {
			return uuid.Nil, eff, fmt.Errorf("bill source: %w", err)
		}
	}
	eff.Merge(ef)
	return e.ID, eff, nil
}

// linkClaimTxTx — one of my plain transactions of the bill's type becomes my copy (my share) or, when I
// paid, the bill's origin.
func linkClaimTxTx(ctx context.Context, tx pgx.Tx, userID, projectID, me uuid.UUID, b ClaimableBill, txID uuid.UUID) error {
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM transactions WHERE id = $1 AND user_id = $2 AND type = $3
		AND project_id IS NULL AND source_project_transaction_id IS NULL AND source_personal_debt_id IS NULL
		AND transfer_group_id IS NULL AND NOT EXISTS (SELECT 1 FROM project_transactions x WHERE x.linked_transaction_id = $1))`,
		txID, userID, b.Type).Scan(&ok); err != nil {
		return fmt.Errorf("claim tx: %w", err)
	}
	if !ok {
		return ErrClaimTxNotLinkable
	}
	if b.payer == me {
		if _, err := tx.Exec(ctx, `UPDATE project_transactions SET linked_transaction_id = $2, unlinked_transaction_id = NULL
			WHERE id = $1`, b.PtID, txID); err != nil {
			return fmt.Errorf("claim origin: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE transactions SET project_id = $2, source_project_transaction_id = $3,
		unlinked_project_transaction_id = NULL WHERE id = $1`, txID, projectID, b.PtID); err != nil {
		return fmt.Errorf("claim tx link: %w", err)
	}
	return nil
}

func firstNonEmpty(a, b *string) *string {
	if c := shared.CleanText(a); c != nil {
		return c
	}
	return shared.CleanText(b)
}
