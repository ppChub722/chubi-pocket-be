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

type SplitsAbsorber func(ctx context.Context, userID, contactID uuid.UUID, names []string) (int, error)

type SplitsRestorer func(ctx context.Context, tx pgx.Tx, userID, contactID uuid.UUID) (int, error)

type UnlinkedNamesProvider func(ctx context.Context, userID uuid.UUID) ([]UnlinkedName, error)

type UnlinkedName struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Service struct {
	store    *Store
	notifs   *notifications.Service
	absorber SplitsAbsorber
	restorer SplitsRestorer
	unlinked UnlinkedNamesProvider
}

func NewService(s *Store) *Service {
	return &Service{store: s}
}

func (s *Service) WithNotificationService(n *notifications.Service)   { s.notifs = n }
func (s *Service) WithSplitsAbsorber(fn SplitsAbsorber)               { s.absorber = fn }
func (s *Service) WithSplitsRestorer(fn SplitsRestorer)               { s.restorer = fn }
func (s *Service) WithUnlinkedNamesProvider(fn UnlinkedNamesProvider) { s.unlinked = fn }

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

func (s *Service) Absorb(ctx context.Context, userID, id uuid.UUID, names []string) (*AbsorbResult, error) {
	if _, err := s.store.GetByID(ctx, userID, id); err != nil {
		return nil, err
	}
	if s.absorber == nil {
		return nil, fmt.Errorf("absorb not wired: splits module missing")
	}
	n, err := s.absorber(ctx, userID, id, names)
	if err != nil {
		return nil, err
	}
	c, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	return &AbsorbResult{Contact: *c, AbsorbedCount: n}, nil
}

func (s *Service) UnlinkedNames(ctx context.Context, userID uuid.UUID) ([]UnlinkedName, error) {
	if s.unlinked == nil {
		return []UnlinkedName{}, nil
	}
	return s.unlinked(ctx, userID)
}

func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (*Contact, error) {
	return s.store.GetByID(ctx, userID, id)
}

func (s *Service) GetDetail(ctx context.Context, userID, id uuid.UUID, withUnlinkedNames, withDeletePreview bool) (*ContactDetail, error) {
	c, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	d := &ContactDetail{Contact: *c}
	names, err := s.UnlinkedNames(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		d.UnlinkedTotal.Names++
		d.UnlinkedTotal.Records += n.Count
	}
	if withUnlinkedNames {
		if names == nil {
			names = []UnlinkedName{}
		}
		d.UnlinkedNames = names
	}
	if withDeletePreview {
		if d.DeletePreview, err = s.store.DeletePreview(ctx, userID, c); err != nil {
			return nil, err
		}
	}
	return d, nil
}

func (s *Service) linkResult(ctx context.Context, userID, notificationID uuid.UUID, c *Contact) (*LinkRequestResult, error) {
	nv, err := shared.LoadNotificationView(ctx, s.store.Pool(), userID, notificationID)
	if err != nil {
		return nil, err
	}
	if nv == nil {
		return nil, notifications.ErrNotificationNotFound
	}
	badges, err := shared.LoadBadges(ctx, s.store.Pool(), userID)
	if err != nil {
		return nil, err
	}
	sum, err := notifications.LoadInboxSummary(ctx, s.store.Pool(), userID)
	if err != nil {
		return nil, err
	}
	return &LinkRequestResult{Contact: c, Notification: nv, Badges: badges, Summary: sum}, nil
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

type RequestLinkResponse struct {
	Message string `json:"message"`
}

func (s *Service) RequestLink(ctx context.Context, userID, contactID uuid.UUID) (*Contact, error) {
	if s.notifs == nil {
		return nil, errors.New("notifications module not wired")
	}

	contact, err := s.store.GetByID(ctx, userID, contactID)
	if err != nil {
		return nil, err
	}
	if contact.LinkedUserID != nil {
		return nil, ErrAlreadyLinked
	}

	var matchedUser *uuid.UUID
	if contact.Email != nil && *contact.Email != "" {
		matchedUser, err = s.notifs.LookupUserByEmail(ctx, *contact.Email)
		if err != nil {
			return nil, fmt.Errorf("lookup: %w", err)
		}
	}

	if matchedUser != nil && *matchedUser != userID {
		pending, err := s.notifs.HasPendingContactLinkRequest(
			ctx, *matchedUser, userID, contactID,
		)
		if err != nil {
			return nil, err
		}
		if pending {
			return contact, nil
		}

		var senderName string
		_ = s.store.Pool().QueryRow(ctx,
			`SELECT display_name FROM users WHERE id = $1`, userID).Scan(&senderName)

		tx, err := s.store.Pool().Begin(ctx)
		if err != nil {
			return nil, fmt.Errorf("begin: %w", err)
		}
		defer tx.Rollback(ctx)

		// The recipient's auto-accept switch (F2): linked at once, the notice (if not muted) answered.
		d, err := s.notifs.DeliveryFor(ctx, tx, *matchedUser, &userID, notifications.TypeContactLinkRequest)
		if err != nil {
			return nil, err
		}
		accepted := false
		if d.AcceptRequest {
			exists, err := s.store.LinkExistsBetween(ctx, tx, *matchedUser, userID)
			if err != nil {
				return nil, err
			}
			if !exists {
				if contact, err = s.store.SetLinkedUserIDTx(ctx, tx, userID, contactID, *matchedUser); err != nil {
					return nil, err
				}
				accepted = true
			}
		}
		if err := s.notifs.DispatchContactLinkRequest(
			ctx, tx, *matchedUser, userID,
			notifications.ContactLinkRequestPayload{
				ContactID:         contactID,
				SenderUserID:      userID,
				SenderDisplayName: senderName,
			}, accepted,
		); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit: %w", err)
		}
	}

	return contact, nil
}

func (s *Service) AcceptLinkRequest(ctx context.Context, userID, notificationID uuid.UUID) (*LinkRequestResult, error) {
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
		return nil, ErrNotLinkRequest
	}
	if n.ActionedAt != nil || n.DismissedAt != nil {
		if n.DismissedAt != nil {
			return nil, ErrRequestRejected
		}
	}

	var p notifications.ContactLinkRequestPayload
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}

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

	exists, err := s.store.LinkExistsBetween(ctx, tx, userID, p.SenderUserID)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrAlreadyLinked
	}

	_, err = s.store.SetLinkedUserIDTx(ctx, tx, p.SenderUserID, p.ContactID, userID)
	if err != nil {
		return nil, err
	}

	if _, err := s.notifs.MarkActionedTx(ctx, tx, userID, notificationID); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.linkResult(ctx, userID, notificationID, nil)
}

func (s *Service) RejectLinkRequest(ctx context.Context, userID, notificationID uuid.UUID) (*LinkRequestResult, error) {
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
		return nil, ErrNotLinkRequest
	}
	if _, err := s.notifs.MarkDismissedTx(ctx, tx, userID, notificationID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.linkResult(ctx, userID, notificationID, nil)
}

func (s *Service) Unlink(ctx context.Context, userID, contactID uuid.UUID) (*Contact, error) {
	return s.store.ClearLinkedUserID(ctx, userID, contactID)
}

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
		return nil, ErrNotLinkRequest
	}
	if n.DismissedAt != nil {
		return nil, ErrRequestRejected
	}
	if n.ActorUserID == nil {
		return nil, errors.New("notification has no actor")
	}

	var p SenderProfile
	var avatarSrc []byte
	err = s.store.Pool().QueryRow(ctx,
		`SELECT id, display_name, email, `+shared.AvatarSQL("id", "NULL")+` FROM users WHERE id = $1`,
		*n.ActorUserID,
	).Scan(&p.ID, &p.DisplayName, &p.Email, &avatarSrc)
	if err != nil {
		return nil, fmt.Errorf("sender lookup: %w", err)
	}
	p.Avatar = shared.AvatarFromSrc(p.DisplayName, avatarSrc)
	if p.ContactID, err = s.store.ContactLinkedTo(ctx, userID, p.ID); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Service) CreateLinkedContactFromLinkRequest(
	ctx context.Context, userID, notificationID uuid.UUID, req CreateLinkedContactRequest,
) (*LinkRequestResult, error) {
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

	var senderName string
	var senderEmail *string
	if err := tx.QueryRow(ctx,
		`SELECT display_name, email FROM users WHERE id = $1`, senderUserID,
	).Scan(&senderName, &senderEmail); err != nil {
		return nil, fmt.Errorf("sender lookup: %w", err)
	}

	created, err := s.store.CreateLinkedTx(
		ctx, tx, userID, senderUserID,
		senderName, senderEmail, req.Phone, req.Description, req.Note, req.IconCode,
	)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.linkResult(ctx, userID, notificationID, created)
}

func (s *Service) LinkExistingContactFromLinkRequest(
	ctx context.Context, userID, notificationID, contactID uuid.UUID,
	req LinkExistingContactRequest,
) (*LinkRequestResult, error) {
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

	if req.Phone != nil || req.Description != nil || req.Note != nil || req.IconCode != nil {
		setClauses := []string{"updated_by_user_id = $1"}
		args := []any{userID}
		if req.Phone != nil {
			args = append(args, *req.Phone)
			setClauses = append(setClauses,
				fmt.Sprintf("phone = $%d", len(args)))
		}
		if req.Description != nil {
			args = append(args, shared.CleanText(req.Description))
			setClauses = append(setClauses,
				fmt.Sprintf("description = $%d", len(args)))
		}
		if req.Note != nil {
			args = append(args, shared.CleanText(req.Note))
			setClauses = append(setClauses,
				fmt.Sprintf("note = $%d", len(args)))
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
	return s.linkResult(ctx, userID, notificationID, updated)
}

func (s *Service) verifyAcceptedLinkRequestTx(
	ctx context.Context, tx pgx.Tx, userID, notificationID uuid.UUID,
) (uuid.UUID, error) {
	n, err := s.notifs.GetByIDForCallerTx(ctx, tx, userID, notificationID)
	if err != nil {
		return uuid.Nil, err
	}
	if n.Type != notifications.TypeContactLinkRequest {
		return uuid.Nil, ErrNotLinkRequest
	}
	if n.ActionedAt == nil {
		return uuid.Nil, ErrNotAccepted
	}
	if n.DismissedAt != nil {
		return uuid.Nil, ErrRequestRejected
	}
	if n.ActorUserID == nil {
		return uuid.Nil, errors.New("notification has no actor")
	}
	return *n.ActorUserID, nil
}
