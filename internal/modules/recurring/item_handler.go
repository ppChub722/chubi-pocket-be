package recurring

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

// Routes of the recurring ITEMS (0.4.3): /recurring/{id} = the item, /recurring/{id}/reminders/{reminder_id}
// = one of its lines, rounds addressed by line + slot.

func bindItem(c *gin.Context) (RecurringInput, bool) {
	var in RecurringInput
	keys, ok := bind(c, &in)
	if !ok {
		return in, false
	}
	in.present = map[string]bool{}
	for k := range keys {
		in.present[k] = true
	}
	return in, true
}

func (h *Handler) ListRecurring(c *gin.Context) {
	uid, ok := user(c)
	if !ok {
		return
	}
	status := c.DefaultQuery("status", "current")
	if status != "current" && status != "archived" && status != "all" {
		response.Validation(c, "Invalid status", apierr.Field("status", apierr.FieldInvalid))
		return
	}
	typ := c.Query("type")
	if typ != "" && !validItemType(typ) {
		response.Validation(c, "Invalid type", apierr.Field("type", apierr.FieldInvalid))
		return
	}
	forType := c.Query("for_type")
	if forType != "" && forType != "expense" && forType != "income" && forType != "transfer" {
		response.Validation(c, "for_type must be expense, income or transfer", apierr.Field("for_type", apierr.FieldInvalid))
		return
	}
	var hint PickerHint
	if id, ok := optionalID(c, "category_id"); !ok {
		return
	} else {
		hint.CategoryID = id
	}
	if v := c.Query("amount"); v != "" {
		a, err := strconv.ParseFloat(v, 64)
		if err != nil || a <= 0 {
			response.Validation(c, "amount must be a positive number", apierr.Field("amount", apierr.FieldInvalid))
			return
		}
		hint.Amount = &a
	}
	out, err := h.service.ListRecurring(c.Request.Context(), uid, status, typ, c.Query("q"), forType, hint,
		atoiOr(c.Query("page"), 1), atoiOr(c.Query("per_page"), 20), tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) CreateRecurring(c *gin.Context) {
	uid, ok := user(c)
	if !ok {
		return
	}
	in, ok := bindItem(c)
	if !ok {
		return
	}
	out, err := h.service.CreateRecurring(c.Request.Context(), uid, in, tz(c))
	reply(c, http.StatusCreated, out, err)
}

func (h *Handler) GetRecurring(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.GetRecurring(c.Request.Context(), uid, id, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) UpdateRecurring(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	in, ok := bindItem(c)
	if !ok {
		return
	}
	out, err := h.service.UpdateRecurring(c.Request.Context(), uid, id, in, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) DeleteRecurring(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.DeleteRecurring(c.Request.Context(), uid, id)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) itemOp(c *gin.Context, op func(uid, id uuid.UUID) (*ItemPage, error)) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := op(uid, id)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) PauseRecurring(c *gin.Context) {
	h.itemOp(c, func(u, id uuid.UUID) (*ItemPage, error) {
		return h.service.PauseRecurring(c.Request.Context(), u, id, tz(c))
	})
}

func (h *Handler) ResumeRecurring(c *gin.Context) {
	h.itemOp(c, func(u, id uuid.UUID) (*ItemPage, error) {
		return h.service.ResumeRecurring(c.Request.Context(), u, id, tz(c))
	})
}

func (h *Handler) ArchiveRecurring(c *gin.Context) {
	h.itemOp(c, func(u, id uuid.UUID) (*ItemPage, error) {
		return h.service.ArchiveRecurring(c.Request.Context(), u, id, tz(c))
	})
}

func (h *Handler) RestoreRecurring(c *gin.Context) {
	h.itemOp(c, func(u, id uuid.UUID) (*ItemPage, error) {
		return h.service.RestoreRecurring(c.Request.Context(), u, id, tz(c))
	})
}

func (h *Handler) DuplicateRecurring(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.DuplicateRecurring(c.Request.Context(), uid, id, tz(c))
	reply(c, http.StatusCreated, out, err)
}

func (h *Handler) ItemRounds(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	tab := c.DefaultQuery("tab", "produced")
	if tab != "produced" && tab != "upcoming" {
		response.Validation(c, "Invalid tab", apierr.Field("tab", apierr.FieldInvalid))
		return
	}
	from, ok := optionalDate(c, "from")
	if !ok {
		return
	}
	out, err := h.service.ItemRounds(c.Request.Context(), uid, id, tab, atoiOr(c.Query("page"), 1), atoiOr(c.Query("per_page"), 20),
		from, atoiOr(c.Query("months"), 12), tz(c))
	reply(c, http.StatusOK, out, err)
}

func optionalDate(c *gin.Context, name string) (*time.Time, bool) {
	v := c.Query(name)
	if v == "" {
		return nil, true
	}
	d, err := time.Parse(dateLayout, v)
	if err != nil {
		response.Validation(c, name+" must be YYYY-MM-DD", apierr.Field(name, apierr.FieldInvalid, "format", "date"))
		return nil, false
	}
	return &d, true
}

// ---- lines ----

func itemAndLine(c *gin.Context) (uuid.UUID, uuid.UUID, uuid.UUID, bool) {
	uid, id, ok := userAndID(c)
	if !ok {
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}
	line, err := uuid.Parse(c.Param("reminder_id"))
	if err != nil {
		response.Validation(c, "Invalid reminder_id", apierr.Field("reminder_id", apierr.FieldInvalid))
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}
	return uid, id, line, true
}

func (h *Handler) AddLine(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	var in LineInput
	if _, ok := bind(c, &in); !ok {
		return
	}
	out, err := h.service.AddLine(c.Request.Context(), uid, id, in, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) UpdateLine(c *gin.Context) {
	uid, id, line, ok := itemAndLine(c)
	if !ok {
		return
	}
	var in LineInput
	if _, ok := bind(c, &in); !ok {
		return
	}
	out, err := h.service.UpdateLine(c.Request.Context(), uid, id, line, in, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) DeleteLine(c *gin.Context) {
	uid, id, line, ok := itemAndLine(c)
	if !ok {
		return
	}
	out, err := h.service.DeleteLine(c.Request.Context(), uid, id, line, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) lineOp(c *gin.Context, op func(uid, id, line uuid.UUID) (*ItemPage, error)) {
	uid, id, line, ok := itemAndLine(c)
	if !ok {
		return
	}
	out, err := op(uid, id, line)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) PauseLine(c *gin.Context) {
	h.lineOp(c, func(u, id, l uuid.UUID) (*ItemPage, error) {
		return h.service.PauseLine(c.Request.Context(), u, id, l, tz(c))
	})
}

func (h *Handler) ResumeLine(c *gin.Context) {
	h.lineOp(c, func(u, id, l uuid.UUID) (*ItemPage, error) {
		return h.service.ResumeLine(c.Request.Context(), u, id, l, tz(c))
	})
}

func (h *Handler) ArchiveLine(c *gin.Context) {
	h.lineOp(c, func(u, id, l uuid.UUID) (*ItemPage, error) {
		return h.service.ArchiveLine(c.Request.Context(), u, id, l, tz(c))
	})
}

func (h *Handler) RestoreLine(c *gin.Context) {
	h.lineOp(c, func(u, id, l uuid.UUID) (*ItemPage, error) {
		return h.service.RestoreLine(c.Request.Context(), u, id, l, tz(c))
	})
}

func (h *Handler) ExtendLine(c *gin.Context) {
	uid, id, line, ok := itemAndLine(c)
	if !ok {
		return
	}
	var req ExtendRequest
	if _, ok := bind(c, &req); !ok {
		return
	}
	out, err := h.service.ExtendLine(c.Request.Context(), uid, id, line, req, tz(c))
	reply(c, http.StatusOK, out, err)
}

// ---- rounds ----

func (h *Handler) ItemOccurrences(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	from, ok := optionalDate(c, "from")
	if !ok {
		return
	}
	to, ok := optionalDate(c, "to")
	if !ok {
		return
	}
	txDate, ok := optionalDate(c, "tx_date")
	if !ok {
		return
	}
	out, err := h.service.ItemOccurrences(c.Request.Context(), uid, id, from, to, txDate,
		atoiOr(c.Query("past"), 3), atoiOr(c.Query("upcoming"), 3), tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) itemBulkOp(c *gin.Context, op func(uid, id uuid.UUID) (*BulkResult, error)) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := op(uid, id)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) ConfirmReadyItem(c *gin.Context) {
	h.itemBulkOp(c, func(u, id uuid.UUID) (*BulkResult, error) {
		return h.service.ConfirmReadyItem(c.Request.Context(), u, id, tz(c))
	})
}

func (h *Handler) MarkPaidReadyItem(c *gin.Context) {
	h.itemBulkOp(c, func(u, id uuid.UUID) (*BulkResult, error) {
		return h.service.MarkPaidReadyItem(c.Request.Context(), u, id, tz(c))
	})
}

func (h *Handler) SkipReadyItem(c *gin.Context) {
	h.itemBulkOp(c, func(u, id uuid.UUID) (*BulkResult, error) {
		return h.service.SkipReadyItem(c.Request.Context(), u, id, tz(c))
	})
}

// ---- plan / installments ----

func (h *Handler) PlanPreview(c *gin.Context) {
	if _, ok := user(c); !ok {
		return
	}
	var in PlanInput
	if _, ok := bind(c, &in); !ok {
		return
	}
	out, err := h.service.PlanPreview(in)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Installments(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.Installments(c.Request.Context(), uid, id, tz(c))
	reply(c, http.StatusOK, out, err)
}

func installmentSeq(c *gin.Context) (uuid.UUID, uuid.UUID, int, bool) {
	uid, id, ok := userAndID(c)
	if !ok {
		return uuid.Nil, uuid.Nil, 0, false
	}
	seq, err := strconv.Atoi(strings.TrimSpace(c.Param("seq")))
	if err != nil || seq < 1 {
		response.Validation(c, "Invalid seq", apierr.Field("seq", apierr.FieldInvalid))
		return uuid.Nil, uuid.Nil, 0, false
	}
	return uid, id, seq, true
}

func (h *Handler) EditInstallment(c *gin.Context) {
	uid, id, seq, ok := installmentSeq(c)
	if !ok {
		return
	}
	var in InstallmentEdit
	if _, ok := bind(c, &in); !ok {
		return
	}
	out, err := h.service.EditInstallment(c.Request.Context(), uid, id, seq, in, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) MarkInstallment(c *gin.Context) {
	uid, id, seq, ok := installmentSeq(c)
	if !ok {
		return
	}
	var in MarkInstallmentRequest
	if _, ok := bind(c, &in); !ok {
		return
	}
	out, err := h.service.MarkInstallment(c.Request.Context(), uid, id, seq, in, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) ClosePlan(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.ClosePlan(c.Request.Context(), uid, id, tz(c))
	reply(c, http.StatusOK, out, err)
}

// ---- debt plans (0.4.3: /debt-plans) ----

func (h *Handler) CreateDebtPlan(c *gin.Context) {
	uid, ok := user(c)
	if !ok {
		return
	}
	var in DebtPlanInput
	if _, ok := bind(c, &in); !ok {
		return
	}
	out, err := h.service.CreateDebtPlan(c.Request.Context(), uid, in, tz(c))
	reply(c, http.StatusCreated, out, err)
}

func (h *Handler) ListDebtPlans(c *gin.Context) {
	uid, ok := user(c)
	if !ok {
		return
	}
	var acc *uuid.UUID
	if v := strings.TrimSpace(c.Query("account_id")); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			response.Validation(c, "Invalid account_id", apierr.Field("account_id", apierr.FieldInvalid))
			return
		}
		acc = &id
	}
	out, err := h.service.ListDebtPlans(c.Request.Context(), uid, acc, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) GetDebtPlan(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.GetDebtPlan(c.Request.Context(), uid, id, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) UpdateDebtPlan(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	var in DebtPlanUpdate
	if _, ok := bind(c, &in); !ok {
		return
	}
	out, err := h.service.UpdateDebtPlan(c.Request.Context(), uid, id, in, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) DeleteDebtPlan(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	if err := h.service.DeleteDebtPlan(c.Request.Context(), uid, id); err != nil {
		mapError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) PayInstallment(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	var in PayInstallmentRequest
	if _, ok := bind(c, &in); !ok {
		return
	}
	out, err := h.service.PayInstallment(c.Request.Context(), uid, id, in, tz(c))
	reply(c, http.StatusOK, out, err)
}

// ---- card summaries (0.4.3) ----

func (h *Handler) CardSummaries(c *gin.Context) {
	uid, ok := user(c)
	if !ok {
		return
	}
	out, err := h.service.CardSummaries(c.Request.Context(), uid, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) CardSummary(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.CardSummaryOf(c.Request.Context(), uid, id, tz(c))
	reply(c, http.StatusOK, out, err)
}
