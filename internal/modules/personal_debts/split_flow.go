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

// Linked splits between two users (contract §5). A split with a linked
// contact creates the splitter's debt plus — unless the partner opted out —
// a mirror debt in the partner's book; the two rows point at each other
// through counterpart_debt_id. Payments on one side notify the other and,
// when the receiver allows it, settle their side automatically.
//
// Each user's flags (user_notification_settings) drive only their own side:
//   - splitter: auto_notify_linked_split_contacts → send split_created
//   - partner:  auto_add_to_personal_debt_on_split_notification → create
//     the mirror now; off = the split_created notification carries an
//     "add to my debts" action instead (AcceptSplitRequest)
//   - receiver: auto_record_received_payment (+ default_account_id) →
//     settle my side when the other side pays

var (
	ErrNotSplitRequest = errors.New("notification is not a split you can add")
)

// WithNotifications wires the notifications module (cross-module, set in
// main). Without it splits behave as before: mirror always, no notices.
func (s *Service) WithNotifications(n *notifications.Service) { s.notifs = n }

// settingsFor — the user's automation flags, defaults when unavailable.
func (s *Service) settingsFor(ctx context.Context, userID uuid.UUID) notifications.Settings {
	def := notifications.Settings{
		UserID:                                   userID,
		AutoNotifyLinkedSplitContacts:            true,
		AutoAddToPersonalDebtOnSplitNotification: true,
	}
	if s.notifs == nil {
		return def
	}
	st, err := s.notifs.GetSettings(ctx, userID)
	if err != nil || st == nil {
		return def
	}
	return *st
}

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
		splitter.Amount, splitter.Currency, nil,
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

// splitToPartnerTx — the partner side of one linked split, honouring both
// users' flags.
func (s *Service) splitToPartnerTx(
	ctx context.Context, tx pgx.Tx, splitter *PersonalDebt, partnerUserID, parentTxID uuid.UUID,
) error {
	var mirrorID *uuid.UUID
	if s.settingsFor(ctx, partnerUserID).AutoAddToPersonalDebtOnSplitNotification {
		mirror, err := s.createMirrorTx(ctx, tx, splitter, partnerUserID)
		if err != nil {
			return err
		}
		mirrorID = &mirror.ID
	}
	if s.notifs == nil || !s.settingsFor(ctx, splitter.UserID).AutoNotifyLinkedSplitContacts {
		return nil
	}
	return s.notifs.DispatchSplitCreated(ctx, tx, partnerUserID, splitter.UserID, notifications.SplitCreatedPayload{
		SplitID:             splitter.ID,
		ParentKind:          "transaction",
		ParentID:            parentTxID,
		ProjectID:           splitter.ProjectID,
		SplitterDisplayName: displayNameTx(ctx, tx, splitter.UserID),
		Amount:              splitter.Amount,
		Currency:            splitter.Currency,
		Note:                splitter.Note,
		RecipientDebtID:     mirrorID,
	})
}

// AcceptSplitRequest — "add to my debts" on a split_created notification
// (the recipient had auto-add off). Idempotent: an already-mirrored split
// returns the existing row.
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
	splitter, err := getAnyDebtTx(ctx, tx, p.SplitID)
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

// afterSettleTx runs inside Settle, after `debt` (as it was before the
// settle) got `amount` paid on `date`; txID is the wallet transaction, nil
// for a direct settle. Linked pairs only.
//
//   - I paid what I owe (i_owe) → split_paid to the creditor; if they allow
//     it, their side settles too (into default_account_id as income when
//     set and usable, else as a direct settle) → split_received back to me.
//   - I recorded money I was owed (owed_to_me) → split_received to the
//     debtor. Their side stays open: only they know which wallet paid.
func (s *Service) afterSettleTx(
	ctx context.Context, tx pgx.Tx, debt *PersonalDebt, amount float64, date string, txID *uuid.UUID,
) error {
	if s.notifs == nil || debt.CounterpartDebtID == nil {
		return nil
	}
	cp, err := getAnyDebtTx(ctx, tx, *debt.CounterpartDebtID)
	if errors.Is(err, ErrDebtNotFound) {
		return nil // mirror deleted — nothing to keep in sync
	}
	if err != nil {
		return err
	}
	me := debt.UserID

	if debt.Direction == DirectionOwedToMe {
		return s.notifs.DispatchSplitReceived(ctx, tx, cp.UserID, me, notifications.SplitReceivedPayload{
			SplitID: cp.ID, ReceiverUserID: me, ReceiverDisplayName: displayNameTx(ctx, tx, me),
			Amount: amount, Currency: debt.Currency, ReceiverTransactionID: txID,
			ProjectID: debt.ProjectID, RecipientDebtID: &cp.ID,
		})
	}

	if err := s.notifs.DispatchSplitPaid(ctx, tx, cp.UserID, me, notifications.SplitPaidPayload{
		SplitID: cp.ID, PayerUserID: me, PayerDisplayName: displayNameTx(ctx, tx, me),
		Amount: amount, Currency: debt.Currency, PayerTransactionID: txID,
		ProjectID: debt.ProjectID, RecipientDebtID: &cp.ID,
	}); err != nil {
		return err
	}

	st := s.settingsFor(ctx, cp.UserID)
	if !st.AutoRecordReceivedPayment || cp.Status != StatusOpen {
		return nil
	}
	recv := math.Min(amount, cp.Amount-cp.SettledAmount)
	if recv <= 0 {
		return nil
	}
	var recvTxID *uuid.UUID
	if acc := st.DefaultAccountID; acc != nil && s.activeWalletMemberTx(ctx, tx, cp.UserID, *acc) {
		accID := *acc
		created, err := s.txs.CreateInTxWithSourceDebt(ctx, tx, cp.UserID, cp.ID, transactions.CreateRequest{
			Type: transactions.TypeIncome, AccountID: &accID, Amount: recv, Date: date,
		})
		if err != nil {
			return fmt.Errorf("auto-record payment: %w", err)
		}
		recvTxID = &created.ID
	} else if err := s.store.AutoBumpInTx(ctx, tx, cp.UserID, cp.ID, recv); err != nil {
		return err
	}
	return s.notifs.DispatchSplitReceived(ctx, tx, me, cp.UserID, notifications.SplitReceivedPayload{
		SplitID: debt.ID, ReceiverUserID: cp.UserID, ReceiverDisplayName: displayNameTx(ctx, tx, cp.UserID),
		Amount: recv, Currency: debt.Currency, ReceiverTransactionID: recvTxID,
		ProjectID: debt.ProjectID, RecipientDebtID: &debt.ID,
	})
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
