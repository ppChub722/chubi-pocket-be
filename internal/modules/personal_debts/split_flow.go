package personal_debts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
)

// Linked splits between two users (contract §5). Principle: each user's
// book is their own — the other side gets a notification with a one-tap
// action, and their own switches decide what happens:
//
//   - split_created → "add to my debts" (creates my mirror entry)
//   - split_changed → "อัปเดตตาม" (split_edit.go)
//
// Muted = nothing at all; auto = the action runs on arrival and the
// notification lands already actioned. The pair of entries points at each
// other through counterpart_debt_id. Nothing is sent on pay / receive (owner
// rule: notify on split creation and split edits only).

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

// createMirrorTx inserts the partner's side of the splitter's entry (the
// opposite direction: the splitter paid for me = I received) and links the
// pair both ways.
func (s *Service) createMirrorTx(ctx context.Context, tx pgx.Tx, splitter *Entry, partnerUserID uuid.UUID) (*Entry, error) {
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
	kind := SourcePartner
	mirror, err := insertEntryTx(ctx, tx, &Entry{
		UserID: partnerUserID, Direction: opposite(splitter.Direction),
		ContactID: partnerContactID, PersonName: personName,
		SourceProjectTransactionID: splitter.SourceProjectTransactionID, ProjectID: splitter.ProjectID,
		Amount: splitter.Amount, Currency: splitter.Currency, Date: splitter.Date,
		SourceKind: &kind, Description: splitter.Description,
	})
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
func (s *Service) splitToPartnerTx(ctx context.Context, tx pgx.Tx, splitter *Entry, partnerUserID, parentTxID uuid.UUID) error {
	if s.notifs == nil { // not wired (tests) — mirror directly
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
// Idempotent: an already-mirrored split returns the existing entry.
func (s *Service) AcceptSplitRequest(ctx context.Context, userID, notificationID uuid.UUID) (*WriteResult, error) {
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
	splitter, err := getAnyEntryTx(ctx, tx, p.SplitID)
	if errors.Is(err, ErrEntryNotFound) {
		return nil, ErrSplitChangeStale // removed since
	}
	if err != nil {
		return nil, err
	}
	// The split must really be addressed to the caller.
	if partner := linkedPartnerTx(ctx, tx, splitter); partner == nil || *partner != userID {
		return nil, ErrNotSplitRequest
	}
	var out *Entry
	if splitter.CounterpartDebtID != nil {
		out, err = getAnyEntryTx(ctx, tx, *splitter.CounterpartDebtID)
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
	return s.writeResult(ctx, userID, out.ID, nil, nil)
}
