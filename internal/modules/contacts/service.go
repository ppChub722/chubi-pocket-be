package contacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// SplitsAbsorber rewrites person_name + contact_id on caller-owned splits
// matching `names` (case-insensitive) where contact_id IS NULL. Returns
// the count rewritten. Implemented by splits.Service.AbsorbForContact.
type SplitsAbsorber func(ctx context.Context, userID, contactID uuid.UUID, names []string) (int, error)

// SplitsRestorer is called inside the contact-delete tx. Snapshots the
// contact's current display name into person_name on every referencing
// split before contact_id is nullified by the cascade. Implemented by
// splits.Service.RestoreOnContactDeleteTx.
type SplitsRestorer func(ctx context.Context, tx pgx.Tx, userID, contactID uuid.UUID) (int, error)

// UnlinkedNamesProvider returns the [(name, count)] list backing the
// "unlinked names" wizard. Implemented by splits.Service.UnlinkedNames.
type UnlinkedNamesProvider func(ctx context.Context, userID uuid.UUID) ([]UnlinkedName, error)

type UnlinkedName struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Service struct {
	store     *Store
	notifs    *notifications.Service
	absorber  SplitsAbsorber
	restorer  SplitsRestorer
	unlinked  UnlinkedNamesProvider
}

func NewService(s *Store) *Service {
	return &Service{store: s}
}

// WithNotificationService wires the notifications dep post-construction. Until
// it's set, the link-request endpoints return a 500 with a clear "module not
// wired" message — preferable to nil-deref.
func (s *Service) WithNotificationService(n *notifications.Service) { s.notifs = n }
func (s *Service) WithSplitsAbsorber(fn SplitsAbsorber)             { s.absorber = fn }
func (s *Service) WithSplitsRestorer(fn SplitsRestorer)             { s.restorer = fn }
func (s *Service) WithUnlinkedNamesProvider(fn UnlinkedNamesProvider) { s.unlinked = fn }

// Create inserts a new contact. When absorb_names is set and the splits
// hook is wired, also runs the rewrite for those names in the same flow
// (best-effort — failure on absorb does not roll back the contact insert
// in 1b.1; tighten to one tx in 1b.2 once the splits package is part of
// the same workflow).
func (s *Service) Create(ctx context.Context, userID uuid.UUID, req CreateContactRequest) (CreateContactResponse, error) {
	c, err := s.store.Create(ctx, userID, req)
	if err != nil {
		return CreateContactResponse{}, err
	}
	absorbed := 0
	if len(req.AbsorbNames) > 0 && s.absorber != nil {
		n, err := s.absorber(ctx, userID, c.ID, req.AbsorbNames)
		if err != nil {
			return CreateContactResponse{}, fmt.Errorf("absorb: %w", err)
		}
		absorbed = n
	}
	return CreateContactResponse{Contact: *c, AbsorbedCount: absorbed}, nil
}

// Absorb wraps the splits absorber for the standalone POST /:id/absorb
// endpoint. Returns 501-style error if the hook isn't wired.
func (s *Service) Absorb(ctx context.Context, userID, id uuid.UUID, names []string) (int, error) {
	// Confirm the contact exists + is owned before delegating.
	if _, err := s.store.GetByID(ctx, userID, id); err != nil {
		return 0, err
	}
	if s.absorber == nil {
		return 0, fmt.Errorf("absorb not wired: splits module missing")
	}
	return s.absorber(ctx, userID, id, names)
}

// UnlinkedNames returns the (name, count) list. Returns empty when the
// hook isn't wired (no splits → nothing to absorb).
func (s *Service) UnlinkedNames(ctx context.Context, userID uuid.UUID) ([]UnlinkedName, error) {
	if s.unlinked == nil {
		return []UnlinkedName{}, nil
	}
	return s.unlinked(ctx, userID)
}

func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (*Contact, error) {
	return s.store.GetByID(ctx, userID, id)
}

func (s *Service) List(ctx context.Context, userID uuid.UUID, f ListFilter) ([]Contact, error) {
	return s.store.List(ctx, userID, f)
}

func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, req UpdateContactRequest) (*Contact, error) {
	return s.store.Update(ctx, userID, id, req)
}

func (s *Service) Archive(ctx context.Context, userID, id uuid.UUID) (*Contact, error) {
	return s.store.SetStatus(ctx, userID, id, StatusArchived)
}

func (s *Service) Restore(ctx context.Context, userID, id uuid.UUID) (*Contact, error) {
	return s.store.SetStatus(ctx, userID, id, StatusActive)
}

// Delete hard-deletes the contact. When the splits restorer is wired,
// snapshots the contact's display name onto every referencing split's
// person_name BEFORE the contact row is dropped (cascade nullifies
// contact_id on those rows automatically). Wrapped in a tx so a failure
// in the restore step rolls back the delete.
func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) (int, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	splitsRestored := 0
	if s.restorer != nil {
		n, err := s.restorer(ctx, tx, userID, id)
		if err != nil {
			return 0, err
		}
		splitsRestored = n
	}

	tag, err := tx.Exec(ctx,
		`DELETE FROM contacts WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return 0, fmt.Errorf("delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return 0, ErrContactNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return splitsRestored, nil
}

// --- Link-request flow (1b.2) ---

// RequestLinkResponse is intentionally identical for hit and miss to
// preserve privacy. A "miss" still returns 200 with sent=true so the
// client can't probe for known users.
type RequestLinkResponse struct {
	Message string `json:"message"`
}

// RequestLink looks up a user with a matching email and fires a
// `contact_link_request` notification iff there's a hit. In both branches
// the lookup runs and the response is identical (privacy preservation per
// spec §07/§4.3).
func (s *Service) RequestLink(ctx context.Context, userID, contactID uuid.UUID) (RequestLinkResponse, error) {
	if s.notifs == nil {
		return RequestLinkResponse{}, errors.New("notifications module not wired")
	}

	contact, err := s.store.GetByID(ctx, userID, contactID)
	if err != nil {
		return RequestLinkResponse{}, err
	}
	if contact.LinkedUserID != nil {
		return RequestLinkResponse{}, ErrAlreadyLinked
	}

	// Always-execute lookup. Constant-time intent: same code path runs on
	// hit and miss, so the response timing doesn't leak whether the email
	// matches a registered user.
	var matchedUser *uuid.UUID
	if contact.Email != nil && *contact.Email != "" {
		matchedUser, err = s.notifs.LookupUserByEmail(ctx, *contact.Email)
		if err != nil {
			return RequestLinkResponse{}, fmt.Errorf("lookup: %w", err)
		}
	}

	// Fire the notification iff hit AND not self.
	if matchedUser != nil && *matchedUser != userID {
		// Idempotency: if the recipient already has an unresolved
		// `contact_link_request` from this sender for this same contact,
		// silently skip the dispatch. Once they Accept / Reject /
		// Dismiss the previous one (or hard-delete it), a new request
		// will create a fresh row again. The response stays "sent" so
		// the timing channel doesn't leak the dedup decision either.
		pending, err := s.notifs.HasPendingContactLinkRequest(
			ctx, *matchedUser, userID, contactID,
		)
		if err != nil {
			return RequestLinkResponse{}, err
		}
		if pending {
			return RequestLinkResponse{Message: "Link request sent"}, nil
		}

		// Look up sender's display name for the payload.
		var senderName string
		_ = s.store.Pool().QueryRow(ctx,
			`SELECT display_name FROM users WHERE id = $1`, userID).Scan(&senderName)

		tx, err := s.store.Pool().Begin(ctx)
		if err != nil {
			return RequestLinkResponse{}, fmt.Errorf("begin: %w", err)
		}
		defer tx.Rollback(ctx)

		if err := s.notifs.DispatchContactLinkRequest(
			ctx, tx, *matchedUser, userID,
			notifications.ContactLinkRequestPayload{
				ContactID:         contactID,
				SenderUserID:      userID,
				SenderDisplayName: senderName,
			},
		); err != nil {
			return RequestLinkResponse{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return RequestLinkResponse{}, fmt.Errorf("commit: %w", err)
		}
	}

	// Privacy: same response shape on hit and miss.
	return RequestLinkResponse{Message: "Link request sent"}, nil
}

// AcceptLinkRequest is called by the recipient. Reads the
// `contact_link_request` notification (must be addressed to caller),
// validates uniqueness in both directions, sets the contact's
// linked_user_id = caller, and marks the notification actioned. All in
// one tx.
func (s *Service) AcceptLinkRequest(ctx context.Context, userID, notificationID uuid.UUID) (*Contact, error) {
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
	if n.Type != notifications.TypeContactLinkRequest {
		return nil, fmt.Errorf("notification is not a contact_link_request")
	}
	if n.ActionedAt != nil || n.DismissedAt != nil {
		// Idempotent re-accept: re-emit success on already-actioned. Reject
		// if the notification was previously dismissed.
		if n.DismissedAt != nil {
			return nil, errors.New("notification was previously dismissed")
		}
	}

	var p notifications.ContactLinkRequestPayload
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}

	// Verify sender still owns the contact (might have been deleted).
	contact, err := s.store.GetByIDTx(ctx, tx, p.SenderUserID, p.ContactID)
	if err != nil {
		return nil, err
	}
	if contact.LinkedUserID != nil {
		return nil, ErrAlreadyLinked
	}
	if userID == p.SenderUserID {
		return nil, ErrCannotLinkSelf
	}

	// Uniqueness in both directions: caller might already have linked back,
	// or sender might have already linked another contact to caller.
	exists, err := s.store.LinkExistsBetween(ctx, tx, userID, p.SenderUserID)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrAlreadyLinked
	}

	updated, err := s.store.SetLinkedUserIDTx(ctx, tx, p.SenderUserID, p.ContactID, userID)
	if err != nil {
		return nil, err
	}

	// Spec change: the FE owns reciprocal contact creation now via
	// `AcceptWithContact` — the recipient picks (existing contact vs
	// create-with-prefill). The old auto-create-on-server path was
	// dropped along with `autoLinkReciprocalTx`. This endpoint is kept
	// for legacy callers that just want to link the sender's side
	// without touching their own contact book.

	if _, err := s.notifs.MarkActionedTx(ctx, tx, userID, notificationID); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return updated, nil
}

// RejectLinkRequest marks the notification dismissed — the inbox hides
// dismissed rows entirely (different from Accept, which leaves the row
// visible-but-actioned for the post-accept tap flow). The sender is
// not notified; rejection is silent on the sender's side.
func (s *Service) RejectLinkRequest(ctx context.Context, userID, notificationID uuid.UUID) error {
	if s.notifs == nil {
		return errors.New("notifications module not wired")
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	n, err := s.notifs.GetByIDForCallerTx(ctx, tx, userID, notificationID)
	if err != nil {
		return err
	}
	if n.Type != notifications.TypeContactLinkRequest {
		return fmt.Errorf("notification is not a contact_link_request")
	}
	if _, err := s.notifs.MarkDismissedTx(ctx, tx, userID, notificationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Unlink clears linked_user_id. Splits keep their contact_id; the linked
// user simply loses visibility into the splits.
func (s *Service) Unlink(ctx context.Context, userID, contactID uuid.UUID) (*Contact, error) {
	return s.store.ClearLinkedUserID(ctx, userID, contactID)
}

// GetSenderProfile returns the public-profile fields of the actor on a
// `contact_link_request` notification owned by the caller. Used by the
// post-accept tap flow to render locked display_name / email fields on
// the link-mode contact form.
//
// Gated to `caller is the recipient` AND `notification not dismissed`.
// Allowed even when the request is already actioned (post-accept tap
// still needs the data). Returns an error when dismissed (rejected
// requests don't expose the sender).
func (s *Service) GetSenderProfile(
	ctx context.Context, userID, notificationID uuid.UUID,
) (*SenderProfile, error) {
	if s.notifs == nil {
		return nil, errors.New("notifications module not wired")
	}
	n, err := s.notifs.Get(ctx, userID, notificationID)
	if err != nil {
		return nil, err
	}
	if n.Type != notifications.TypeContactLinkRequest {
		return nil, errors.New("notification is not a contact_link_request")
	}
	if n.DismissedAt != nil {
		return nil, errors.New("notification was rejected")
	}
	if n.ActorUserID == nil {
		return nil, errors.New("notification has no actor")
	}

	var p SenderProfile
	var iconBytes []byte
	err = s.store.Pool().QueryRow(ctx,
		`SELECT id, display_name, email, icon_code FROM users WHERE id = $1`,
		*n.ActorUserID,
	).Scan(&p.ID, &p.DisplayName, &p.Email, &iconBytes)
	if err != nil {
		return nil, fmt.Errorf("sender lookup: %w", err)
	}
	if iconBytes != nil {
		p.IconCode = new(shared.IconCode)
		if err := json.Unmarshal(iconBytes, p.IconCode); err != nil {
			return nil, fmt.Errorf("unmarshal icon_code: %w", err)
		}
	}
	return &p, nil
}

// CreateLinkedContactFromLinkRequest is the post-accept "I have no
// existing contact for them, create a fresh one" path. The notification
// must already be actioned (caller previously hit Accept). BE pulls
// display_name + email from the sender's user record so they're frozen
// at create time as the contact's snapshot fallback; phone / notes /
// icon come from the form.
func (s *Service) CreateLinkedContactFromLinkRequest(
	ctx context.Context, userID, notificationID uuid.UUID, req CreateLinkedContactRequest,
) (*Contact, error) {
	if s.notifs == nil {
		return nil, errors.New("notifications module not wired")
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	senderUserID, err := s.verifyAcceptedLinkRequestTx(ctx, tx, userID, notificationID)
	if err != nil {
		return nil, err
	}

	// Pull sender's name + email; both are required snapshot values
	// for the contact row even though the FE displays them live.
	var senderName string
	var senderEmail *string
	if err := tx.QueryRow(ctx,
		`SELECT display_name, email FROM users WHERE id = $1`, senderUserID,
	).Scan(&senderName, &senderEmail); err != nil {
		return nil, fmt.Errorf("sender lookup: %w", err)
	}

	created, err := s.store.CreateLinkedTx(
		ctx, tx, userID, senderUserID,
		senderName, senderEmail, req.Phone, req.Notes, req.IconCode,
	)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return created, nil
}

// LinkExistingContactFromLinkRequest is the post-accept "I already have
// a contact for them, just wire the link" path. Notification must be
// actioned. The contact must be caller-owned and unlinked (or already
// linked to the same sender — idempotent).
//
// `display_name` and `email` are NOT touched on the contact row — the
// FE pulls them from the linked user once linked. Phone / notes / icon
// are optional B-side updates.
func (s *Service) LinkExistingContactFromLinkRequest(
	ctx context.Context, userID, notificationID, contactID uuid.UUID,
	req LinkExistingContactRequest,
) (*Contact, error) {
	if s.notifs == nil {
		return nil, errors.New("notifications module not wired")
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	senderUserID, err := s.verifyAcceptedLinkRequestTx(ctx, tx, userID, notificationID)
	if err != nil {
		return nil, err
	}

	existing, err := s.store.GetByIDTx(ctx, tx, userID, contactID)
	if err != nil {
		return nil, err
	}
	if existing.LinkedUserID != nil && *existing.LinkedUserID != senderUserID {
		return nil, ErrAlreadyLinked
	}

	// Apply optional B-side field updates first (so they're persisted
	// regardless of whether the link write below is a no-op). Only
	// phone / notes / icon — display_name / email are linked-driven.
	if req.Phone != nil || req.Notes != nil || req.IconCode != nil {
		setClauses := []string{"updated_by_user_id = $1"}
		args := []any{userID}
		if req.Phone != nil {
			args = append(args, *req.Phone)
			setClauses = append(setClauses,
				fmt.Sprintf("phone = $%d", len(args)))
		}
		if req.Notes != nil {
			args = append(args, *req.Notes)
			setClauses = append(setClauses,
				fmt.Sprintf("notes = $%d", len(args)))
		}
		if req.IconCode != nil {
			iconJSON, err := json.Marshal(req.IconCode)
			if err != nil {
				return nil, fmt.Errorf("marshal icon_code: %w", err)
			}
			args = append(args, iconJSON)
			setClauses = append(setClauses,
				fmt.Sprintf("icon_code = $%d::jsonb", len(args)))
		}
		args = append(args, contactID)
		q := "UPDATE contacts SET " + strings.Join(setClauses, ", ") +
			fmt.Sprintf(" WHERE id = $%d AND user_id = $1", len(args))
		if _, err := tx.Exec(ctx, q, args...); err != nil {
			return nil, fmt.Errorf("update b-side fields: %w", err)
		}
	}

	updated, err := s.store.SetLinkedUserIDTx(ctx, tx, userID, contactID, senderUserID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return updated, nil
}

// verifyAcceptedLinkRequestTx is the shared gate for both post-accept
// linking paths. The notification must:
//   - exist for the caller,
//   - be a contact_link_request,
//   - be actioned (caller previously hit Accept),
//   - not be dismissed,
//   - have an actor.
//
// Returns the sender's user_id on success.
func (s *Service) verifyAcceptedLinkRequestTx(
	ctx context.Context, tx pgx.Tx, userID, notificationID uuid.UUID,
) (uuid.UUID, error) {
	n, err := s.notifs.GetByIDForCallerTx(ctx, tx, userID, notificationID)
	if err != nil {
		return uuid.Nil, err
	}
	if n.Type != notifications.TypeContactLinkRequest {
		return uuid.Nil, errors.New("notification is not a contact_link_request")
	}
	if n.ActionedAt == nil {
		return uuid.Nil, errors.New("notification has not been accepted")
	}
	if n.DismissedAt != nil {
		return uuid.Nil, errors.New("notification was rejected")
	}
	if n.ActorUserID == nil {
		return uuid.Nil, errors.New("notification has no actor")
	}
	return *n.ActorUserID, nil
}
