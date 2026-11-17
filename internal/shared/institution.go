package shared

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// InstitutionRef — a bank / card issuer / finance company / store of the user (0.4.3), embedded
// wherever a row points at one (a wallet, a debt counterparty, a recurring item).
type InstitutionRef struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Kind     string    `json:"kind"`
	IconCode *IconCode `json:"icon_code"`
}

// InstitutionKey — the counterparty key of an institution in the debt pages.
func InstitutionKey(id uuid.UUID) string { return "i:" + id.String() }

// LoadInstitutionRefs — one query for the institutions of a page.
func LoadInstitutionRefs(ctx context.Context, q Querier, ids []uuid.UUID) (map[uuid.UUID]InstitutionRef, error) {
	out := map[uuid.UUID]InstitutionRef{}
	ids = uniqueIDs(ids)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT id, name, kind, icon_code FROM institutions WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("institution refs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			r    InstitutionRef
			icon []byte
		)
		if err := rows.Scan(&r.ID, &r.Name, &r.Kind, &icon); err != nil {
			return nil, err
		}
		r.IconCode = IconFromJSON(icon)
		out[r.ID] = r
	}
	return out, rows.Err()
}
