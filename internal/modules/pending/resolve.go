package pending

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/projects"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

const (
	missingAccount    = "account_id"
	missingTransferTo = "transfer_to_account_id"
	missingCategory   = "category_id"
	missingTags       = "tag_ids"
	missingProject    = "project_id"
	missingContact    = "contact_id"
	missingGoal       = "goal_id"
)

// billPeopleOf — an event draft's payer and members, payer first.
func billPeopleOf(d Draft) []projects.BillPerson {
	var out []projects.BillPerson
	if d.Payer != nil {
		out = append(out, *d.Payer)
	}
	for _, m := range d.Members {
		out = append(out, m.BillPerson)
	}
	return out
}

func resolveBillPerson(p projects.BillPerson, persons map[uuid.UUID]shared.PersonRef, members map[uuid.UUID]shared.MemberRef) ResolvedBillPerson {
	r := ResolvedBillPerson{Me: p.Me, MemberID: p.MemberID, ContactID: p.ContactID, Name: strings.TrimSpace(p.PersonName)}
	switch {
	case p.MemberID != nil:
		if m, ok := members[*p.MemberID]; ok {
			r.Name, r.Me = m.Name, r.Me || m.IsMe
		}
	case p.ContactID != nil:
		r.Name = shared.ResolvePerson(persons, shared.PersonKey{ContactID: p.ContactID, Snapshot: r.Name}).Name
	}
	return r
}

func ResolveDrafts(ctx context.Context, q shared.Querier, userID uuid.UUID, drafts []Draft) ([]DraftResolved, error) {
	var accIDs, catIDs, eventIDs, tagIDs, contactIDs, goalIDs, memberIDs []uuid.UUID
	for _, d := range drafts {
		if d.GoalID != nil {
			goalIDs = append(goalIDs, *d.GoalID)
		}
		if d.AccountID != nil {
			accIDs = append(accIDs, *d.AccountID)
		}
		if d.TransferToAccountID != nil {
			accIDs = append(accIDs, *d.TransferToAccountID)
		}
		if d.CategoryID != nil {
			catIDs = append(catIDs, *d.CategoryID)
		}
		if d.ProjectID != nil {
			eventIDs = append(eventIDs, *d.ProjectID)
		}
		tagIDs = append(tagIDs, d.TagIDs...)
		for _, sp := range d.Splits {
			if sp.ContactID != nil {
				contactIDs = append(contactIDs, *sp.ContactID)
			}
		}
		if d.PaidBy != nil && d.PaidBy.ContactID != nil {
			contactIDs = append(contactIDs, *d.PaidBy.ContactID)
		}
		for _, bp := range billPeopleOf(d) {
			if bp.ContactID != nil {
				contactIDs = append(contactIDs, *bp.ContactID)
			}
			if bp.MemberID != nil {
				memberIDs = append(memberIDs, *bp.MemberID)
			}
		}
	}
	members, err := shared.LoadMemberRefs(ctx, q, userID, memberIDs)
	if err != nil {
		return nil, err
	}
	accounts, err := shared.LoadAccountRefs(ctx, q, accIDs)
	if err != nil {
		return nil, err
	}
	cats, err := shared.LoadCategoryRefs(ctx, q, catIDs)
	if err != nil {
		return nil, err
	}
	events, err := shared.LoadEventRefs(ctx, q, eventIDs)
	if err != nil {
		return nil, err
	}
	tagRefs, err := shared.LoadTagRefs(ctx, q, userID, tagIDs)
	if err != nil {
		return nil, err
	}
	persons, err := shared.LoadPersons(ctx, q, userID, contactIDs)
	if err != nil {
		return nil, err
	}
	goals, err := shared.LoadSavingGoalRefs(ctx, q, goalIDs)
	if err != nil {
		return nil, err
	}

	out := make([]DraftResolved, len(drafts))
	for i, d := range drafts {
		r := DraftResolved{Tags: []shared.TagRef{}, Splits: []ResolvedSplit{}, Missing: []string{}}
		if d.AccountID != nil {
			if a, ok := accounts[*d.AccountID]; ok {
				r.Account = &a
			} else {
				r.Missing = append(r.Missing, missingAccount)
			}
		}
		if d.TransferToAccountID != nil {
			if a, ok := accounts[*d.TransferToAccountID]; ok {
				r.TransferToAccount = &a
			} else {
				r.Missing = append(r.Missing, missingTransferTo)
			}
		}
		if d.CategoryID != nil {
			if c, ok := cats[*d.CategoryID]; ok {
				r.Category = &c
			} else {
				r.Missing = append(r.Missing, missingCategory)
			}
		}
		tagGone := false
		for _, id := range d.TagIDs {
			if t, ok := tagRefs[id]; ok {
				r.Tags = append(r.Tags, t)
			} else {
				tagGone = true
			}
		}
		if tagGone {
			r.Missing = append(r.Missing, missingTags)
		}
		if d.ProjectID != nil {
			if e, ok := events[*d.ProjectID]; ok {
				r.Project = &e
			} else {
				r.Missing = append(r.Missing, missingProject)
			}
		}
		contactGone := false
		for _, sp := range d.Splits {
			if sp.ContactID != nil {
				if _, ok := persons[*sp.ContactID]; !ok {
					contactGone = true
				}
			}
			r.Splits = append(r.Splits, ResolvedSplit{
				Person:     shared.ResolvePerson(persons, shared.PersonKey{ContactID: sp.ContactID, Snapshot: sp.PersonName}),
				OwedAmount: sp.OwedAmount,
			})
		}
		if pb := d.PaidBy; pb != nil {
			if pb.ContactID != nil {
				if _, ok := persons[*pb.ContactID]; !ok {
					contactGone = true
				}
			}
			r.PaidBy = &ResolvedPaidBy{
				Person: shared.ResolvePerson(persons, shared.PersonKey{ContactID: pb.ContactID, Snapshot: pb.PersonName}),
				Amount: pb.Amount,
			}
			if d.Amount != nil {
				mine := *d.Amount - pb.Amount
				r.PaidBy.MyPaid = &mine
			}
		}
		r.LinkMode = draftLinkMode(d)
		if d.Payer != nil || len(d.Members) > 0 {
			payer := projects.BillPerson{Me: true}
			if d.Payer != nil {
				payer = *d.Payer
			}
			bp := &ResolvedBillPeople{Payer: resolveBillPerson(payer, persons, members), Members: []ResolvedBillPerson{}}
			for _, m := range d.Members {
				rm := resolveBillPerson(m.BillPerson, persons, members)
				share := m.Share
				rm.Share = &share
				bp.Members = append(bp.Members, rm)
			}
			r.BillPeople = bp
		}
		if contactGone {
			r.Missing = append(r.Missing, missingContact)
		}
		if d.GoalID != nil {
			if g, ok := goals[*d.GoalID]; ok {
				r.Goal = &g
			} else {
				r.Missing = append(r.Missing, missingGoal)
			}
		}
		out[i] = r
	}
	return out, nil
}

func ResolveRows(ctx context.Context, q shared.Querier, userID uuid.UUID, rows []PendingTransaction) error {
	drafts := make([]Draft, len(rows))
	for i := range rows {
		drafts[i] = rows[i].Draft
	}
	res, err := ResolveDrafts(ctx, q, userID, drafts)
	if err != nil {
		return err
	}
	for i := range rows {
		rows[i].Resolved = res[i]
		rows[i].NeedsInput, rows[i].CanConfirm = readiness(&rows[i])
	}
	return nil
}

type Resolver struct{ db shared.Querier }

func NewResolver(db shared.Querier) *Resolver { return &Resolver{db: db} }

func (r *Resolver) Resolve(ctx context.Context, userID uuid.UUID, drafts []Draft) ([]DraftResolved, error) {
	return ResolveDrafts(ctx, r.db, userID, drafts)
}
