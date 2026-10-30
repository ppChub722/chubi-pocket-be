package projects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

var (
	ErrEventTagNotFound = errors.New("event tag not found")
	ErrUnknownTag       = errors.New("tag not found")
	ErrTagNameTaken     = errors.New("this event already has a tag with that name")
	ErrTagNameRequired  = apierr.Invalid("name is required", "name", apierr.FieldRequired)
)

func cleanTagName(n string) string {
	n = strings.TrimSpace(n)
	if r := []rune(n); len(r) > 50 {
		n = string(r[:50])
	}
	return n
}

func setRowTagsTx(ctx context.Context, tx pgx.Tx, projectID, ptID uuid.UUID, tagIDs []uuid.UUID) error {
	tagIDs = uniqueIDs(tagIDs)
	if len(tagIDs) > 0 {
		var n int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM event_tags WHERE project_id = $1 AND id = ANY($2)`,
			projectID, tagIDs).Scan(&n); err != nil {
			return fmt.Errorf("check event tags: %w", err)
		}
		if n != len(tagIDs) {
			return ErrUnknownTag
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM project_transaction_tags WHERE project_transaction_id = $1`, ptID); err != nil {
		return fmt.Errorf("clear row tags: %w", err)
	}
	if len(tagIDs) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO project_transaction_tags (project_transaction_id, event_tag_id)
		SELECT $1, unnest($2::uuid[])`, ptID, tagIDs)
	if err != nil {
		return fmt.Errorf("set row tags: %w", err)
	}
	return nil
}

func ensureEventTagsTx(ctx context.Context, tx pgx.Tx, projectID, userID uuid.UUID, names []string) ([]uuid.UUID, error) {
	out := make([]uuid.UUID, 0, len(names))
	seen := map[string]bool{}
	for _, raw := range names {
		name := cleanTagName(raw)
		key := strings.ToLower(name)
		if name == "" || seen[key] {
			continue
		}
		seen[key] = true
		var id uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM event_tags WHERE project_id = $1 AND LOWER(name) = $2`,
			projectID, key).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			id, _ = uuid.NewV7()
			_, err = tx.Exec(ctx, `INSERT INTO event_tags (id, project_id, name, created_by_user_id, updated_by_user_id)
				VALUES ($1, $2, $3, $4, $4)`, id, projectID, name, userID)
		}
		if err != nil {
			return nil, fmt.Errorf("event tag %q: %w", name, err)
		}
		out = append(out, id)
	}
	return out, nil
}

func personalTagNamesTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT t.name FROM transaction_tags tt JOIN tags t ON t.id = tt.tag_id
		WHERE tt.transaction_id = $1 AND t.user_id = $2 ORDER BY LOWER(t.name)`, txID, userID)
	if err != nil {
		return nil, fmt.Errorf("tx tags: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func personalTagNamesByIDTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, tagIDs []uuid.UUID) ([]string, error) {
	if len(tagIDs) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT name FROM tags WHERE user_id = $1 AND id = ANY($2) ORDER BY LOWER(name)`, userID, tagIDs)
	if err != nil {
		return nil, fmt.Errorf("tag names: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func MatchPersonalTags(ctx context.Context, q shared.Querier, userID uuid.UUID, names []string) (matched []uuid.UUID, missing []string, err error) {
	lowered := make([]string, 0, len(names))
	for _, n := range names {
		if n = cleanTagName(n); n != "" {
			lowered = append(lowered, strings.ToLower(n))
		}
	}
	if len(lowered) == 0 {
		return nil, nil, nil
	}
	rows, err := q.Query(ctx, `SELECT id, LOWER(name) FROM tags WHERE user_id = $1 AND LOWER(name) = ANY($2)`, userID, lowered)
	if err != nil {
		return nil, nil, fmt.Errorf("match tags: %w", err)
	}
	have := map[string]uuid.UUID{}
	for rows.Next() {
		var (
			id   uuid.UUID
			name string
		)
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return nil, nil, err
		}
		have[name] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	for i, n := range names {
		_ = i
		key := strings.ToLower(cleanTagName(n))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if id, ok := have[key]; ok {
			matched = append(matched, id)
		} else {
			missing = append(missing, cleanTagName(n))
		}
	}
	return matched, missing, nil
}

func setMyTagsTx(ctx context.Context, tx pgx.Tx, projectID, userID uuid.UUID, tagIDs []uuid.UUID) error {
	tagIDs = uniqueIDs(tagIDs)
	if len(tagIDs) > 0 {
		var n int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM tags WHERE user_id = $1 AND id = ANY($2)`,
			userID, tagIDs).Scan(&n); err != nil {
			return fmt.Errorf("check my tags: %w", err)
		}
		if n != len(tagIDs) {
			return ErrUnknownTag
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM project_my_tags WHERE project_id = $1 AND user_id = $2`, projectID, userID); err != nil {
		return fmt.Errorf("clear my tags: %w", err)
	}
	if len(tagIDs) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO project_my_tags (project_id, user_id, tag_id)
		SELECT $1, $2, unnest($3::uuid[])`, projectID, userID, tagIDs)
	if err != nil {
		return fmt.Errorf("set my tags: %w", err)
	}
	return nil
}

func (s *Service) tagWriteGate(ctx context.Context, callerUserID, projectID uuid.UUID) error {
	if _, err := s.store.GetByIDForCaller(ctx, callerUserID, projectID); err != nil {
		return err
	}
	if err := s.store.AssertWritable(ctx, projectID, "edit_pt"); err != nil {
		return err
	}
	_, err := s.store.AssertCanWrite(ctx, projectID, callerUserID)
	return err
}

func (s *Service) ListTags(ctx context.Context, callerUserID, projectID uuid.UUID) (*EventTagList, error) {
	if _, err := s.store.GetByIDForCaller(ctx, callerUserID, projectID); err != nil {
		return nil, err
	}
	tags, err := loadEventTags(ctx, s.store.db, callerUserID, projectID)
	if err != nil {
		return nil, err
	}
	return &EventTagList{Data: tags}, nil
}

func iconJSON(ic *shared.IconCode) ([]byte, error) {
	if ic == nil {
		return nil, nil
	}
	return json.Marshal(ic)
}

func isNameTaken(err error) bool { return err != nil && isUniqueViolation(err) }

func (s *Service) CreateTag(ctx context.Context, callerUserID, projectID uuid.UUID, req EventTagRequest) (*EventTagWriteResult, error) {
	if req.Name == nil || cleanTagName(*req.Name) == "" {
		return nil, ErrTagNameRequired
	}
	if err := s.tagWriteGate(ctx, callerUserID, projectID); err != nil {
		return nil, err
	}
	icon, err := iconJSON(req.IconCode)
	if err != nil {
		return nil, err
	}
	id, _ := uuid.NewV7()
	_, err = s.store.db.Exec(ctx, `INSERT INTO event_tags (id, project_id, name, icon_code, created_by_user_id, updated_by_user_id)
		VALUES ($1, $2, $3, $4::jsonb, $5, $5)`, id, projectID, cleanTagName(*req.Name), icon, callerUserID)
	if isNameTaken(err) {
		return nil, ErrTagNameTaken
	}
	if err != nil {
		return nil, fmt.Errorf("create event tag: %w", err)
	}
	return s.tagResult(ctx, callerUserID, projectID, id)
}

func (s *Service) UpdateTag(ctx context.Context, callerUserID, projectID, tagID uuid.UUID, req EventTagRequest) (*EventTagWriteResult, error) {
	if err := s.tagWriteGate(ctx, callerUserID, projectID); err != nil {
		return nil, err
	}
	q := `UPDATE event_tags SET updated_by_user_id = $3`
	args := []any{tagID, projectID, callerUserID}
	if req.Name != nil {
		if cleanTagName(*req.Name) == "" {
			return nil, ErrTagNameRequired
		}
		args = append(args, cleanTagName(*req.Name))
		q += fmt.Sprintf(", name = $%d", len(args))
	}
	if req.iconPresent {
		icon, err := iconJSON(req.IconCode)
		if err != nil {
			return nil, err
		}
		args = append(args, icon)
		q += fmt.Sprintf(", icon_code = $%d::jsonb", len(args))
	}
	tag, err := s.store.db.Exec(ctx, q+` WHERE id = $1 AND project_id = $2`, args...)
	if isNameTaken(err) {
		return nil, ErrTagNameTaken
	}
	if err != nil {
		return nil, fmt.Errorf("update event tag: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrEventTagNotFound
	}
	return s.tagResult(ctx, callerUserID, projectID, tagID)
}

func (s *Service) DeleteTag(ctx context.Context, callerUserID, projectID, tagID uuid.UUID) (*EventPatch, error) {
	if err := s.tagWriteGate(ctx, callerUserID, projectID); err != nil {
		return nil, err
	}
	tag, err := s.store.db.Exec(ctx, `DELETE FROM event_tags WHERE id = $1 AND project_id = $2`, tagID, projectID)
	if err != nil {
		return nil, fmt.Errorf("delete event tag: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrEventTagNotFound
	}
	return s.patch(ctx, callerUserID, projectID, nil, nil, nil)
}

func (s *Service) tagResult(ctx context.Context, callerUserID, projectID, tagID uuid.UUID) (*EventTagWriteResult, error) {
	p, err := s.patch(ctx, callerUserID, projectID, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	out := &EventTagWriteResult{EventPatch: *p}
	for _, t := range p.Tags {
		if t.ID == tagID {
			out.Tag = t
		}
	}
	return out, nil
}

// ImportTags — POST /projects/:id/tags/import: a personal tag (same name and icon) for each
// listed event tag (none listed = all) I don't have by name yet.
func (s *Service) ImportTags(ctx context.Context, callerUserID, projectID uuid.UUID, req EventTagImportRequest) (*EventTagImportResult, error) {
	if _, err := s.store.GetByIDForCaller(ctx, callerUserID, projectID); err != nil {
		return nil, err
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	q := `SELECT et.name, et.icon_code FROM event_tags et WHERE et.project_id = $1
		AND NOT EXISTS (SELECT 1 FROM tags t WHERE t.user_id = $2 AND LOWER(t.name) = LOWER(et.name))`
	args := []any{projectID, callerUserID}
	if ids := uniqueIDs(req.TagIDs); len(ids) > 0 {
		var n int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM event_tags WHERE project_id = $1 AND id = ANY($2)`,
			projectID, ids).Scan(&n); err != nil {
			return nil, err
		}
		if n != len(ids) {
			return nil, ErrUnknownTag
		}
		args = append(args, ids)
		q += ` AND et.id = ANY($3)`
	}
	rows, err := tx.Query(ctx, q+` ORDER BY LOWER(et.name)`, args...)
	if err != nil {
		return nil, fmt.Errorf("tags to import: %w", err)
	}
	type src struct {
		name string
		icon []byte
	}
	var todo []src
	for rows.Next() {
		var t src
		if err := rows.Scan(&t.name, &t.icon); err != nil {
			rows.Close()
			return nil, err
		}
		todo = append(todo, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := &EventTagImportResult{Created: []shared.TagRef{}}
	for _, t := range todo {
		id, _ := uuid.NewV7()
		if _, err := tx.Exec(ctx, `INSERT INTO tags (id, user_id, name, icon_code, created_by_user_id, updated_by_user_id)
			VALUES ($1, $2, $3, $4::jsonb, $2, $2)`, id, callerUserID, t.name, t.icon); err != nil {
			return nil, fmt.Errorf("import tag %q: %w", t.name, err)
		}
		out.Created = append(out.Created, shared.TagRef{ID: id, Name: t.name, IconCode: shared.IconFromJSON(t.icon)})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	if out.Tags, err = loadEventTags(ctx, s.store.db, callerUserID, projectID); err != nil {
		return nil, err
	}
	return out, nil
}
