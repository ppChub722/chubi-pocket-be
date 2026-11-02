package shared

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
)

type memberRefsKey struct{}

type memberRefCache struct {
	mu   sync.Mutex
	refs map[[2]uuid.UUID]MemberRef // (caller, member id)
}

// WithMemberRefCache lets the member refs one request reads once be reused by every
// LoadMemberRefs after it (an event page asks for the same members in three sections).
func WithMemberRefCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, memberRefsKey{}, &memberRefCache{refs: map[[2]uuid.UUID]MemberRef{}})
}

// RememberMemberRefs hands the cache refs a caller already holds.
func RememberMemberRefs(ctx context.Context, callerUserID uuid.UUID, refs []MemberRef) {
	c, _ := ctx.Value(memberRefsKey{}).(*memberRefCache)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range refs {
		c.refs[[2]uuid.UUID{callerUserID, r.ID}] = r
	}
}

// LoadMemberRefs — event members by id, page-ready (a linked user's live name / icon).
func LoadMemberRefs(ctx context.Context, q Querier, callerUserID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]MemberRef, error) {
	out := map[uuid.UUID]MemberRef{}
	if len(ids) == 0 {
		return out, nil
	}
	cache, _ := ctx.Value(memberRefsKey{}).(*memberRefCache)
	missing := uniqueIDs(ids)
	if cache != nil {
		cache.mu.Lock()
		missing = missing[:0:0]
		for _, id := range uniqueIDs(ids) {
			if r, ok := cache.refs[[2]uuid.UUID{callerUserID, id}]; ok {
				out[id] = r
			} else {
				missing = append(missing, id)
			}
		}
		cache.mu.Unlock()
	}
	if len(missing) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT pm.id, pm.user_id,
			COALESCE(u.display_name, pm.display_name), `+AvatarSQL("u.id", "pm.icon_code")+`, pm.removed_at IS NOT NULL
		FROM project_members pm LEFT JOIN users u ON u.id = pm.user_id
		WHERE pm.id = ANY($1::uuid[])`, missing)
	if err != nil {
		return nil, fmt.Errorf("member refs: %w", err)
	}
	defer rows.Close()
	var loaded []MemberRef
	for rows.Next() {
		var (
			r    MemberRef
			icon []byte
		)
		if err := rows.Scan(&r.ID, &r.UserID, &r.Name, &icon, &r.Removed); err != nil {
			return nil, err
		}
		r.Avatar = AvatarFromSrc(r.Name, icon)
		r.IsMe = r.UserID != nil && *r.UserID == callerUserID
		out[r.ID] = r
		loaded = append(loaded, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	RememberMemberRefs(ctx, callerUserID, loaded)
	return out, nil
}

// TextOr — the text of a nullable string ("" for nil).
func TextOr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
