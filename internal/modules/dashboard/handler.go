package dashboard

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

const headerTimezone = "X-Timezone"

type Handler struct {
	service *Service
}

func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

func (h *Handler) Get(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	out, err := h.service.Get(c.Request.Context(), userID, c.Query("month"), c.GetHeader(headerTimezone))
	if errors.Is(err, ErrInvalidMonth) {
		response.ValidationErr(c, err)
		return
	}
	if err != nil {
		response.InternalError(c, "Failed to build dashboard", err)
		return
	}
	c.JSON(http.StatusOK, out)
}
