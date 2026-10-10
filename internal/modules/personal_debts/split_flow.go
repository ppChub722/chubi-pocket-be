package personal_debts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

// Linked splits between two users (contract §5). Principle: each user's
// book is their own — the other side gets a notification with a one-tap
// action, and their own switches decide what happens:
//
//   - split_created → "add to my debts" (creates my mirror debt)
//   - split_paid    → "record the receipt" (income into my debt)
//
// Muted = nothing at all; auto = the action runs on arrival and the
// notification lands already actioned. The pair of debts points at each
// other through counterpart_debt_id so a payment can find the other side.
// Nothing is ever pushed back the other way (no "received" notice, no
// closing the payer's side).

var (
	ErrNotSplitRequest = errors.New("notification is not a split you can add")
)

// WithNotifications wires the notifications module (cross-module, set in
// main). Without it splits behave as before: mirror always, no notices.
func (s *Service) WithNotifications(n *notifications.Service) { s.notifs = n }

func displayNameTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) string {
	var name string
	_ = tx.QueryRow(ctx, `SELECT display_name FROM users WHERE id = $1`, userID).Scan(&name)
	if name == "" {
		name = "Unknown"
	}
	return name
}

func getAnyDebtTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*PersonalDebt, error) {
	d, err := scanDebt(tx.QueryRow(ctx,
		`SELECT `+debtColumns+` FROM personal_debts WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDebtNotFound
	}
	return d, err
}

// createMirrorTx inserts the partner's side of splitter's debt and links
// the pair both ways.
func (s *Service) createMirrorTx(
	ctx context.Context, tx pgx.Tx, splitter *PersonalDebt, partnerUserID uuid.UUID,
) (*PersonalDebt, error) {
	partnerDir := DirectionIOwe
	if splitter.Direction == DirectionIOwe {
		partnerDir = DirectionOwedToMe
	}
	// The partner's own contact for the splitter, else the splitter's name.
	var partnerContactID *uuid.UUID
	var personName string
	var pcID uuid.UUID
	if err := tx.QueryRow(ctx,
		`SELECT id, display_name FROM contacts WHERE user_id = $1 AND linked_user_id = $2 LIMIT 1`,
		partnerUserID, splitter.UserID).Scan(&pcID, &personName); err == nil {
		partnerContactID = &pcID
	} else {
		personName = displayNameTx(ctx, tx, splitter.UserID)
	}
	mirror, err := s.store.CreateAttachedTx(ctx, tx, partnerUserID, partnerDir,
		partnerContactID, personName,
		nil, // the partner has no transaction in their book
		splitter.SourceProjectTransactionID, splitter.ProjectID,
		splitter.Amount, splitter.Currency, splitter.Description, nil,
	)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE personal_debts SET counterpart_debt_id = CASE id WHEN $1 THEN $2::uuid ELSE $1::uuid END
		WHERE id IN ($1, $2)`, splitter.ID, mirror.ID); err != nil {
		return nil, fmt.Errorf("link mirror: %w", err)
	}
	mirror.CounterpartDebtID = &splitter.ID
	return mirror, nil
}

// splitToPartnerTx — the partner side of one linked split.
func (s *Service) splitToPartnerTx(
	ctx context.Context, tx pgx.Tx, splitter *PersonalDebt, partnerUserID, parentTxID uuid.UUID,
) error {
	if s.notifs == nil { // not wired (tests) — the pre-§5 behaviour
		_, err := s.createMirrorTx(ctx, tx, splitter, partnerUserID)
		return err
	}
	d, err := s.notifs.DeliveryFor(ctx, tx, partnerUserID, &splitter.UserID, notifications.TypeSplitCreated)
	if err != nil || !d.Deliver {
		return err
	}
	var mirrorID *uuid.UUID
	if d.Auto {
		mirror, err := s.createMirrorTx(ctx, tx, splitter, partnerUserID)
		if err != nil {
			return err
		}
		mirrorID = &mirror.ID
	}
	return s.notifs.DispatchSplitCreated(ctx, tx, partnerUserID, splitter.UserID, notifications.SplitCreatedPayload{
		SplitID:             splitter.ID,
		ParentKind:          "transaction",
		ParentID:            parentTxID,
		ProjectID:           splitter.ProjectID,
		SplitterDisplayName: displayNameTx(ctx, tx, splitter.UserID),
		Amount:              splitter.Amount,
		Currency:            splitter.Currency,
		Description:         splitter.Description,
		Note:                splitter.Note,
		RecipientDebtID:     mirrorID,
	}, d.Auto)
}

// AcceptSplitRequest — "add to my debts" on a split_created notification.
// Idempotent: an already-mirrored split returns the existing row.
func (s *Service) AcceptSplitRequest(ctx context.Context, userID, notificationID uuid.UUID) (*PersonalDebt, error) {
	if s.notifs == nil {
		return nil, errors.New("notifications module not wired")
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	n, err := s.notifs.GetByIDForCallerTx(ctx, tx, userID, notificationID)
	if err != nil {
		return nil, err
	}
	if n.Type != notifications.TypeSplitCreated {
		return nil, ErrNotSplitRequest
	}
	var p notifications.SplitCreatedPayload
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}
	if p.Superseded {
		return nil, ErrSplitChangeStale
	}
	splitter, err := getAnyDebtTx(ctx, tx, p.SplitID)
	if errors.Is(err, ErrDebtNotFound) {
		return nil, ErrSplitChangeStale // removed since
	}
	if err != nil {
		return nil, err
	}
	// The split must really be addressed to the caller.
	var linked *uuid.UUID
	if splitter.CounterpartyContactID != nil {
		_ = tx.QueryRow(ctx,
			`SELECT linked_user_id FROM contacts WHERE id = $1 AND user_id = $2`,
			*splitter.CounterpartyContactID, splitter.UserID).Scan(&linked)
	}
	if linked == nil || *linked != userID {
		return nil, ErrNotSplitRequest
	}

	var out *PersonalDebt
	if splitter.CounterpartDebtID != nil {
		out, err = getAnyDebtTx(ctx, tx, *splitter.CounterpartDebtID)
	} else {
		out, err = s.createMirrorTx(ctx, tx, splitter, userID)
	}
	if err != nil {
		return nil, err
	}
	if _, err := s.notifs.MarkActionedTx(ctx, tx, userID, notificationID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return out, nil
}

// afterSettleTx runs inside Settle once `debt` (as it was before) got
// `amount` paid on `date`. Only the payer's side notifies: when I pay back
// what I owe on a linked split, the creditor gets split_paid — unless their
// side is already closed (they recorded it themselves; nothing to tell).
// Their auto switch records the receipt into default_account_id, or as a
// floating row when none / unusable.
func (s *Service) afterSettleTx(
	ctx context.Context, tx pgx.Tx, debt *PersonalDebt, amount float64, date string,
) error {
	if s.notifs == nil || debt.CounterpartDebtID == nil || debt.Direction != DirectionIOwe {
		return nil
	}
	cp, err := getAnyDebtTx(ctx, tx, *debt.CounterpartDebtID)
	if errors.Is(err, ErrDebtNotFound) {
		return nil // their row was deleted — their book, their call
	}
	if err != nil || cp.Status != StatusOpen {
		return err
	}
	me := debt.UserID
	d, err := s.notifs.DeliveryFor(ctx, tx, cp.UserID, &me, notifications.TypeSplitPaid)
	if err != nil || !d.Deliver {
		return err
	}

	var recorded *uuid.UUID
	if d.Auto {
		recv := math.Min(amount, cp.Amount-cp.SettledAmount)
		st, err := s.notifs.SettingsTx(ctx, tx, cp.UserID)
		if err != nil {
			return err
		}
		var accID *uuid.UUID
		if a := st.DefaultAccountID; a != nil && s.activeWalletMemberTx(ctx, tx, cp.UserID, *a) {
			accID = a
		}
		created, err := s.txs.CreateInTxWithSourceDebt(ctx, tx, cp.UserID, cp.ID, transactions.CreateRequest{
			Type: transactions.TypeIncome, AccountID: accID, Amount: recv, Date: date,
		})
		if err != nil {
			return fmt.Errorf("auto-record payment: %w", err)
		}
		recorded = &created.ID
	}
	return s.notifs.DispatchSplitPaid(ctx, tx, cp.UserID, me, notifications.SplitPaidPayload{
		SplitID: cp.ID, PayerUserID: me, PayerDisplayName: displayNameTx(ctx, tx, me),
		Amount: amount, Currency: debt.Currency, ProjectID: debt.ProjectID,
		RecipientDebtID: &cp.ID, RecordedTransactionID: recorded,
	}, d.Auto)
}

func (s *Service) activeWalletMemberTx(ctx context.Context, tx pgx.Tx, userID, accountID uuid.UUID) bool {
	var ok bool
	_ = tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM account_members am JOIN accounts a ON a.id = am.account_id
			WHERE am.account_id = $1 AND am.user_id = $2
			  AND am.joined_at IS NOT NULL AND am.left_at IS NULL AND a.status = 'active'
		)`, accountID, userID).Scan(&ok)
	return ok
}
