package transactions

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Handler — the /transactions routes (API spec v2: bare bodies on success,
// response.Fail errors).
type Handler struct {
	service *Service
}

func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

// POST /v1/transactions — 201 TransactionWriteResult.
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

	res, err := h.service.CreateWrite(c.Request.Context(), userID, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, res)
}

// GET /v1/transactions — TransactionList.
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
	switch f.Sort {
	case "date_desc", "date_asc", "amount_desc", "amount_asc":
	default:
		response.BadRequest(c, "VALIDATION_ERROR", "sort must be date_desc, date_asc, amount_desc or amount_asc", nil)
		return
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
	f.IncludeChildren = c.Query("include_children") == "true"
	f.Uncategorized = c.Query("uncategorized") == "true"
	if v := strings.TrimSpace(c.Query("q")); v != "" {
		if utf8.RuneCountInString(v) > 100 {
			response.BadRequest(c, "VALIDATION_ERROR", "q must be at most 100 characters", nil)
			return
		}
		f.Q = v
	}
	for _, v := range c.QueryArray("tag_id") {
		id, err := uuid.Parse(v)
		if err != nil {
			response.BadRequest(c, "VALIDATION_ERROR", "Invalid tag_id", nil)
			return
		}
		f.TagIDs = append(f.TagIDs, id)
	}
	if v := c.Query("type"); v != "" {
		if v != "expense" && v != "income" && v != "transfer" {
			response.BadRequest(c, "VALIDATION_ERROR", "type must be 'expense', 'income', or 'transfer'", nil)
			return
		}
		t := TxType(v)
		f.Type = &t
	}
	for _, p := range []struct {
		key string
		dst **string
	}{{"from", &f.From}, {"to", &f.To}} {
		if v := c.Query(p.key); v != "" {
			if !validDate(v) {
				response.BadRequest(c, "VALIDATION_ERROR", p.key+" must be YYYY-MM-DD", nil)
				return
			}
			*p.dst = &v
		}
	}
	if c.Query("include_archived") == "true" {
		f.IncludeArchived = true
	}
	if c.Query("no_wallet") == "true" {
		f.NoWallet = true
	}
	f.CanJoinEvent = c.Query("can_join_event") == "true"
	if v := c.Query("counterparty_key"); v != "" {
		k, err := shared.ParseDebtKey(v)
		if err != nil {
			response.BadRequest(c, "INVALID_PERSON_KEY", err.Error(), nil)
			return
		}
		f.CounterpartyKey = k.Key()
	}

	resp, err := h.service.List(c.Request.Context(), userID, f)
	if err != nil {
		response.InternalError(c, "Failed to list transactions", err.Error())
		return
	}
	c.JSON(http.StatusOK, resp)
}

// GET /v1/transactions/:id — Transaction (splits filled).
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

// PUT /v1/transactions/:id — TransactionWriteResult (both rows for a
// transfer).
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

	res, err := h.service.UpdateWrite(c.Request.Context(), userID, id, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// DELETE /v1/transactions/:id — DeleteTransactionResult.
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

	res, err := h.service.DeleteWrite(c.Request.Context(), userID, id, c.Query("also_delete_link") == "true")
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// PUT /v1/transactions/:id/splits — replace the split list (see
// EditSplits); returns the Transaction.
func (h *Handler) EditSplits(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req EditSplitsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	out, err := h.service.EditSplits(c.Request.Context(), userID, id, *req.Splits)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// PUT /v1/transactions/:id/tags — replace the caller's tag set on the row;
// returns the Transaction.
func (h *Handler) SetTags(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req SetTagsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	out, err := h.service.SetTags(c.Request.Context(), userID, id, *req.TagIDs)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// GET /v1/transactions/form-options — FormOptions.
func (h *Handler) FormOptions(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	out, err := h.service.FormOptions(c.Request.Context(), userID)
	if err != nil {
		response.InternalError(c, "Failed to load form options", err.Error())
		return
	}
	c.JSON(http.StatusOK, out)
}

// GET /v1/transactions/summary — TransactionSummary.
func (h *Handler) Summary(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}

	from := c.Query("from")
	to := c.Query("to")
	if !validDate(from) || !validDate(to) {
		response.BadRequest(c, "VALIDATION_ERROR", "from and to are required (YYYY-MM-DD)", nil)
		return
	}

	req := SummaryRequest{From: from, To: to, GroupBy: c.Query("group_by")}
	switch req.GroupBy {
	case "", "day", "week", "month", "category", "parent_category", "account":
	default:
		response.BadRequest(c, "VALIDATION_ERROR",
			"group_by must be day, week, month, category, parent_category or account", nil)
		return
	}
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
	switch v := TxType(c.Query("type")); v {
	case "":
	case TypeIncome, TypeExpense:
		req.Type = &v
	default:
		response.BadRequest(c, "VALIDATION_ERROR", "type must be income or expense", nil)
		return
	}

	summary, err := h.service.Summary(c.Request.Context(), userID, req)
	if err != nil {
		response.InternalError(c, "Failed to compute summary", err.Error())
		return
	}
	c.JSON(http.StatusOK, summary)
}

// --- Helpers ---

func validDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func parseIDParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid transaction id", nil)
		return uuid.Nil, false
	}
	return id, true
}

// MapError — the transaction error codes, for modules whose writes edit
// transactions (a recurring item's link).
func MapError(c *gin.Context, err error) { mapServiceError(c, err) }

func mapServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrTxNotFound):
		response.NotFound(c, "NOT_FOUND", "Transaction not found")
	case errors.Is(err, ErrAccountArchived):
		response.Fail(c, http.StatusConflict, "ACCOUNT_ARCHIVED", err.Error(), nil)
	case errors.Is(err, ErrAccountForbidden):
		response.NotFound(c, "ACCOUNT_NOT_FOUND", "Account not found")
	case errors.Is(err, ErrCategoryForbidden):
		response.Fail(c, http.StatusForbidden, "FORBIDDEN", "Category not accessible", nil)
	case errors.Is(err, ErrCategoryTypeMismatch):
		response.BadRequest(c, "CATEGORY_TYPE_MISMATCH", err.Error(), nil)
	case errors.Is(err, ErrTagNotFound):
		response.BadRequest(c, "TAG_NOT_FOUND", "Tag not found", nil)
	case errors.Is(err, ErrSplitContactNotFound):
		response.BadRequest(c, "CONTACT_NOT_FOUND", "Split contact not found", nil)
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
		errors.Is(err, ErrTransferCategoryEdit),
		errors.Is(err, ErrTransferRequiresAccount):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	case errors.Is(err, ErrSystemTransactionImmutable):
		response.BadRequest(c, "SYSTEM_TRANSACTION_IMMUTABLE", err.Error(), nil)
	case errors.Is(err, ErrCategoryAuthorOnly):
		response.Fail(c, http.StatusForbidden, "CATEGORY_AUTHOR_ONLY", err.Error(), nil)
	case errors.Is(err, ErrFutureDateNotAllowed):
		response.BadRequest(c, "FUTURE_DATE_NOT_ALLOWED", err.Error(), nil)
	case errors.Is(err, ErrDebtTxAuthorOnly):
		response.Fail(c, http.StatusForbidden, "DEBT_TX_AUTHOR_ONLY", err.Error(), nil)
	case errors.Is(err, ErrDebtTxNeedsWallet):
		response.BadRequest(c, "DEBT_TX_NEEDS_WALLET", err.Error(), nil)
	case errors.Is(err, ErrRowLocked):
		response.Fail(c, http.StatusForbidden, "ROW_LOCKED", err.Error(), nil)
	case errors.Is(err, ErrSplitsOnTransfer):
		response.BadRequest(c, "SPLITS_ON_TRANSFER", err.Error(), nil)
	case errors.Is(err, ErrSplitsAuthorOnly):
		response.Fail(c, http.StatusForbidden, "SPLITS_AUTHOR_ONLY", err.Error(), nil)
	case errors.Is(err, ErrSplitsExceedAmount):
		response.BadRequest(c, "SPLITS_EXCEED_AMOUNT", err.Error(), nil)
	case errors.Is(err, ErrSplitUnknownDebt), errors.Is(err, ErrSplitPersonRequired):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	case errors.Is(err, ErrSplitContactArchived):
		response.Fail(c, http.StatusConflict, "CONTACT_ARCHIVED", err.Error(), nil)
	case errors.Is(err, ErrSplitsExceedShare):
		response.BadRequest(c, "SPLITS_EXCEED_SHARE", err.Error(), nil)
	case errors.Is(err, ErrAmountInvalid):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	default:
		response.InternalError(c, "Transaction operation failed", err.Error())
	}
}
