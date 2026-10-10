package accounts

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

// TestInviteMember — invite a wallet member by picking a linked contact
// (owner 2026-10-11), or by email (the older form).
func TestInviteMember(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	me := testdb.User(t, pool)
	friend := testdb.User(t, pool)
	other := testdb.User(t, pool)
	svc := NewService(NewStore(pool), nil, nil)
	svc.WithNotificationService(notifications.NewService(notifications.NewStore(pool)))
	wallet := testdb.Account(t, pool, me)

	contact := func(owner uuid.UUID, linked *uuid.UUID, status string) uuid.UUID {
		t.Helper()
		id := uuid.New()
		if _, err := pool.Exec(ctx, `INSERT INTO contacts (id, user_id, display_name, linked_user_id, status)
			VALUES ($1, $2, 'C', $3, $4)`, id, owner, linked, status); err != nil {
			t.Fatalf("contact: %v", err)
		}
		return id
	}
	t.Cleanup(func() {
		users := []uuid.UUID{me, friend, other}
		_, _ = pool.Exec(ctx, `DELETE FROM notifications WHERE recipient_user_id = ANY($1)`, users)
		_, _ = pool.Exec(ctx, `DELETE FROM contacts WHERE user_id = ANY($1)`, users)
	})
	linked := contact(me, &friend, "active")
	unlinked := contact(me, nil, "active")
	archived := contact(me, &other, "archived")
	notMine := contact(other, &friend, "active")

	invite := func(req InviteMemberRequest) (*AccountMember, error) {
		return svc.InviteMember(ctx, me, wallet, req)
	}

	m, err := invite(InviteMemberRequest{ContactID: &linked})
	if err != nil {
		t.Fatalf("invite linked contact: %v", err)
	}
	if m.UserID != friend || m.JoinedAt != nil {
		t.Errorf("member = %+v, want friend, pending", m)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM notifications
		WHERE recipient_user_id = $1 AND type = 'account_invite'`, friend).Scan(&n); err != nil || n != 1 {
		t.Errorf("account_invite to friend = %d (err %v), want 1", n, err)
	}
	if _, err := invite(InviteMemberRequest{ContactID: &linked}); !errors.Is(err, ErrAlreadyMember) {
		t.Errorf("invite again: err = %v", err)
	}

	for name, tc := range map[string]struct {
		req  InviteMemberRequest
		want error
	}{
		"unlinked":  {InviteMemberRequest{ContactID: &unlinked}, ErrContactNotLinked},
		"archived":  {InviteMemberRequest{ContactID: &archived}, ErrContactArchived},
		"not mine":  {InviteMemberRequest{ContactID: &notMine}, ErrContactNotFound},
		"neither":   {InviteMemberRequest{}, ErrInviteTarget},
		"both":      {InviteMemberRequest{ContactID: &linked, Email: testdb.Str("x@test.local")}, ErrInviteTarget},
		"bad email": {InviteMemberRequest{Email: testdb.Str("nobody@test.local")}, ErrUserNotFound},
	} {
		if _, err := invite(tc.req); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}

	// Email still works.
	var email string
	if err := pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, other).Scan(&email); err != nil {
		t.Fatal(err)
	}
	if m, err := invite(InviteMemberRequest{Email: &email}); err != nil || m.UserID != other {
		t.Errorf("invite by email: %+v, %v", m, err)
	}
}
