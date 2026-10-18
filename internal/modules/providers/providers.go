package providers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

const SchemeBOT = "bot"

type Provider struct {
	ID        uuid.UUID `json:"id"`
	Kind      string    `json:"kind"` // bank | e_wallet | card_issuer | other
	Scheme    string    `json:"scheme"`
	Code      string    `json:"code"`
	NameTH    string    `json:"name_th"`
	NameEN    string    `json:"name_en"`
	ShortName *string   `json:"short_name"`
	Color     *string   `json:"color"`
	CreatedAt time.Time `json:"-"`
	UpdatedAt time.Time `json:"-"`
}

type ListResponse struct {
	Data  []Provider `json:"data"`
	Count int        `json:"count"`
}

type Store struct{ db *pgxpool.Pool }

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

func (s *Store) List(ctx context.Context) ([]Provider, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, kind, scheme, code, name_th, name_en, short_name, color, created_at, updated_at
		FROM payment_providers
		ORDER BY (kind <> 'bank'), scheme, code`)
	if err != nil {
		return nil, fmt.Errorf("list payment providers: %w", err)
	}
	defer rows.Close()
	out := []Provider{}
	for rows.Next() {
		var p Provider
		if err := rows.Scan(&p.ID, &p.Kind, &p.Scheme, &p.Code, &p.NameTH, &p.NameEN,
			&p.ShortName, &p.Color, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Known(ctx context.Context, scheme, code string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM payment_providers WHERE scheme = $1 AND code = $2)`,
		scheme, code).Scan(&ok)
	return ok, err
}

type Handler struct{ store *Store }

func NewHandler(s *Store) *Handler { return &Handler{store: s} }

func (h *Handler) List(c *gin.Context) {
	list, err := h.store.List(c.Request.Context())
	if err != nil {
		response.InternalError(c, "Failed to list payment providers", nil)
		return
	}
	c.JSON(http.StatusOK, ListResponse{Data: list, Count: len(list)})
}
