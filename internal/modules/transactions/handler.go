package transactions

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

type Handler struct {
	service *Service
}

func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

// POST /v1/transactions
func (h *Handler) Create(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}

	var req CreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}

	res, err := h.service.Create(c.Request.Context(), userID, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, res)
}

// GET /v1/transactions
func (h *Handler) List(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}

	f := ListFilter{
		Page:    1,
		PerPage: 20,
		Sort:    c.DefaultQuery("sort", "date_desc"),
	}
	if v := c.Query("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Page = n
		}
	}
	if v := c.Query("per_page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.PerPage = n
		}
	}
	if v := c.Query("account_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			response.BadRequest(c, "VALIDATION_ERROR", "Invalid account_id", nil)
			return
		}
		f.AccountID = &id
	}
	if v := c.Query("category_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			response.BadRequest(c, "VALIDATION_ERROR", "Invalid category_id", nil)
			return
		}
		f.CategoryID = &id
	}
	if v := c.Query("type"); v != "" {
		if v != "expense" && v != "income" && v != "transfer" {
			response.BadRequest(c, "VALIDATION_ERROR", "type must be 'expense', 'income', or 'transfer'", nil)
			return
		}
		t := TxType(v)
		f.Type = &t
	}
	if v := c.Query("from"); v != "" {
		f.From = &v
	}
	if v := c.Query("to"); v != "" {
		f.To = &v
	}

	resp, err := h.service.List(c.Request.Context(), userID, f)
	if err != nil {
		response.InternalError(c, "Failed to list transactions", err.Error())
		return
	}
	c.JSON(http.StatusOK, resp)
}

// GET /v1/transactions/:id
func (h *Handler) Get(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	d, err := h.service.Get(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, d)
}

// PUT /v1/transactions/:id
func (h *Handler) Update(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	var req UpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}

	res, err := h.service.Update(c.Request.Context(), userID, id, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	// res is either *TransactionDetail (single) or *TransferResponse
	// (transfer pair). c.JSON marshals whichever we got — same
	// polymorphic dispatch as the create handler.
	c.JSON(http.StatusOK, res)
}

// DELETE /v1/transactions/:id
func (h *Handler) Delete(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	if err := h.service.Delete(c.Request.Context(), userID, id); err != nil {
		mapServiceError(c, err)
		return
	}
	response.OK(c, "Transaction deleted", nil)
}

// GET /v1/transactions/summary
func (h *Handler) Summary(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}

	from := c.Query("from")
	to := c.Query("to")
	if from == "" || to == "" {
		response.BadRequest(c, "VALIDATION_ERROR", "from and to are required (YYYY-MM-DD)", nil)
		return
	}

	req := SummaryRequest{From: from, To: to, GroupBy: c.Query("group_by")}
	if v := c.Query("account_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			response.BadRequest(c, "VALIDATION_ERROR", "Invalid account_id", nil)
			return
		}
		req.AccountID = &id
	}
	if v := c.Query("category_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			response.BadRequest(c, "VALIDATION_ERROR", "Invalid category_id", nil)
			return
		}
		req.CategoryID = &id
	}

	summary, err := h.service.Summary(c.Request.Context(), userID, req)
	if err != nil {
		response.InternalError(c, "Failed to compute summary", err.Error())
		return
	}
	c.JSON(http.StatusOK, summary)
}

// --- Helpers ---

func parseIDParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid transaction id", nil)
		return uuid.Nil, false
	}
	return id, true
}

func mapServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrTxNotFound):
		response.NotFound(c, "NOT_FOUND", "Transaction not found")
	case errors.Is(err, ErrAccountForbidden):
		response.NotFound(c, "ACCOUNT_NOT_FOUND", "Account not found")
	case errors.Is(err, ErrCategoryForbidden):
		response.Fail(c, http.StatusForbidden, "FORBIDDEN", "Category not accessible", nil)
	case errors.Is(err, ErrCategoryTypeMismatch):
		response.BadRequest(c, "CATEGORY_TYPE_MISMATCH", err.Error(), nil)
	case errors.Is(err, ErrTransferSameAccount):
		response.BadRequest(c, "TRANSFER_SAME_ACCOUNT", err.Error(), nil)
	case errors.Is(err, ErrTransferCurrencyMismatch):
		response.BadRequest(c, "TRANSFER_CURRENCY_MISMATCH", err.Error(), nil)
	case errors.Is(err, ErrSystemCategoryNotAllowed):
		response.BadRequest(c, "SYSTEM_CATEGORY_NOT_ALLOWED", err.Error(), nil)
	case errors.Is(err, ErrSplitsNotSupportedYet):
		response.BadRequest(c, "SPLITS_NOT_SUPPORTED_YET", err.Error(), nil)
	case errors.Is(err, ErrProjectIDNotAllowed):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	case errors.Is(err, ErrSourcePTNotFound):
		response.NotFound(c, "SOURCE_PT_NOT_FOUND", err.Error())
	case errors.Is(err, ErrSourcePTNotForUser):
		response.Fail(c, http.StatusForbidden, "SOURCE_PT_NOT_FOR_USER", err.Error(), nil)
	case errors.Is(err, ErrTransferToAccountRequired),
		errors.Is(err, ErrTransferFieldsOnNonTransfer),
		errors.Is(err, ErrCategoryRequiredForTransfer),
		errors.Is(err, ErrCategoryRequired),
		errors.Is(err, ErrTransferCategoryEdit):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	case errors.Is(err, ErrSystemTransactionImmutable):
		response.BadRequest(c, "SYSTEM_TRANSACTION_IMMUTABLE", err.Error(), nil)
	case errors.Is(err, ErrAmountInvalid):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	default:
		response.InternalError(c, "Transaction operation failed", err.Error())
	}
}
