package recurring

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Duplicate — POST /recurring/:id/duplicate: a new active setting with the
// same values (name + " (สำเนา)"), starting today, the same end; its items
// are generated like a new setting's. Answers with the new setting's page.
func (s *Service) Duplicate(ctx context.Context, userID, id uuid.UUID, tz string) (*Detail, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	src, err := getRule(ctx, tx, userID, id, false)
	if err != nil {
		return nil, err
	}
	cp := *src
	cp.ID = uuid.Nil
	cp.Status = "active"
	cp.GeneratedUntil, cp.EndingNotified, cp.TotalCount = nil, false, nil
	cp.StartDate = today
	cp.Name = copyName(src.Name)
	cp.Weekdays = append([]int(nil), src.Weekdays...)
	cp.TagIDs = append([]uuid.UUID(nil), src.TagIDs...)
	// "dates" settings keep only the dates still to come.
	if cp.Freq == "dates" {
		var keep []string
		cp.Dates = nil
		for _, d := range src.Dates {
			if !d.Before(today) {
				cp.Dates = append(cp.Dates, d)
				keep = append(keep, d.Format(dateLayout))
			}
		}
		if len(keep) == 0 {
			return nil, ErrValidation
		}
	}
	if err := cp.limits(today); err != nil {
		return nil, err
	}
	if err := checkRuleRefsTx(ctx, tx, userID, &cp); err != nil {
		return nil, err
	}
	if err := saveRuleTx(ctx, tx, &cp); err != nil {
		return nil, err
	}
	if err := s.generateTx(ctx, tx, &cp, cp.StartDate); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.afterWrite(ctx, userID, cp.ID, tz, nil)
}

func copyName(n string) string {
	suffix := " (สำเนา)"
	r := []rune(n)
	if len(r)+len([]rune(suffix)) > 100 {
		r = r[:100-len([]rune(suffix))]
	}
	return string(r) + suffix
}

// POST /v1/recurring/:id/duplicate
func (h *Handler) Duplicate(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.Duplicate(c.Request.Context(), uid, id, tz(c))
	reply(c, http.StatusCreated, out, err)
}
