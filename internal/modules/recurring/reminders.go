package recurring

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Alerts on a reminder (0.4.3): anchor due (every round of the reminder, days_before its date),
// start (the item's plan / the reminder's start) or end (the plan's last installment / the
// reminder's last round), at a local time. A job sends them as recurring_due_soon notices, once
// per alert per round.

const maxAlerts = 5

func validAlerts(in []AlertInput) ([]AlertInput, error) {
	if len(in) > maxAlerts {
		return nil, ErrValidation
	}
	out := make([]AlertInput, 0, len(in))
	for _, r := range in {
		if r.Anchor != "due" && r.Anchor != "start" && r.Anchor != "end" {
			return nil, ErrValidation
		}
		if r.DaysBefore < 0 || r.DaysBefore > 60 {
			return nil, ErrValidation
		}
		if r.Time == "" {
			r.Time = "09:00"
		}
		t, err := time.Parse("15:04", r.Time)
		if err != nil {
			return nil, ErrValidation
		}
		r.Time = t.Format("15:04")
		out = append(out, r)
	}
	return out, nil
}

// saveAlertsTx — the reminder's alerts become in (unchanged ones keep their id, so what was sent
// is not sent again); nil = kept.
func saveAlertsTx(ctx context.Context, tx pgx.Tx, r *Rule, in *[]AlertInput) error {
	if in == nil {
		return nil
	}
	want, err := validAlerts(*in)
	if err != nil {
		return err
	}
	have, err := loadAlerts(ctx, tx, r.ID)
	if err != nil {
		return err
	}
	key := func(a string, d int, t string) string { return fmt.Sprintf("%s/%d/%s", a, d, t) }
	keep := map[string]bool{}
	for _, a := range want {
		keep[key(a.Anchor, a.DaysBefore, a.Time)] = true
	}
	had := map[string]bool{}
	for _, a := range have {
		k := key(a.Anchor, a.DaysBefore, a.Time)
		if !keep[k] || had[k] {
			if _, err := tx.Exec(ctx, `DELETE FROM recurring_reminder_alerts WHERE id = $1`, a.ID); err != nil {
				return err
			}
			continue
		}
		had[k] = true
	}
	for _, a := range want {
		k := key(a.Anchor, a.DaysBefore, a.Time)
		if had[k] {
			continue
		}
		had[k] = true
		if _, err := tx.Exec(ctx, `INSERT INTO recurring_reminder_alerts (user_id, reminder_id, anchor, days_before, time)
			VALUES ($1, $2, $3, $4, $5::time)`, r.UserID, r.ID, a.Anchor, a.DaysBefore, a.Time); err != nil {
			return fmt.Errorf("save alert: %w", err)
		}
	}
	return nil
}

func loadAlerts(ctx context.Context, q shared.Querier, reminderID uuid.UUID) ([]Alert, error) {
	rows, err := q.Query(ctx, `SELECT id, anchor, days_before, to_char(time, 'HH24:MI') FROM recurring_reminder_alerts
		WHERE reminder_id = $1 ORDER BY anchor, days_before DESC, time, id`, reminderID)
	if err != nil {
		return nil, fmt.Errorf("alerts: %w", err)
	}
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		var r Alert
		if err := rows.Scan(&r.ID, &r.Anchor, &r.DaysBefore, &r.Time); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// StartAlertJob — runs SendAlerts every interval until ctx ends.
func (s *Service) StartAlertJob(ctx context.Context, interval time.Duration, log *slog.Logger) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			if n, err := s.SendAlerts(ctx, time.Now()); err != nil {
				log.Warn("recurring alerts", "error", err)
			} else if n > 0 {
				log.Info("recurring alerts sent", "count", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// SendAlerts — every user's alerts whose moment (the user's local day and time) has come and
// that were not sent yet. Answers how many notices it wrote.
func (s *Service) SendAlerts(ctx context.Context, now time.Time) (int, error) {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT m.user_id FROM recurring_reminder_alerts m
		JOIN recurring_reminders r ON r.id = m.reminder_id JOIN recurrings g ON g.id = r.recurring_id
		WHERE r.status = 'active' AND g.status = 'active'`)
	if err != nil {
		return 0, fmt.Errorf("alert users: %w", err)
	}
	var users []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		users = append(users, id)
	}
	rows.Close()
	sent := 0
	for _, u := range users {
		n, err := s.sendUserAlerts(ctx, u, now)
		if err != nil {
			slog.Warn("recurring alerts of a user", "user_id", u, "error", err)
			continue
		}
		sent += n
	}
	return sent, nil
}

type alertRow struct {
	id         uuid.UUID
	reminderID uuid.UUID
	itemID     uuid.UUID
	anchor     string
	daysBefore int
	at         string // HH:MM
}

func (s *Service) sendUserAlerts(ctx context.Context, userID uuid.UUID, now time.Time) (int, error) {
	loc := time.UTC
	if prefs, err := shared.UserPrefs(ctx, s.db, userID); err == nil {
		for _, n := range []string{prefs.Timezone, defaultTimezone} {
			if l, err := time.LoadLocation(n); n != "" && err == nil {
				loc = l
				break
			}
		}
	}
	local := now.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	clock := local.Format("15:04")
	rows, err := s.db.Query(ctx, `SELECT m.id, r.id, r.recurring_id, m.anchor, m.days_before, to_char(m.time, 'HH24:MI')
		FROM recurring_reminder_alerts m JOIN recurring_reminders r ON r.id = m.reminder_id
		JOIN recurrings g ON g.id = r.recurring_id
		WHERE m.user_id = $1 AND r.status = 'active' AND g.status = 'active' AND to_char(m.time, 'HH24:MI') <= $2`, userID, clock)
	if err != nil {
		return 0, err
	}
	var rems []alertRow
	for rows.Next() {
		var r alertRow
		if err := rows.Scan(&r.id, &r.reminderID, &r.itemID, &r.anchor, &r.daysBefore, &r.at); err != nil {
			rows.Close()
			return 0, err
		}
		rems = append(rems, r)
	}
	rows.Close()
	if len(rems) == 0 || s.notifs == nil {
		return 0, nil
	}
	sent := 0
	for _, m := range rems {
		it, err := getItemRecord(ctx, s.db, userID, m.itemID, false)
		if err != nil {
			continue
		}
		rem, err := getRule(ctx, s.db, userID, m.reminderID, false)
		if err != nil {
			continue
		}
		lines := []*Rule{rem}
		target := today.AddDate(0, 0, m.daysBefore) // the date the alert is about
		type soon struct {
			p      notifications.RecurringDueSoonPayload
			target uuid.UUID
			slot   time.Time
		}
		var due []soon
		switch m.anchor {
		case "due":
			occs, err := s.occsInRange(ctx, s.db, userID, lines, target, target, today, true)
			if err != nil {
				return sent, err
			}
			for _, o := range occs {
				if o.status != StatusUpcoming && o.status != StatusDue {
					continue
				}
				ruleID := o.rule.ID
				due = append(due, soon{p: notifications.RecurringDueSoonPayload{RecurringID: it.ID, RecurringReminderID: &ruleID,
					Name: it.Name, Date: o.date.Format(dateLayout), Amount: amountPtrFrom(o.draft.Amount),
					DaysBefore: m.daysBefore, Anchor: m.anchor}, target: ruleID, slot: o.slot})
			}
		case "start", "end":
			if d := anchorDate(it, lines, m.anchor); d != nil && d.Equal(target) {
				due = append(due, soon{p: notifications.RecurringDueSoonPayload{RecurringID: it.ID, RecurringReminderID: &rem.ID,
					Name: it.Name, Date: d.Format(dateLayout), DaysBefore: m.daysBefore, Anchor: m.anchor}, target: rem.ID, slot: *d})
			}
		}
		for _, x := range due {
			n, err := s.sendOne(ctx, userID, m.id, x.target, x.slot, x.p)
			if err != nil {
				return sent, err
			}
			sent += n
		}
	}
	return sent, nil
}

// anchorDate — the start / end date an alert is about: the item's plan's first / last installment,
// else the reminder's start / last round.
func anchorDate(it *itemRecord, lines []*Rule, anchor string) *time.Time {
	if p := it.Plan; p != nil {
		if anchor == "start" {
			d := p.Start
			return &d
		}
		d := addMonths(p.Start, p.Count-1)
		return &d
	}
	var ds []time.Time
	for _, r := range lines {
		if anchor == "start" {
			ds = append(ds, r.StartDate)
		} else if r.GeneratedUntil != nil && r.TotalCount != nil {
			ds = append(ds, *r.GeneratedUntil)
		}
	}
	if len(ds) == 0 {
		return nil
	}
	sort.Slice(ds, func(a, b int) bool { return ds[a].Before(ds[b]) })
	d := ds[0]
	if anchor == "end" {
		d = ds[len(ds)-1]
	}
	return &d
}

func (s *Service) sendOne(ctx context.Context, userID, alertID, targetID uuid.UUID, slot time.Time, p notifications.RecurringDueSoonPayload) (int, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO recurring_reminder_alert_sends (alert_id, target_id, slot) VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING`, alertID, targetID, slot)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 0 {
		return 0, nil // sent already
	}
	if err := s.notifs.RecurringDueSoonTx(ctx, tx, userID, p); err != nil {
		return 0, err
	}
	return 1, tx.Commit(ctx)
}
