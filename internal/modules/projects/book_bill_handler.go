package projects

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

// BookBill — POST /event-bills (0.4.1-1 E8): the bill sheet's save in one call.
func (h *Handler) BookBill(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	var req EventBillBookRequest
	if !bindJSON(c, &req) {
		return
	}
	preview := c.Query("preview") == "true"
	out, err := h.service.BookBill(c.Request.Context(), userID, req, preview)
	if err != nil {
		mapBookBillError(c, err)
		return
	}
	status := http.StatusCreated
	if preview {
		status = http.StatusOK
	}
	c.JSON(status, out)
}

// RebookBill — PUT /event-bills/{pt_id}.
func (h *Handler) RebookBill(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	ptID, ok := pathID(c, "pt_id", "project transaction id")
	if !ok {
		return
	}
	var req EventBillRebookRequest
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.RebookBill(c.Request.Context(), userID, ptID, req, c.Query("preview") == "true")
	if err != nil {
		mapBookBillError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func mapBookBillError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrBookBillTarget), errors.Is(err, ErrBookBillKind), errors.Is(err, ErrBookBillInput):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	case errors.Is(err, personal_debts.ErrInvalidInput):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	case errors.Is(err, personal_debts.ErrContactNotFound):
		response.BadRequest(c, "CONTACT_NOT_FOUND", err.Error(), nil)
	default:
		mapQuickCreateError(c, err)
	}
}
