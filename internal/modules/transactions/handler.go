package transactions

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type Handler struct {
	service *Service
}

func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

func (h *Handler) Create(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}

	var req CreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}

	res, err := h.service.CreateWrite(shared.WithHeaderTZ(c.Request.Context(), c.GetHeader("X-Timezone")), userID, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, res)
}

func (h *Handler) List(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
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
		response.Validation(c, "sort must be date_desc, date_asc, amount_desc or amount_asc", apierr.Field("sort", apierr.FieldInvalid))
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
	for _, v := range c.QueryArray("account_id") {
		id, err := uuid.Parse(v)
		if err != nil {
			response.Validation(c, "Invalid account_id", apierr.Field("account_id", apierr.FieldInvalid))
			return
		}
		f.AccountIDs = append(f.AccountIDs, id)
	}
	for _, v := range c.QueryArray("category_id") {
		id, err := uuid.Parse(v)
		if err != nil {
			response.Validation(c, "Invalid category_id", apierr.Field("category_id", apierr.FieldInvalid))
			return
		}
		f.CategoryIDs = append(f.CategoryIDs, id)
	}
	f.NoTags = c.Query("no_tags") == "true"
	f.IncludeChildren = c.Query("include_children") == "true"
	f.Uncategorized = c.Query("uncategorized") == "true"
	if v := strings.TrimSpace(c.Query("q")); v != "" {
		if utf8.RuneCountInString(v) > 100 {
			response.Validation(c, "q must be at most 100 characters", apierr.Field("q", apierr.FieldInvalid))
			return
		}
		f.Q = v
	}
	for _, v := range c.QueryArray("tag_id") {
		id, err := uuid.Parse(v)
		if err != nil {
			response.Validation(c, "Invalid tag_id", apierr.Field("tag_id", apierr.FieldInvalid))
			return
		}
		f.TagIDs = append(f.TagIDs, id)
	}
	for _, v := range c.QueryArray("type") {
		if v != "expense" && v != "income" && v != "transfer" {
			response.Validation(c, "type must be 'expense', 'income', or 'transfer'", apierr.Field("type", apierr.FieldInvalid))
			return
		}
		f.Types = append(f.Types, TxType(v))
	}
	for _, p := range []struct {
		key string
		dst **string
	}{{"from", &f.From}, {"to", &f.To}} {
		if v := c.Query(p.key); v != "" {
			if !validDate(v) {
				response.Validation(c, p.key+" must be YYYY-MM-DD", apierr.Field(p.key, apierr.FieldInvalid, "format", "date"))
				return
			}
			*p.dst = &v
		}
	}
	for _, p := range []struct {
		key string
		dst **float64
	}{{"amount_min", &f.AmountMin}, {"amount_max", &f.AmountMax}} {
		if v := c.Query(p.key); v != "" {
			n, err := strconv.ParseFloat(v, 64)
			if err != nil || n < 0 || math.IsNaN(n) || math.IsInf(n, 0) {
				response.Validation(c, p.key+" must be a non-negative number", apierr.Field(p.key, apierr.FieldOutOfRange, "min", 0))
				return
			}
			*p.dst = &n
		}
	}
	if f.AmountMin != nil && f.AmountMax != nil && *f.AmountMin > *f.AmountMax {
		response.Validation(c, "amount_min must be at most amount_max", apierr.Field("amount_min", apierr.FieldInvalid))
		return
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
			response.BadRequest(c, apierr.InvalidPersonKey, err.Error(), nil)
			return
		}
		f.CounterpartyKey = k.Key()
	}

	f.TZ = c.GetHeader("X-Timezone")
	resp, err := h.service.List(c.Request.Context(), userID, f)
	if err != nil {
		response.InternalError(c, "Failed to list transactions", err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) Get(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
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

func (h *Handler) Update(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	var req UpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}

	ctx := shared.WithHeaderTZ(c.Request.Context(), c.GetHeader("X-Timezone"))
	if c.Query("preview") == "true" {
		p, err := h.service.Preview(ctx, userID, id, req)
		if err != nil {
			mapServiceError(c, err)
			return
		}
		c.JSON(http.StatusOK, p)
		return
	}
	res, err := h.service.UpdateWrite(ctx, userID, id, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

func (h *Handler) Delete(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
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

func (h *Handler) EditSplits(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req EditSplitsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}
	out, err := h.service.EditSplits(c.Request.Context(), userID, id, *req.Splits)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) SetTags(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req SetTagsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}
	out, err := h.service.SetTags(c.Request.Context(), userID, id, *req.TagIDs)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) FormOptions(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	out, err := h.service.FormOptions(c.Request.Context(), userID)
	if err != nil {
		response.InternalError(c, "Failed to load form options", err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) Summary(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}

	from := c.Query("from")
	to := c.Query("to")
	if !validDate(from) || !validDate(to) {
		var fields []apierr.FieldError
		for _, q := range []struct{ name, v string }{{"from", from}, {"to", to}} {
			if !validDate(q.v) {
				fields = append(fields, apierr.Field(q.name, apierr.FieldInvalid, "format", "date"))
			}
		}
		response.Validation(c, "from and to are required (YYYY-MM-DD)", fields...)
		return
	}

	req := SummaryRequest{From: from, To: to, GroupBy: c.Query("group_by")}
	switch req.GroupBy {
	case "", "day", "week", "month", "category", "parent_category", "account":
	default:
		response.Validation(c, "group_by must be day, week, month, category, parent_category or account", apierr.Field("group_by", apierr.FieldInvalid))
		return
	}
	if v := c.Query("account_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			response.Validation(c, "Invalid account_id", apierr.Field("account_id", apierr.FieldInvalid))
			return
		}
		req.AccountID = &id
	}
	if v := c.Query("category_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			response.Validation(c, "Invalid category_id", apierr.Field("category_id", apierr.FieldInvalid))
			return
		}
		req.CategoryID = &id
	}
	switch v := TxType(c.Query("type")); v {
	case "":
	case TypeIncome, TypeExpense:
		req.Type = &v
	default:
		response.Validation(c, "type must be income or expense", apierr.Field("type", apierr.FieldInvalid))
		return
	}

	summary, err := h.service.Summary(c.Request.Context(), userID, req)
	if err != nil {
		response.InternalError(c, "Failed to compute summary", err)
		return
	}
	c.JSON(http.StatusOK, summary)
}

func validDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func parseIDParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.Validation(c, "Invalid transaction id", apierr.Field("id", apierr.FieldInvalid))
		return uuid.Nil, false
	}
	return id, true
}

func MapError(c *gin.Context, err error) { mapServiceError(c, err) }

func mapServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrTxNotFound):
		response.NotFound(c, apierr.NotFound, "Transaction not found")
	case errors.Is(err, ErrAccountArchived):
		response.Fail(c, http.StatusConflict, apierr.AccountArchived, err.Error(), nil)
	case errors.Is(err, ErrAccountForbidden):
		response.NotFound(c, apierr.AccountNotFound, "Account not found")
	case errors.Is(err, ErrCategoryForbidden):
		response.Fail(c, http.StatusForbidden, apierr.Forbidden, "Category not accessible", nil)
	case errors.Is(err, ErrCategoryTypeMismatch):
		response.BadRequest(c, apierr.CategoryTypeMismatch, err.Error(), nil)
	case errors.Is(err, ErrTagNotFound):
		response.BadRequest(c, apierr.TagNotFound, "Tag not found", nil)
	case errors.Is(err, ErrSplitContactNotFound):
		response.BadRequest(c, apierr.ContactNotFound, "Split contact not found", nil)
	case errors.Is(err, ErrTransferSameAccount):
		response.BadRequest(c, apierr.TransferSameAccount, err.Error(), nil)
	case errors.Is(err, ErrSystemCategoryNotAllowed):
		response.BadRequest(c, apierr.SystemCategoryNotAllowed, err.Error(), nil)
	case errors.Is(err, ErrSplitsNotSupportedYet):
		response.BadRequest(c, apierr.SplitsNotSupportedYet, err.Error(), nil)
	case errors.Is(err, ErrProjectIDNotAllowed):
		response.ValidationErr(c, err)
	case errors.Is(err, ErrSourcePTNotFound):
		response.NotFound(c, apierr.SourcePtNotFound, err.Error())
	case errors.Is(err, ErrSourcePTNotForUser):
		response.Fail(c, http.StatusForbidden, apierr.SourcePtNotForUser, err.Error(), nil)
	case errors.Is(err, ErrTransferToAccountRequired),
		errors.Is(err, ErrTransferFieldsOnNonTransfer),
		errors.Is(err, ErrCategoryRequiredForTransfer),
		errors.Is(err, ErrTransferCategoryEdit),
		errors.Is(err, ErrTransferRequiresAccount):
		response.ValidationErr(c, err)
	case errors.Is(err, ErrSystemTransactionImmutable):
		response.BadRequest(c, apierr.SystemTransactionImmutable, err.Error(), nil)
	case errors.Is(err, ErrCategoryAuthorOnly):
		response.Fail(c, http.StatusForbidden, apierr.CategoryAuthorOnly, err.Error(), nil)
	case errors.Is(err, ErrDebtTxAuthorOnly):
		response.Fail(c, http.StatusForbidden, apierr.DebtTxAuthorOnly, err.Error(), nil)
	case errors.Is(err, ErrDebtTxNeedsWallet):
		response.BadRequest(c, apierr.DebtTxNeedsWallet, err.Error(), nil)
	case errors.Is(err, ErrRowLocked):
		response.Fail(c, http.StatusForbidden, apierr.RowLocked, err.Error(), nil)
	case errors.Is(err, ErrSplitsOnTransfer):
		response.BadRequest(c, apierr.SplitsOnTransfer, err.Error(), nil)
	case errors.Is(err, ErrSplitsAuthorOnly):
		response.Fail(c, http.StatusForbidden, apierr.SplitsAuthorOnly, err.Error(), nil)
	case errors.Is(err, ErrSplitsExceedAmount):
		response.BadRequest(c, apierr.SplitsExceedAmount, err.Error(), nil)
	case errors.Is(err, ErrSplitUnknownDebt), errors.Is(err, ErrSplitPersonRequired):
		response.ValidationErr(c, err)
	case errors.Is(err, ErrSplitsExceedShare):
		response.BadRequest(c, apierr.SplitsExceedShare, err.Error(), nil)
	case errors.Is(err, ErrAmountInvalid), errors.Is(err, ErrPaidByPersonRequired):
		response.ValidationErr(c, err)
	case errors.Is(err, ErrPaidByExpenseOnly):
		response.BadRequest(c, apierr.PaidByExpenseOnly, err.Error(), nil)
	case errors.Is(err, ErrPaidByExceedsAmount):
		response.BadRequest(c, apierr.PaidByExceedsAmount, err.Error(), nil)
	case errors.Is(err, ErrLinkModeConflict):
		response.BadRequest(c, apierr.LinkModeConflict, err.Error(), nil)
	case errors.Is(err, ErrLinkModeInput):
		response.BadRequest(c, apierr.LinkModeInput, err.Error(), nil)
	case errors.As(err, new(*SharedBillFieldError)):
		var f *SharedBillFieldError
		errors.As(err, &f)
		response.Fail(c, http.StatusConflict, apierr.SharedBillField, err.Error(), map[string]any{"fields": f.Fields})
	case errors.Is(err, ErrPrivateNoteNotShared):
		response.BadRequest(c, apierr.PrivateNoteNotShared, err.Error(), nil)
	case errors.Is(err, ErrPrivateNoteTooLong):
		response.ValidationErr(c, err)
	default:
		response.InternalError(c, "Transaction operation failed", err)
	}
}
