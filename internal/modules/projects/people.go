package projects

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// ResolvePeopleRequest (0.4.0-3, the event bill sheet) — the people a bill names, as event members:
// me, a member, a contact or a typed name. Anyone who isn't a member yet is added.
type ResolvePeopleRequest struct {
	People []BillPerson `json:"people" binding:"required,min=1,dive"`
}

// ResolvePeopleResult — MemberIDs in the request's order, and the event after any additions.
type ResolvePeopleResult struct {
	EventPatch
	MemberIDs []uuid.UUID `json:"member_ids"`
}

func (s *Service) ResolvePeople(
	ctx context.Context, callerUserID, projectID uuid.UUID, req ResolvePeopleRequest,
) (*ResolvePeopleResult, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	p, me, mb, err := s.billWriterTx(ctx, tx, callerUserID, projectID)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(req.People))
	for _, person := range req.People {
		if person.Empty() {
			return nil, ErrBadBillPeople
		}
		id, err := mb.memberForPerson(ctx, person, me.ID)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := s.dispatchAddedTx(ctx, tx, p, mb, me.DisplayName); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	patch, err := s.patch(ctx, callerUserID, projectID, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	return &ResolvePeopleResult{EventPatch: *patch, MemberIDs: ids}, nil
}
