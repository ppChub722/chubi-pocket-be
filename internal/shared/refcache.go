package shared

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

type refCacheKey struct{}

type refCache struct {
	mu   sync.Mutex
	accs map[uuid.UUID]AccountRef
	cats map[uuid.UUID]CategoryRef
}

// WithRefCache lets the wallet and category refs one page read once be reused by every
// LoadAccountRefs / LoadCategoryRefs after it, so a page that resolves them in several
// sections (the items, the upcoming items, the views) asks for each only once. Install it
// where the page starts reading, never before a write in the same request.
func WithRefCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, refCacheKey{}, &refCache{
		accs: map[uuid.UUID]AccountRef{}, cats: map[uuid.UUID]CategoryRef{},
	})
}

func refCacheOf(ctx context.Context) *refCache {
	c, _ := ctx.Value(refCacheKey{}).(*refCache)
	return c
}

// takeAccounts moves the cached refs of ids into out and returns the ids still to read.
func (c *refCache) takeAccounts(ids []uuid.UUID, out map[uuid.UUID]AccountRef) []uuid.UUID {
	c.mu.Lock()
	defer c.mu.Unlock()
	var missing []uuid.UUID
	for _, id := range ids {
		if r, ok := c.accs[id]; ok {
			out[id] = r
		} else {
			missing = append(missing, id)
		}
	}
	return missing
}

func (c *refCache) takeCategories(ids []uuid.UUID, out map[uuid.UUID]CategoryRef) []uuid.UUID {
	c.mu.Lock()
	defer c.mu.Unlock()
	var missing []uuid.UUID
	for _, id := range ids {
		if r, ok := c.cats[id]; ok {
			out[id] = r
		} else {
			missing = append(missing, id)
		}
	}
	return missing
}

func (c *refCache) putAccounts(refs map[uuid.UUID]AccountRef) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, r := range refs {
		c.accs[id] = r
	}
}

func (c *refCache) putCategories(refs map[uuid.UUID]CategoryRef) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, r := range refs {
		c.cats[id] = r
	}
}
