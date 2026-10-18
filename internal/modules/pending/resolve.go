package pending

import (
	"context"

	"github.com/google/uuid"

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

func ResolveDrafts(ctx context.Context, q shared.Querier, userID uuid.UUID, drafts []Draft) ([]DraftResolved, error) {
	var accIDs, catIDs, eventIDs, tagIDs, contactIDs, goalIDs []uuid.UUID
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
