// Package providers — payment providers (migration 49): who a slip's money
// moved through. Banks for now (scheme "bot" = the Bank of Thailand's
// 3-digit bank code, as on a slip's QR); e-wallets and card issuers later.
// Read-only over the API; rows are added by migrations as unknown codes
// show up in import_logs.
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

// SchemeBOT — the Bank of Thailand's 3-digit bank codes.
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
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ListResponse struct {
	Data  []Provider `json:"data"`
	Count int        `json:"count"`
}

type Store struct{ db *pgxpool.Pool }

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// List — every provider, banks first, then by scheme + code.
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

// Known — is (scheme, code) in the table?
func (s *Store) Known(ctx context.Context, scheme, code string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM payment_providers WHERE scheme = $1 AND code = $2)`,
		scheme, code).Scan(&ok)
	return ok, err
}

type Handler struct{ store *Store }

func NewHandler(s *Store) *Handler { return &Handler{store: s} }

// GET /v1/payment-providers — for the wallet page's bank picker.
func (h *Handler) List(c *gin.Context) {
	list, err := h.store.List(c.Request.Context())
	if err != nil {
		response.InternalError(c, "Failed to list payment providers", nil)
		return
	}
	c.JSON(http.StatusOK, ListResponse{Data: list, Count: len(list)})
}
