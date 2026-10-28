package projects

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
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

// billCall — the /event-bills/{pt_id} calls (0.4.1-2): the caller, the bill id, then fn's answer.
func (h *Handler) billCall(c *gin.Context, fn func(userID, ptID uuid.UUID) (any, error)) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	ptID, ok := pathID(c, "pt_id", "project transaction id")
	if !ok {
		return
	}
	out, err := fn(userID, ptID)
	if err != nil {
		mapBookBillError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// GetBill — GET /event-bills/{pt_id} (0.4.1-2 SA2).
func (h *Handler) GetBill(c *gin.Context) {
	h.billCall(c, func(userID, ptID uuid.UUID) (any, error) { return h.service.GetBill(c.Request.Context(), userID, ptID) })
}

// DeleteBill — DELETE /event-bills/{pt_id} (0.4.1-2 S13 / V13).
func (h *Handler) DeleteBill(c *gin.Context) {
	h.billCall(c, func(userID, ptID uuid.UUID) (any, error) {
		return h.service.DeleteBill(c.Request.Context(), userID, ptID, c.Query("keep_my_tx") == "true")
	})
}

// ApplyBill — POST /event-bills/{pt_id}/apply (0.4.1-2 S7).
func (h *Handler) ApplyBill(c *gin.Context) {
	h.billCall(c, func(userID, ptID uuid.UUID) (any, error) {
		return h.service.ApplyBill(c.Request.Context(), userID, ptID)
	})
}

// DisconnectBill — POST /event-bills/{pt_id}/disconnect (0.4.1-2).
func (h *Handler) DisconnectBill(c *gin.Context) {
	h.billCall(c, func(userID, ptID uuid.UUID) (any, error) {
		return h.service.DisconnectBill(c.Request.Context(), userID, ptID)
	})
}

func mapBookBillError(c *gin.Context, err error) {
	var changed *BillChangedError
	switch {
	case errors.As(err, &changed):
		response.Fail(c, http.StatusConflict, "BILL_CHANGED", err.Error(), gin.H{"bill": changed.Bill})
	case errors.Is(err, ErrBillDisconnected):
		response.Fail(c, http.StatusConflict, "BILL_DISCONNECTED", err.Error(), nil)
	case errors.Is(err, ErrBillGone):
		response.Fail(c, http.StatusGone, "BILL_GONE", err.Error(), nil)
	case errors.Is(err, ErrMoveTarget):
		response.BadRequest(c, "MOVE_TARGET", err.Error(), nil)
	case errors.Is(err, ErrPayersSum):
		response.BadRequest(c, "PAYERS_SUM", err.Error(), nil)
	case errors.Is(err, transactions.ErrPrivateNoteTooLong):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
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
