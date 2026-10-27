package recurring

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
	"github.com/ppChub722/chubi-pocket-be/internal/shared/recurrence"
)

// Pausing, the overdue limit and resuming (0.4.1-1 R7 / R8 / R9).

const (
	PauseManual       = "manual"
	PauseOverdueLimit = "overdue_limit" // "ค้าง 3 งวด"
	PauseGoalInactive = "goal_inactive" // a saving setting whose goal was closed / archived

	// overdueMax — the due occurrences a setting may hold, whatever its frequency.
	overdueMax = 3
)

// pauseTx pauses r from `from` for a reason. The due occurrences in keep are
// stored first, so they stay due and listed while it is paused (a computed one
// would vanish with the rule's schedule).
func (s *Service) pauseTx(ctx context.Context, tx pgx.Tx, r *Rule, keep []*occ, from time.Time, reason string) error {
	for _, o := range keep {
		if !o.stored() {
			if _, err := s.store(ctx, tx, o); err != nil {
				return err
			}
		}
	}
	r.Status, r.PausedFrom, r.PauseReason = "paused", &from, &reason
	return nil
}

// overdueLimit — a setting holds at most 3 due occurrences. When a 4th has come
// due while 3 wait (any 3), it turns paused (overdue_limit) instead of making
// it: the first 3 stay due, nothing after them is made. Clearing them does not
// resume it. Auto-create settings never wait, so they are never limited.
func (s *Service) overdueLimit(ctx context.Context, tx pgx.Tx, r *Rule, today time.Time) error {
	if r.AutoCreate || r.Status != "active" {
		return nil
	}
	due, err := s.dueOccs(ctx, tx, r, today)
	if err != nil || len(due) <= overdueMax {
		return err
	}
	if err := s.pauseTx(ctx, tx, r, due[:overdueMax], due[overdueMax].date, PauseOverdueLimit); err != nil {
		return err
	}
	return saveRuleTx(ctx, tx, r)
}

// currentSlot — the occurrence whose period holds today (a monthly one: this
// month's, even when its day is still to come); else the next one; nil when
// the schedule has none left.
func (r *Rule) currentSlot(today time.Time) *time.Time {
	ext := r.Spec.Between(today.AddDate(-2, 0, 0), today.AddDate(2, 0, 0))
	if i := periodOf(r, ext, today); i >= 0 {
		return &ext[i]
	}
	for i := range ext {
		if !ext[i].Before(today) {
			return &ext[i]
		}
	}
	return nil
}

// resumeGap — resuming makes the current period's occurrence the next one: the
// span from the pause up to it becomes a gap the schedule skips (R8). A count
// end counts past it, so no installment is lost (R9); a date end keeps its date.
func (r *Rule) resumeGap(today time.Time) {
	from := time.Date(r.UpdatedAt.Year(), r.UpdatedAt.Month(), r.UpdatedAt.Day(), 0, 0, 0, 0, time.UTC)
	if r.PausedFrom != nil {
		from = *r.PausedFrom
	}
	if from.Before(r.TrackedFrom) {
		from = r.TrackedFrom
	}
	to := today
	if cur := r.currentSlot(today); cur != nil {
		to = *cur
	}
	if from.Before(to) {
		r.Gaps = append(r.Gaps, recurrence.Gap{From: from.Format(dateLayout), To: to.Format(dateLayout)})
	}
}

// pauseReason — what the API shows: the reason while paused, else null.
func (r *Rule) pauseReason() *string {
	if r.Status != "paused" {
		return nil
	}
	if r.PauseReason == nil {
		return ptr(PauseManual)
	}
	return r.PauseReason
}

// autoDraftTx — what auto-create records (R12): a wallet that is gone or
// archived, a category that is gone or no longer fits, and tags that are gone
// are left out, so the transaction is still made and the setting keeps running.
func autoDraftTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, d *pending.Draft) error {
	for _, acc := range []**uuid.UUID{&d.AccountID, &d.TransferToAccountID} {
		if *acc == nil {
			continue
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT `+shared.ActiveMembershipPredicate("$1", "$2")+`
			AND EXISTS (SELECT 1 FROM accounts WHERE id = $1 AND status <> 'archived')`, **acc, userID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			*acc = nil
		}
	}
	if d.CategoryID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM categories WHERE id = $1 AND user_id = $2
			AND NOT is_system AND type = $3)`, *d.CategoryID, userID, draftType(*d)).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			d.CategoryID = nil
		}
	}
	if len(d.TagIDs) > 0 {
		tags, err := collectUUIDs(ctx, tx, `SELECT id FROM tags WHERE user_id = $1 AND id = ANY($2) ORDER BY id`, userID, d.TagIDs)
		if err != nil {
			return err
		}
		d.TagIDs = tags
	}
	return nil
}
