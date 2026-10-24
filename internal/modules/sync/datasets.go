package sync

import (
	"context"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/accounts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/contacts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/projects"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/providers"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/recurring"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/tags"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

// Loader returns the body of the dataset's existing endpoint.
type Loader func(ctx context.Context, userID uuid.UUID, tz string) (any, error)

// Services are the existing services the datasets are read through. Each loader calls the service
// the matching endpoint calls; no query is repeated here.
type Services struct {
	Accounts     *accounts.Service
	Categories   *categories.Service
	Tags         *tags.Service
	Contacts     *contacts.Service
	Debts        *personal_debts.Service
	Projects     *projects.Service
	Recurring    *recurring.Service
	Transactions *transactions.Service
	Providers    *providers.Store
}

// dataset is one A dataset.
type dataset struct {
	load Loader
}

func datasets(s Services) map[string]dataset {
	return map[string]dataset{
		Accounts: {load: func(ctx context.Context, uid uuid.UUID, tz string) (any, error) {
			return s.Accounts.ListPage(ctx, uid, "all", "", tz)
		}},
		Categories: {load: func(ctx context.Context, uid uuid.UUID, tz string) (any, error) {
			cats, err := s.Categories.ListMonth(ctx, uid, categories.ListFilter{IncludeSystem: true}, tz)
			if err != nil {
				return nil, err
			}
			return categories.ListResponse{Data: cats}, nil
		}},
		Tags: {load: func(ctx context.Context, uid uuid.UUID, _ string) (any, error) {
			list, err := s.Tags.List(ctx, uid)
			if err != nil {
				return nil, err
			}
			return tags.ListResponse{Data: list}, nil
		}},
		Contacts: {load: func(ctx context.Context, uid uuid.UUID, _ string) (any, error) {
			list, err := s.Contacts.List(ctx, uid, contacts.ListFilter{Status: "all"})
			if err != nil {
				return nil, err
			}
			return contacts.ListResponse{Data: list}, nil
		}},
		DebtPeople: {load: func(ctx context.Context, uid uuid.UUID, _ string) (any, error) {
			return s.Debts.PeopleAll(ctx, uid)
		}},
		Projects: {load: func(ctx context.Context, uid uuid.UUID, _ string) (any, error) {
			return s.Projects.ListAll(ctx, uid, projects.ListFilter{Status: "all"})
		}},
		Recurring: {load: func(ctx context.Context, uid uuid.UUID, tz string) (any, error) {
			return s.Recurring.ListAll(ctx, uid, tz)
		}},
		FormDefaults: {load: func(ctx context.Context, uid uuid.UUID, _ string) (any, error) {
			return s.Transactions.FormDefaults(ctx, uid)
		}},
		Providers: {load: func(ctx context.Context, _ uuid.UUID, _ string) (any, error) {
			list, err := s.Providers.List(ctx)
			if err != nil {
				return nil, err
			}
			return providers.ListResponse{Data: list, Count: len(list)}, nil
		}},
	}
}
