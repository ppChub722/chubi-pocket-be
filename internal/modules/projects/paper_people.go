package projects

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

var (
	ErrBadPaperPerson = apierr.Invalid("each person is exactly one of: me, member_id, contact_id, person_name (or person_id to keep one)",
		"people", apierr.FieldInvalid)
	ErrDuplicatePaperPerson = apierr.Invalid("the same person is on the bill twice", "people", apierr.FieldInvalid)
)

// paperPerson — one row to write on a paper (resolved from the writer's input).
type paperPerson struct {
	ID        uuid.UUID // existing row (Nil = new)
	UserID    *uuid.UUID
	MemberID  *uuid.UUID
	Name      string
	ContactID *uuid.UUID // the writer's contact (bound for the writer)
	Share     float64
	Paid      float64
	Status    string
	Sort      int
}

// matcher — event members by identity, for one write (a bill of the event, or a paper moving in):
// the same user, the writer's bound contact, or a name-only member with the very same name (trim,
// NFC, case-sensitive). Anything else becomes a new member; a linked user gets the event invite.
type matcher struct {
	svc     *Service
	tx      pgx.Tx
	event   *Project
	writer  uuid.UUID
	byUser  map[uuid.UUID]uuid.UUID
	invited map[uuid.UUID]uuid.UUID // invited (not yet linked) user → member
	byBound map[uuid.UUID]uuid.UUID // writer's contact → member
	byName  map[string]uuid.UUID    // NFC name → name-only member
	invites []pendingInvite
	// noInvite — a contact who uses the app is added as their contact only;
	// the owner invites them later (the members page, owner 2026-11-03).
	noInvite bool
}

type pendingInvite struct {
	userID, memberID uuid.UUID
	accepted         bool
}

func newMatcher(s *Service, tx pgx.Tx, event *Project, writer uuid.UUID) *matcher {
	return &matcher{svc: s, tx: tx, event: event, writer: writer, byUser: map[uuid.UUID]uuid.UUID{},
		invited: map[uuid.UUID]uuid.UUID{}, byBound: map[uuid.UUID]uuid.UUID{}, byName: map[string]uuid.UUID{}}
}

func (m *matcher) load(ctx context.Context) error {
	rows, err := m.tx.Query(ctx, `
		SELECT pm.id, pm.user_id, pm.invited_user_id, normalize(btrim(pm.display_name), NFC), pmc.contact_id
		FROM project_members pm
		LEFT JOIN project_member_contacts pmc ON pmc.project_member_id = pm.id AND pmc.user_id = $2
		WHERE pm.project_id = $1 AND pm.status <> 'left'
		ORDER BY pm.created_at, pm.id`, m.event.ID, m.writer)
	if err != nil {
		return fmt.Errorf("event members: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id              uuid.UUID
			user, invitedTo *uuid.UUID
			name            string
			bound           *uuid.UUID
		)
		if err := rows.Scan(&id, &user, &invitedTo, &name, &bound); err != nil {
			return err
		}
		switch {
		case user != nil:
			m.byUser[*user] = id
		case invitedTo != nil:
			m.invited[*invitedTo] = id
		case bound == nil:
			if _, ok := m.byName[name]; !ok {
				m.byName[name] = id
			}
		}
		if bound != nil {
			m.byBound[*bound] = id
		}
	}
	return rows.Err()
}

func (m *matcher) insert(ctx context.Context, userID *uuid.UUID, name string) (*ProjectMember, error) {
	return m.svc.store.InsertMemberTx(ctx, m.tx, m.event.ID, userID, name, RoleContributor, MemberStatusActive, m.writer, nil)
}

// forUser — the member of a user (the writer: their own member).
func (m *matcher) forUser(ctx context.Context, userID uuid.UUID, name string) (uuid.UUID, error) {
	if id, ok := m.byUser[userID]; ok {
		return id, nil
	}
	if id, ok := m.invited[userID]; ok {
		return id, nil
	}
	if strings.TrimSpace(name) == "" {
		if name = userDisplayOrEmpty(ctx, m.tx, userID); name == "" {
			name = "Member"
		}
	}
	if userID == m.writer {
		return uuid.Nil, ErrNotMember
	}
	accepted := m.autoAcceptsInvite(ctx, userID)
	var linked *uuid.UUID
	if accepted {
		linked = &userID
	}
	mem, err := m.insert(ctx, linked, name)
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := m.tx.Exec(ctx, `UPDATE project_members SET invited_user_id = $2 WHERE id = $1`, mem.ID, userID); err != nil {
		return uuid.Nil, fmt.Errorf("invited member: %w", err)
	}
	if accepted {
		m.byUser[userID] = mem.ID
	} else {
		m.invited[userID] = mem.ID
	}
	m.invites = append(m.invites, pendingInvite{userID: userID, memberID: mem.ID, accepted: accepted})
	return mem.ID, nil
}

// forContact — the member of one of the writer's contacts.
func (m *matcher) forContact(ctx context.Context, contactID uuid.UUID) (uuid.UUID, error) {
	name, linked, err := contactOfTx(ctx, m.tx, m.writer, contactID)
	if err != nil {
		return uuid.Nil, err
	}
	// Already a member through this contact (maybe added and not invited yet).
	if id, ok := m.byBound[contactID]; ok {
		return id, nil
	}
	if linked != nil {
		id, ok := m.byUser[*linked]
		if !ok {
			id, ok = m.invited[*linked]
		}
		if !ok && !m.noInvite {
			if id, err = m.forUser(ctx, *linked, name); err != nil {
				return uuid.Nil, err
			}
			ok = true
		}
		if ok {
			return id, bindContactTx(ctx, m.tx, id, m.writer, contactID)
		}
	}
	mem, err := m.insert(ctx, nil, name)
	if err != nil {
		return uuid.Nil, err
	}
	m.byBound[contactID] = mem.ID
	return mem.ID, bindContactTx(ctx, m.tx, mem.ID, m.writer, contactID)
}

// forName — a name-only member with the very same name, else a new one.
func (m *matcher) forName(ctx context.Context, name string) (uuid.UUID, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return uuid.Nil, ErrBadPaperPerson
	}
	var key string
	if err := m.tx.QueryRow(ctx, `SELECT normalize($1::text, NFC)`, name).Scan(&key); err != nil {
		return uuid.Nil, fmt.Errorf("normalize name: %w", err)
	}
	if id, ok := m.byName[key]; ok {
		return id, nil
	}
	mem, err := m.insert(ctx, nil, name)
	if err != nil {
		return uuid.Nil, err
	}
	m.byName[key] = mem.ID
	return mem.ID, nil
}

func (m *matcher) autoAcceptsInvite(ctx context.Context, userID uuid.UUID) bool {
	if m.svc.notifs == nil {
		return false
	}
	d, err := m.svc.notifs.DeliveryFor(ctx, m.tx, userID, &m.writer, notifications.TypeProjectInvite)
	return err == nil && d.AcceptRequest
}

func (m *matcher) dispatchInvites(ctx context.Context) error {
	if m.svc.notifs == nil {
		m.invites = nil
		return nil
	}
	actor := userDisplayOrEmpty(ctx, m.tx, m.writer)
	for _, in := range m.invites {
		if err := m.svc.notifs.DispatchProjectInvite(ctx, m.tx, in.userID, m.writer, notifications.ProjectInvitePayload{
			ProjectMemberID: in.memberID, ProjectID: m.event.ID, ProjectName: m.event.Name,
			InviterUserID: m.writer, InviterDisplayName: actor, Role: RoleContributor,
		}, in.accepted); err != nil {
			return err
		}
	}
	m.invites = nil
	return nil
}

// contactOfTx — one of my contacts: its name (as I see it) and the user it is linked to.
func contactOfTx(ctx context.Context, tx pgx.Tx, userID, contactID uuid.UUID) (string, *uuid.UUID, error) {
	var (
		name   string
		linked *uuid.UUID
	)
	err := tx.QueryRow(ctx, `SELECT `+shared.PersonNameSQL("c", "lu")+`, c.linked_user_id
		FROM contacts c LEFT JOIN users lu ON lu.id = c.linked_user_id
		WHERE c.id = $1 AND c.user_id = $2`, contactID, userID).Scan(&name, &linked)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrContactNotFound
	}
	if err != nil {
		return "", nil, fmt.Errorf("contact: %w", err)
	}
	return name, linked, nil
}

// memberInfoTx — an event member's user and name.
func memberInfoTx(ctx context.Context, tx pgx.Tx, projectID, memberID uuid.UUID) (*uuid.UUID, string, error) {
	var (
		user *uuid.UUID
		name string
	)
	err := tx.QueryRow(ctx, `SELECT pm.user_id, COALESCE(u.display_name, pm.display_name)
		FROM project_members pm LEFT JOIN users u ON u.id = pm.user_id
		WHERE pm.id = $1 AND pm.project_id = $2`, memberID, projectID).Scan(&user, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrMemberNotInProject
	}
	if err != nil {
		return nil, "", fmt.Errorf("member: %w", err)
	}
	return user, name, nil
}

// resolvePeople — the writer's people input as rows to write. existing = the paper's rows now (an
// edit), keyed by id. event = the paper's event (nil = a split bill); m its matcher.
func (s *Service) resolvePeople(ctx context.Context, tx pgx.Tx, writer uuid.UUID, in []PaperPersonInput,
	existing map[uuid.UUID]paperPerson, event *Project, m *matcher) ([]paperPerson, error) {
	out := make([]paperPerson, 0, len(in))
	seenUser := map[uuid.UUID]bool{}
	seenMember := map[uuid.UUID]bool{}
	for i, p := range in {
		n := 0
		for _, set := range []bool{p.PersonID != nil, p.Me, p.MemberID != nil, p.ContactID != nil, p.PersonName != nil} {
			if set {
				n++
			}
		}
		if n != 1 || p.Share < 0 || p.Paid < 0 {
			return nil, ErrBadPaperPerson
		}
		row := paperPerson{Share: round2(p.Share), Paid: round2(p.Paid), Sort: i, Status: shared.PersonJoined}
		switch {
		case p.PersonID != nil:
			cur, ok := existing[*p.PersonID]
			if !ok {
				return nil, ErrBadPaperPerson
			}
			row.ID, row.UserID, row.MemberID, row.Name, row.Status = cur.ID, cur.UserID, cur.MemberID, cur.Name, cur.Status
		case p.Me:
			w := writer
			row.UserID = &w
			row.Name = userDisplayOrEmpty(ctx, tx, writer)
			if event != nil {
				id, err := m.forUser(ctx, writer, row.Name)
				if err != nil {
					return nil, err
				}
				row.MemberID = &id
			}
		case p.MemberID != nil:
			if event == nil {
				return nil, ErrBadPaperPerson
			}
			user, name, err := memberInfoTx(ctx, tx, event.ID, *p.MemberID)
			if err != nil {
				return nil, err
			}
			id := *p.MemberID
			row.MemberID, row.UserID, row.Name = &id, user, name
		case p.ContactID != nil:
			name, linked, err := contactOfTx(ctx, tx, writer, *p.ContactID)
			if err != nil {
				return nil, err
			}
			c := *p.ContactID
			row.Name, row.UserID, row.ContactID = name, linked, &c
			if event != nil {
				id, err := m.forContact(ctx, c)
				if err != nil {
					return nil, err
				}
				row.MemberID = &id
				row.UserID, _, _ = memberInfoTx(ctx, tx, event.ID, id)
			}
		default:
			name := strings.TrimSpace(*p.PersonName)
			if name == "" {
				return nil, ErrBadPaperPerson
			}
			row.Name = name
			if event != nil {
				id, err := m.forName(ctx, name)
				if err != nil {
					return nil, err
				}
				row.MemberID = &id
				row.UserID, row.Name, _ = memberInfoTx(ctx, tx, event.ID, id)
			}
		}
		if row.UserID != nil {
			if seenUser[*row.UserID] {
				return nil, ErrDuplicatePaperPerson
			}
			seenUser[*row.UserID] = true
		}
		if row.MemberID != nil {
			if seenMember[*row.MemberID] {
				return nil, ErrDuplicatePaperPerson
			}
			seenMember[*row.MemberID] = true
		}
		out = append(out, row)
	}
	return out, nil
}
