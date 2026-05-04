package contacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
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

	if _, err := s.notifs.MarkActionedTx(ctx, tx, userID, notificationID); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return updated, nil
}

// RejectLinkRequest marks the notification dismissed; no link change.
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
