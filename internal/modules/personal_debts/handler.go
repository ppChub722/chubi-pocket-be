package personal_debts

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
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

func caller(c *gin.Context) (uuid.UUID, bool) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
	}
	return userID, ok
}

func (h *Handler) People(c *gin.Context) {
	userID, ok := caller(c)
	if !ok {
		return
	}
	f := PeopleFilter{
		Q: c.Query("q"), All: c.Query("all") == "true", Side: c.Query("side"), Sort: c.Query("sort"),
		Page: atoiOr(c.Query("page"), 1), PerPage: atoiOr(c.Query("per_page"), 30),
	}
	switch f.Sort {
	case "", "oldest", "amount", "recent":
	default:
		response.Validation(c, "sort must be oldest, amount or recent", apierr.Field("sort", apierr.FieldInvalid))
		return
	}
	switch f.Side {
	case "", "owed_to_me", "i_owe", "receivable", "payable", "overdue_30":
	default:
		response.Validation(c, "side must be receivable, payable, overdue_30 (or owed_to_me / i_owe)", apierr.Field("side", apierr.FieldInvalid))
		return
	}
	out, err := h.service.People(c.Request.Context(), userID, f)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) Person(c *gin.Context) {
	userID, ok := caller(c)
	if !ok {
		return
	}
	out, err := h.service.Person(c.Request.Context(), userID, c.Param("key"), c.Query("cycle"),
		atoiOr(c.Query("page"), 1), atoiOr(c.Query("per_page"), 30))
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) Entries(c *gin.Context) {
	userID, ok := caller(c)
	if !ok {
		return
	}
	f := EntryFilter{
		Q: c.Query("q"), Sort: c.Query("sort"), Direction: c.Query("direction"),
		Money: c.Query("money") == "true", Cycle: c.Query("cycle"), Kind: c.Query("kind"),
		Page: atoiOr(c.Query("page"), 1), PerPage: atoiOr(c.Query("per_page"), 30),
	}
	switch f.Kind {
	case "", KindDebt, KindRepayment, "all":
	default:
		response.Validation(c, "kind must be debt, repayment or all", apierr.Field("kind", apierr.FieldInvalid))
		return
	}
	switch f.Cycle {
	case "", "current", "earlier", "all":
	default:
		response.Validation(c, "cycle must be current or earlier", apierr.Field("cycle", apierr.FieldInvalid))
		return
	}
	switch f.Sort {
	case "", "date_desc", "date_asc", "amount_desc", "amount_asc":
	default:
		response.Validation(c, "Invalid sort", apierr.Field("sort", apierr.FieldInvalid))
		return
	}
	if f.Direction != "" && f.Direction != DirectionPaid && f.Direction != DirectionReceived {
		response.Validation(c, "direction must be paid or received", apierr.Field("direction", apierr.FieldInvalid))
		return
	}
	if v := c.Query("paid"); v != "" {
		b := v == "true"
		f.Paid = &b
	}
	if v := c.Query("account_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			response.Validation(c, "Invalid account_id", apierr.Field("account_id", apierr.FieldInvalid))
			return
		}
		f.AccountID = &id
	}
	for _, p := range []struct {
		key string
		dst **string
	}{{"from", &f.From}, {"to", &f.To}} {
		if v := c.Query(p.key); v != "" {
			if !validDate(v) {
				response.Validation(c, "Invalid "+p.key, apierr.Field(p.key, apierr.FieldInvalid))
				return
			}
			vv := v
			*p.dst = &vv
		}
	}
	out, err := h.service.Entries(c.Request.Context(), userID, c.Param("key"), f)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) CreateEntry(c *gin.Context) {
	userID, ok := caller(c)
	if !ok {
		return
	}
	var req CreateEntryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}
	if c.Query("preview") == "true" {
		p, err := h.service.PreviewEntry(c.Request.Context(), userID, c.Param("key"), req)
		if err != nil {
			mapServiceError(c, err)
			return
		}
		c.JSON(http.StatusOK, p)
		return
	}
	out, err := h.service.CreateEntry(c.Request.Context(), userID, c.Param("key"), req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

// CreateRepayment — POST /personal-debts/people/:key/repayments.
func (h *Handler) CreateRepayment(c *gin.Context) {
	userID, ok := caller(c)
	if !ok {
		return
	}
	var req RepaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}
	if c.Query("preview") == "true" {
		p, err := h.service.PreviewRepayment(c.Request.Context(), userID, c.Param("key"), req)
		if err != nil {
			mapServiceError(c, err)
			return
		}
		c.JSON(http.StatusOK, p)
		return
	}
	out, err := h.service.CreateRepayment(c.Request.Context(), userID, c.Param("key"), req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

func (h *Handler) Get(c *gin.Context) {
	userID, ok := caller(c)
	if !ok {
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	out, err := h.service.GetEntry(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) Update(c *gin.Context) {
	userID, ok := caller(c)
	if !ok {
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req UpdateEntryRequest
	if err := c.ShouldBindBodyWith(&req, binding.JSON); err != nil {
		response.BindError(c, err)
		return
	}
	out, err := h.service.UpdateEntry(c.Request.Context(), userID, id, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// LinkTx — POST /personal-debts/{id}/link: the entry points at one of my existing transactions.
func (h *Handler) LinkTx(c *gin.Context) {
	userID, ok := caller(c)
	if !ok {
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req LinkTxRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}
	out, err := h.service.LinkTx(c.Request.Context(), userID, id, req.TransactionID)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// Disconnect — POST /personal-debts/{id}/disconnect (0.4.1-1 D9): my copy of a cross-user pair.
func (h *Handler) Disconnect(c *gin.Context) {
	userID, ok := caller(c)
	if !ok {
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	out, err := h.service.Disconnect(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// UnlinkTx — POST /personal-debts/{id}/unlink: disconnects a connected transaction (D2).
func (h *Handler) UnlinkTx(c *gin.Context) {
	userID, ok := caller(c)
	if !ok {
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	out, err := h.service.UnlinkTx(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) Delete(c *gin.Context) {
	userID, ok := caller(c)
	if !ok {
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	out, err := h.service.DeleteEntry(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func parseIDParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.Validation(c, "Invalid entry id", apierr.Field("id", apierr.FieldInvalid))
		return uuid.Nil, false
	}
	return id, true
}

func validDate(s string) bool {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	_, err1 := strconv.Atoi(s[:4])
	_, err2 := strconv.Atoi(s[5:7])
	_, err3 := strconv.Atoi(s[8:])
	return err1 == nil && err2 == nil && err3 == nil
}

func mapServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrEntryNotFound):
		response.NotFound(c, apierr.NotFound, "Debt entry not found")
	case errors.Is(err, ErrPersonNotFound):
		response.NotFound(c, apierr.NotFound, err.Error())
	case errors.Is(err, shared.ErrInvalidPersonKey):
		response.BadRequest(c, apierr.InvalidPersonKey, err.Error(), nil)
	case errors.Is(err, ErrContactNotFound):
		response.BadRequest(c, apierr.ContactNotFound, "Contact not found", nil)
	case errors.Is(err, ErrTagsNeedWallet):
		response.BadRequest(c, apierr.TagsNeedWallet, err.Error(), nil)
	case errors.Is(err, ErrDueBeforeDate):
		response.ValidationErr(c, err)
	case errors.Is(err, ErrLedgerOnlyNoWallet):
		response.BadRequest(c, apierr.LedgerOnlyNoWallet, err.Error(), nil)
	case errors.Is(err, ErrMarkPaidNotFound):
		response.BadRequest(c, apierr.EntryNotFound, err.Error(), nil)
	case errors.Is(err, ErrManagedBySource):
		response.Fail(c, http.StatusConflict, apierr.EntryManagedBySource, err.Error(), nil)
	case errors.As(err, new(*transactions.SharedBillFieldError)):
		var f *transactions.SharedBillFieldError
		errors.As(err, &f)
		response.Fail(c, http.StatusConflict, apierr.SharedBillField, err.Error(), map[string]any{"fields": f.Fields})
	case errors.Is(err, transactions.ErrPrivateNoteNotShared):
		response.BadRequest(c, apierr.PrivateNoteNotShared, err.Error(), nil)
	case errors.Is(err, transactions.ErrPrivateNoteTooLong):
		response.ValidationErr(c, err)
	case errors.Is(err, ErrBillNotFound):
		response.BadRequest(c, apierr.BillNotFound, err.Error(), nil)
	case errors.Is(err, ErrUseClaim):
		response.BadRequest(c, apierr.UseClaim, err.Error(), nil)
	case errors.Is(err, ErrBillAlreadyBooked):
		response.Fail(c, http.StatusConflict, apierr.BillAlreadyBooked, err.Error(), nil)
	case errors.Is(err, ErrManagedByTx):
		response.Fail(c, http.StatusConflict, apierr.DebtManagedByTx, err.Error(), nil)
	case errors.Is(err, transactions.ErrTxNotFound):
		response.NotFound(c, apierr.TransactionNotFound, "Transaction not found")
	case errors.Is(err, ErrTxNotLinkable):
		response.BadRequest(c, apierr.TxNotLinkable, err.Error(), nil)
	case errors.Is(err, ErrTxLinkedElsewhere):
		response.Fail(c, http.StatusConflict, apierr.DebtTxAlreadyLinked, err.Error(), nil)
	case errors.Is(err, ErrEntryHasTx):
		response.Fail(c, http.StatusConflict, apierr.EntryHasTransaction, err.Error(), nil)
	case errors.Is(err, ErrTxNotDisconnectable):
		response.Fail(c, http.StatusConflict, apierr.TxNotDisconnectable, err.Error(), nil)
	case errors.Is(err, ErrDebtNotLinked):
		response.Fail(c, http.StatusConflict, apierr.DebtNotLinked, err.Error(), nil)
	case errors.Is(err, ErrInvalidInput):
		response.ValidationErr(c, err)
	case errors.Is(err, transactions.ErrAccountArchived):
		response.Fail(c, http.StatusConflict, apierr.AccountArchived, err.Error(), nil)
	case errors.Is(err, transactions.ErrAccountForbidden):
		response.BadRequest(c, apierr.AccountNotFound, "Account not found", nil)
	case errors.Is(err, transactions.ErrTagNotFound):
		response.BadRequest(c, apierr.TagNotFound, "Tag not found", nil)
	default:
		response.InternalError(c, "Debt operation failed", err)
	}
}

func atoiOr(s string, dflt int) int {
	if s == "" {
		return dflt
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return dflt
	}
	return n
}
