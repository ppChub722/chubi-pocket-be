package projects

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// 0.4.2-3 F3: a member is MY contact through my own binding; two people's "แม่" never merge by name.
func TestMemberContactsArePerViewer(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()
	momA := f.newContact(t, f.owner, "แม่", nil)
	momB := f.newContact(t, f.friend, "แม่", nil)
	f.insertDebt(t, f.owner, &momA, "แม่", f.bill1, 50, 0, "open")

	res, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{Name: "Trip", TransactionIDs: []uuid.UUID{f.bill1}})
	if err != nil {
		t.Fatalf("quick create: %v", err)
	}
	event := res.Detail.Event.ID
	// Bee accepts (linked from here on).
	if _, err := f.pool.Exec(ctx, `UPDATE project_members SET user_id = invited_user_id
		WHERE project_id = $1 AND invited_user_id = $2`, event, f.friend); err != nil {
		t.Fatal(err)
	}
	mom := func(viewer uuid.UUID) (uuid.UUID, *uuid.UUID) {
		t.Helper()
		ms, err := loadMembers(ctx, f.pool, viewer, event)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ms {
			if m.Name == "แม่" {
				return m.ID, m.ContactID
			}
		}
		t.Fatal("no แม่ member")
		return uuid.Nil, nil
	}
	id, c := mom(f.owner)
	if c == nil || *c != momA {
		t.Errorf("owner's แม่ = %v, want their contact %s", c, momA)
	}
	if _, c := mom(f.friend); c != nil {
		t.Errorf("Bee's own แม่ matched by name: %v", c)
	}

	if _, err := f.svc.BindMyContact(ctx, f.friend, event, id, &momB); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if _, c := mom(f.friend); c == nil || *c != momB {
		t.Errorf("Bee's binding = %v, want %s", c, momB)
	}
	if _, c := mom(f.owner); c == nil || *c != momA {
		t.Errorf("owner's แม่ changed by Bee's binding: %v", c)
	}
	// Deleting the contact makes the member a plain name again for that viewer.
	if _, err := f.pool.Exec(ctx, `DELETE FROM contacts WHERE id = $1`, momB); err != nil {
		t.Fatal(err)
	}
	if _, c := mom(f.friend); c != nil {
		t.Errorf("deleted contact still bound: %v", c)
	}
}
