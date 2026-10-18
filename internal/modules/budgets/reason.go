package budgets

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

const (
	ReasonAll         = "all"          // the budget counts all expenses
	ReasonCategory    = "category"     // the row's category is one of the scope categories
	ReasonSubCategory = "sub_category" // a sub-category of a scope category
	ReasonTag         = "tag"
	ReasonWallet      = "wallet"
)

func (s *Service) reasoner(ctx context.Context, b *Budget) (func(countRow) string, error) {
	expanded, err := s.store.expandCategories(ctx, b.Categories)
	if err != nil {
		return nil, err
	}
	direct := map[uuid.UUID]bool{}
	for _, c := range b.Categories {
		direct[c.ID] = true
	}
	deep := map[uuid.UUID]bool{}
	for _, id := range expanded {
		deep[id] = true
	}
	tags := map[string]bool{}
	for _, t := range b.TagNames {
		tags[strings.ToLower(t)] = true
	}
	wallets := map[uuid.UUID]bool{}
	for _, w := range b.Wallets {
		wallets[w] = true
	}
	return func(r countRow) string {
		if b.ScopeAll {
			return ReasonAll
		}
		if r.CategoryID != nil {
			if direct[*r.CategoryID] {
				return ReasonCategory
			}
			if deep[*r.CategoryID] {
				return ReasonSubCategory
			}
		}
		for _, t := range r.Tags {
			if tags[strings.ToLower(t)] {
				return ReasonTag
			}
		}
		if r.AccountID != nil && wallets[*r.AccountID] {
			return ReasonWallet
		}
		return ""
	}, nil
}
