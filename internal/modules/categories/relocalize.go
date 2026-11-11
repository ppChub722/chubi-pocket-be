package categories

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// seedEntry is one starter category flattened out of the catalog.
type seedEntry struct {
	Key         string
	ParentKey   string // "" = a root
	Type        string
	Name        Text
	Description Text // nil = the starter has no description
}

// seedEntries lists every starter category, roots before their children.
func seedEntries() []seedEntry {
	out := make([]seedEntry, 0, 64)
	for _, r := range starterCatalog {
		out = append(out, seedEntry{Key: r.Key, Type: "expense", Name: r.Name})
		for _, c := range r.Children {
			out = append(out, seedEntry{Key: c.Key, ParentKey: r.Key, Type: "expense", Name: c.Name, Description: c.Description})
		}
	}
	for _, in := range starterIncomeCatalog {
		out = append(out, seedEntry{Key: in.Key, Type: "income", Name: in.Name, Description: in.Description})
	}
	return out
}

// Relocalize rewrites the name and description of the user's starter categories into lang. A value
// is rewritten only while it still equals a starter text (in any language): one the user edited is
// theirs and stays. A name that would collide with a sibling at the same level is skipped.
func (s *Store) Relocalize(ctx context.Context, userID uuid.UUID, lang string) error {
	lang = NormalizeLang(lang)
	entries := make(map[string]seedEntry)
	for _, e := range seedEntries() {
		entries[e.Key] = e
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin relocalize: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	type row struct {
		id          uuid.UUID
		key, name   string
		description *string
	}
	rows, err := tx.Query(ctx,
		`SELECT id, seed_key, name, description FROM categories WHERE user_id = $1 AND seed_key IS NOT NULL`, userID)
	if err != nil {
		return fmt.Errorf("list starters: %w", err)
	}
	var found []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.key, &r.name, &r.description); err != nil {
			rows.Close()
			return fmt.Errorf("scan starter: %w", err)
		}
		found = append(found, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, r := range found {
		e, ok := entries[r.key]
		if !ok {
			continue // a key from a newer / older catalog
		}
		if want := e.Name.In(lang); want != r.name && e.Name.Is(r.name) {
			if _, err := tx.Exec(ctx, `
				UPDATE categories c SET name = $2, updated_at = now()
				 WHERE c.id = $1
				   AND NOT EXISTS (SELECT 1 FROM categories o
				                    WHERE o.user_id = c.user_id AND o.type = c.type AND o.id <> c.id
				                      AND o.parent_id IS NOT DISTINCT FROM c.parent_id
				                      AND lower(o.name) = lower($2))`,
				r.id, want); err != nil {
				return fmt.Errorf("rename starter %s: %w", r.key, err)
			}
		}
		if e.Description != nil && r.description != nil {
			if want := e.Description.In(lang); want != *r.description && e.Description.Is(*r.description) {
				if _, err := tx.Exec(ctx,
					`UPDATE categories SET description = $2, updated_at = now() WHERE id = $1`, r.id, want); err != nil {
					return fmt.Errorf("describe starter %s: %w", r.key, err)
				}
			}
		}
	}
	return tx.Commit(ctx)
}

// BackfillSeedKeys gives rows seeded before seed_key existed their key, matched by name (in any
// language) under the matching parent. Idempotent; a row the user renamed no longer matches and
// stays a plain category. Returns how many rows it keyed.
func (s *Store) BackfillSeedKeys(ctx context.Context) (int, error) {
	total := 0
	for _, e := range seedEntries() {
		names := make([]string, 0, len(e.Name))
		for _, n := range e.Name {
			names = append(names, strings.ToLower(n))
		}
		var parentClause string
		args := []any{e.Key, e.Type, names}
		if e.ParentKey == "" {
			parentClause = `c.parent_id IS NULL`
		} else {
			args = append(args, e.ParentKey)
			parentClause = `EXISTS (SELECT 1 FROM categories p WHERE p.id = c.parent_id AND p.seed_key = $4)`
		}
		// DISTINCT ON keeps one row per user, so the unique (user_id, seed_key) index can't trip.
		tag, err := s.db.Exec(ctx, `
			UPDATE categories SET seed_key = $1
			 WHERE id IN (
				SELECT DISTINCT ON (c.user_id) c.id
				  FROM categories c
				 WHERE c.seed_key IS NULL AND NOT c.is_system AND c.type = $2
				   AND lower(c.name) = ANY($3) AND `+parentClause+`
				   AND NOT EXISTS (SELECT 1 FROM categories o WHERE o.user_id = c.user_id AND o.seed_key = $1)
				 ORDER BY c.user_id, c.created_at)`, args...)
		if err != nil {
			return total, fmt.Errorf("backfill %s: %w", e.Key, err)
		}
		total += int(tag.RowsAffected())
	}
	return total, nil
}
