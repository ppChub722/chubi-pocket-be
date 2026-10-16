package saving_goals

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// --- goals ---

func applyGoal(g *Goal, r *GoalRequest, creating bool) error {
	has := func(k string) bool { return r.present[k] }
	if creating || has("name") {
		if r.Name == nil {
			return ErrValidation
		}
		n := strings.TrimSpace(*r.Name)
		if n == "" || len([]rune(n)) > 100 {
			return ErrValidation
		}
		g.Name = n
	}
	if has("description") {
		if r.Description != nil && len([]rune(*r.Description)) > 200 {
			return ErrValidation
		}
		g.Description = shared.CleanText(r.Description)
	}
	if has("note") {
		if r.Note != nil && len([]rune(*r.Note)) > 500 {
			return ErrValidation
		}
		g.Note = shared.CleanText(r.Note)
	}
	if has("icon_code") {
		g.IconCode = r.IconCode
	}
	if creating || has("target_amount") {
		if r.TargetAmount == nil || *r.TargetAmount <= 0 {
			return ErrValidation
		}
		g.TargetAmount = *r.TargetAmount
	}
	if has("deadline") {
		g.Deadline = nil
		if r.Deadline != nil {
			d, err := time.Parse(dateLayout, *r.Deadline)
			if err != nil {
				return ErrValidation
			}
			g.Deadline = &d
		}
	}
	if creating && !has("milestones") {
		g.Milestones = append([]int{}, defaultMilestones...)
	} else if has("milestones") {
		seen := map[int]bool{}
		g.Milestones = []int{}
		for _, m := range r.Milestones {
			if m < 1 || m > 100 {
				return ErrValidation
			}
			if !seen[m] {
				seen[m] = true
				g.Milestones = append(g.Milestones, m)
			}
		}
		sort.Ints(g.Milestones)
	}
	return nil
}

func (s *Service) Create(ctx context.Context, userID uuid.UUID, r *GoalRequest) (*GoalDetail, error) {
	g := &Goal{UserID: userID, Status: StateActive}
	if err := applyGoal(g, r, true); err != nil {
		return nil, err
	}
	if err := s.store.saveGoal(ctx, s.store.db, g); err != nil {
		return nil, err
	}
	return s.Get(ctx, userID, g.ID, entriesPerPage)
}

func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, r *GoalRequest) (*GoalDetail, error) {
	g, err := s.store.getGoal(ctx, s.store.db, userID, id, false)
	if err != nil {
		return nil, err
	}
	if err := applyGoal(g, r, false); err != nil {
		return nil, err
	}
	if err := s.store.saveGoal(ctx, s.store.db, g); err != nil {
		return nil, err
	}
	return s.Get(ctx, userID, id, entriesPerPage)
}

func (s *Service) goalSaved(ctx context.Context, q shared.Querier, userID, id uuid.UUID) (float64, map[uuid.UUID]float64, error) {
	per, err := bucketBalances(ctx, q, userID, &id)
	if err != nil {
		return 0, nil, err
	}
	var total float64
	for _, a := range per {
		total += a
	}
	return round2(total), per, nil
}

func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) error {
	tx, err := s.store.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockUser(ctx, tx, userID); err != nil {
		return err
	}
	if _, err := s.store.getGoal(ctx, tx, userID, id, true); err != nil {
		return err
	}
	saved, _, err := s.goalSaved(ctx, tx, userID, id)
	if err != nil {
		return err
	}
	if saved > 0.004 {
		return ErrGoalHasSavings
	}
	if _, err := tx.Exec(ctx, `DELETE FROM saving_goals WHERE id = $1 AND user_id = $2`, id, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) setStatus(ctx context.Context, userID, id uuid.UUID, check func(g *Goal, saved float64) error, status string) (*GoalDetail, error) {
	tx, err := s.store.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := lockUser(ctx, tx, userID); err != nil {
		return nil, err
	}
	g, err := s.store.getGoal(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	saved, _, err := s.goalSaved(ctx, tx, userID, id)
	if err != nil {
		return nil, err
	}
	if err := check(g, saved); err != nil {
		return nil, err
	}
	g.Status = status
	if status == StateActive {
		g.ClosedAt = nil
	}
	if err := s.store.saveGoal(ctx, tx, g); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Get(ctx, userID, id, entriesPerPage)
}

func (s *Service) Archive(ctx context.Context, userID, id uuid.UUID) (*GoalDetail, error) {
	return s.setStatus(ctx, userID, id, func(g *Goal, saved float64) error {
		if g.Status == StateArchived {
			return ErrInvalidTransition
		}
		if saved > 0.004 {
			return ErrGoalHasSavings
		}
		return nil
	}, StateArchived)
}

func (s *Service) Restore(ctx context.Context, userID, id uuid.UUID) (*GoalDetail, error) {
	return s.setStatus(ctx, userID, id, func(g *Goal, _ float64) error {
		if g.Status == StateActive {
			return ErrInvalidTransition
		}
		return nil
	}, StateActive)
}

// Close — ปิดเป้าหมาย: the money goes to spare or back to its wallets, the
// goal is completed.
func (s *Service) Close(ctx context.Context, userID, id uuid.UUID, r CloseRequest) (*WriteResult, error) {
	if r.To != BucketSpare && r.To != "wallets" {
		return nil, ErrValidation
	}
	today, err := s.today(ctx, userID)
	if err != nil {
		return nil, err
	}
	tx, err := s.store.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := lockUser(ctx, tx, userID); err != nil {
		return nil, err
	}
	g, err := s.store.getGoal(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	if g.Status != StateActive {
		return nil, ErrInvalidTransition
	}
	_, per, err := s.goalSaved(ctx, tx, userID, id)
	if err != nil {
		return nil, err
	}
	group, _ := uuid.NewV7()
	var (
		ids     []uuid.UUID
		wallets []uuid.UUID
	)
	for acc, amt := range per {
		if amt < 0.005 {
			continue
		}
		e := &entryRow{UserID: userID, GroupID: group, Kind: KindWithdraw, AccountID: acc, FromBucket: BucketGoal,
			FromGoalID: &g.ID, ToBucket: BucketAvailable, Amount: round2(amt), Date: today, Source: SourceManual}
		if r.To == BucketSpare {
			e.Kind, e.ToBucket = KindMove, BucketSpare
		}
		if err := s.store.insertEntry(ctx, tx, e); err != nil {
			return nil, err
		}
		ids = append(ids, e.ID)
		if acc != uuid.Nil {
			wallets = append(wallets, acc)
		}
	}
	now := time.Now()
	g.Status, g.ClosedAt = StateCompleted, &now
	if err := s.store.saveGoal(ctx, tx, g); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.writeResult(ctx, userID, ids, []uuid.UUID{g.ID}, wallets, pageRef{goal: &g.ID})
}

// --- entries ---

// sourceOf — the bucket a withdraw / move takes from.
func bucketOf(goalID *uuid.UUID) string {
	if goalID == nil {
		return BucketSpare
	}
	return BucketGoal
}

func sameBucket(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// splitsFor — how much each wallet gives: the client's splits (checked) or
// largest-first.
func splitsFor(per map[uuid.UUID]float64, amount float64, in []Split) ([]Split, error) {
	var total float64
	for _, a := range per {
		total += a
	}
	if amount > total+0.004 {
		return nil, &InsufficientError{Available: round2(total)}
	}
	if len(in) > 0 {
		var sum float64
		seen := map[uuid.UUID]bool{}
		for _, sp := range in {
			if sp.Amount <= 0 || seen[sp.WalletID] {
				return nil, ErrSplitMismatch
			}
			seen[sp.WalletID] = true
			if sp.Amount > per[sp.WalletID]+0.004 {
				return nil, &InsufficientError{Available: round2(per[sp.WalletID])}
			}
			sum += sp.Amount
		}
		if math.Abs(sum-amount) > 0.004 {
			return nil, ErrSplitMismatch
		}
		return in, nil
	}
	type wb struct {
		id  uuid.UUID
		amt float64
	}
	var list []wb
	for id, a := range per {
		if a > 0.004 {
			list = append(list, wb{id, a})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].amt != list[j].amt {
			return list[i].amt > list[j].amt
		}
		return list[i].id.String() < list[j].id.String()
	})
	var out []Split
	left := amount
	for _, w := range list {
		if left < 0.005 {
			break
		}
		take := math.Min(w.amt, left)
		out = append(out, Split{WalletID: w.id, Amount: round2(take)})
		left = round2(left - take)
	}
	return out, nil
}

// checkGoalTarget — a goal money may go into: the caller's, active.
func (s *Service) checkGoalTarget(ctx context.Context, q querier, userID uuid.UUID, goalID *uuid.UUID) error {
	if goalID == nil {
		return nil
	}
	g, err := s.store.getGoal(ctx, q, userID, *goalID, false)
	if err != nil {
		return err
	}
	if g.Status != StateActive {
		return ErrGoalNotActive
	}
	return nil
}

// createEntriesTx writes one action (one row per wallet). Returns the
// entry ids, the goals and the wallets it touched.
func (s *Service) createEntriesTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, r EntryRequest, source string) ([]uuid.UUID, []uuid.UUID, []uuid.UUID, error) {
	if r.Amount <= 0 || (r.Note != nil && len([]rune(*r.Note)) > 500) {
		return nil, nil, nil, ErrValidation
	}
	date, err := s.today(ctx, userID)
	if err != nil {
		return nil, nil, nil, err
	}
	if r.Date != nil {
		if date, err = time.Parse(dateLayout, *r.Date); err != nil {
			return nil, nil, nil, ErrValidation
		}
	}
	if err := lockUser(ctx, tx, userID); err != nil {
		return nil, nil, nil, err
	}
	group, _ := uuid.NewV7()
	var goals []uuid.UUID
	for _, g := range []*uuid.UUID{r.GoalID, r.ToGoalID} {
		if g != nil {
			goals = append(goals, *g)
		}
	}
	row := func(acc uuid.UUID, amt float64) *entryRow {
		return &entryRow{UserID: userID, GroupID: group, Kind: r.Kind, AccountID: acc, Amount: round2(amt), Date: date,
			Note: r.Note, Source: source}
	}
	var rows []*entryRow
	switch r.Kind {
	case KindSave:
		if len(r.Splits) > 0 || r.ToGoalID != nil {
			return nil, nil, nil, ErrValidation
		}
		walletID := uuid.Nil // no wallet_id sent = NO wallet (outside any wallet; counts for the goal only)
		if r.WalletID != nil {
			if err := checkWallet(ctx, tx, userID, *r.WalletID); err != nil {
				return nil, nil, nil, err
			}
			walletID = *r.WalletID
		}
		if err := s.checkGoalTarget(ctx, tx, userID, r.GoalID); err != nil {
			return nil, nil, nil, err
		}
		e := row(walletID, r.Amount)
		e.FromBucket, e.ToBucket, e.ToGoalID = BucketAvailable, bucketOf(r.GoalID), r.GoalID
		rows = append(rows, e)
	case KindWithdraw, KindMove:
		if r.WalletID != nil {
			return nil, nil, nil, ErrValidation
		}
		if r.GoalID != nil {
			if _, err := s.store.getGoal(ctx, tx, userID, *r.GoalID, false); err != nil {
				return nil, nil, nil, err
			}
		}
		if r.Kind == KindMove {
			if sameBucket(r.GoalID, r.ToGoalID) {
				return nil, nil, nil, ErrValidation
			}
			if err := s.checkGoalTarget(ctx, tx, userID, r.ToGoalID); err != nil {
				return nil, nil, nil, err
			}
		} else if r.ToGoalID != nil {
			return nil, nil, nil, ErrValidation
		}
		per, err := bucketBalances(ctx, tx, userID, r.GoalID)
		if err != nil {
			return nil, nil, nil, err
		}
		splits, err := splitsFor(per, r.Amount, r.Splits)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, sp := range splits {
			e := row(sp.WalletID, sp.Amount)
			e.FromBucket, e.FromGoalID = bucketOf(r.GoalID), r.GoalID
			e.ToBucket = BucketAvailable
			if r.Kind == KindMove {
				e.ToBucket, e.ToGoalID = bucketOf(r.ToGoalID), r.ToGoalID
			}
			rows = append(rows, e)
		}
	default:
		return nil, nil, nil, ErrValidation
	}
	var ids, wallets []uuid.UUID
	for _, e := range rows {
		if err := s.store.insertEntry(ctx, tx, e); err != nil {
			return nil, nil, nil, err
		}
		ids = append(ids, e.ID)
		if e.AccountID != uuid.Nil {
			wallets = append(wallets, e.AccountID)
		}
	}
	return ids, goals, wallets, nil
}

// CreateEntry — POST /saving-entries.
func (s *Service) CreateEntry(ctx context.Context, userID uuid.UUID, r EntryRequest) (*WriteResult, error) {
	tx, err := s.store.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	ids, goals, wallets, err := s.createEntriesTx(ctx, tx, userID, r, SourceManual)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.writeResult(ctx, userID, ids, goals, wallets, pageRef{goal: r.GoalID, spare: r.GoalID == nil})
}

// entryPageRef — the page an entry belongs to: a save's target, else its
// source.
func entryPageRef(e *entryRow) pageRef {
	bucket, goal := e.FromBucket, e.FromGoalID
	if e.Kind == KindSave {
		bucket, goal = e.ToBucket, e.ToGoalID
	}
	return pageRef{goal: goal, spare: bucket == BucketSpare}
}

// updateEntryTx changes one entry (amount / date / note); no bucket of its
// wallet may go below 0.
func (s *Service) updateEntryTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID, r EntryUpdateRequest) (*entryRow, error) {
	if err := lockUser(ctx, tx, userID); err != nil {
		return nil, err
	}
	e, err := s.store.getEntry(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	if r.Amount != nil {
		if *r.Amount <= 0 {
			return nil, ErrValidation
		}
		e.Amount = round2(*r.Amount)
	}
	if r.Date != nil {
		d, err := time.Parse(dateLayout, *r.Date)
		if err != nil {
			return nil, ErrValidation
		}
		e.Date = d
	}
	if r.present["note"] {
		if r.Note != nil && len([]rune(*r.Note)) > 500 {
			return nil, ErrValidation
		}
		e.Note = shared.CleanText(r.Note)
	}
	if _, err := tx.Exec(ctx, `UPDATE saving_entries SET amount = $2, date = $3, note = $4 WHERE id = $1`,
		e.ID, e.Amount, e.Date, e.Note); err != nil {
		return nil, err
	}
	if err := checkNoNegative(ctx, tx, userID, e.AccountID); err != nil {
		return nil, err
	}
	return e, nil
}

func goalsOf(e *entryRow) []uuid.UUID {
	var out []uuid.UUID
	for _, g := range []*uuid.UUID{e.FromGoalID, e.ToGoalID} {
		if g != nil {
			out = append(out, *g)
		}
	}
	return out
}

func (s *Service) UpdateEntry(ctx context.Context, userID, id uuid.UUID, r EntryUpdateRequest) (*WriteResult, error) {
	tx, err := s.store.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	e, err := s.updateEntryTx(ctx, tx, userID, id, r)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.writeResult(ctx, userID, []uuid.UUID{e.ID}, goalsOf(e), walletList(e.AccountID), entryPageRef(e))
}

func (s *Service) deleteEntryTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID) (*entryRow, error) {
	if err := lockUser(ctx, tx, userID); err != nil {
		return nil, err
	}
	e, err := s.store.getEntry(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM saving_entries WHERE id = $1`, id); err != nil {
		return nil, err
	}
	if err := checkNoNegative(ctx, tx, userID, e.AccountID); err != nil {
		return nil, err
	}
	return e, nil
}

func (s *Service) DeleteEntry(ctx context.Context, userID, id uuid.UUID) (*WriteResult, error) {
	tx, err := s.store.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	e, err := s.deleteEntryTx(ctx, tx, userID, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.writeResult(ctx, userID, nil, goalsOf(e), walletList(e.AccountID), entryPageRef(e))
}

// writeResult — the final state after a write.
func (s *Service) writeResult(ctx context.Context, userID uuid.UUID, entryIDs, goalIDs, walletIDs []uuid.UUID, page pageRef) (*WriteResult, error) {
	today, err := s.today(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := &WriteResult{Entries: []Entry{}, Goals: []GoalView{}, Wallets: []WalletOut{}}
	if len(entryIDs) > 0 {
		rows, err := s.store.listEntries(ctx, s.store.db, userID, `AND id = ANY($2) ORDER BY amount DESC, id`, entryIDs)
		if err != nil {
			return nil, err
		}
		if out.Entries, err = s.entryViews(ctx, rows); err != nil {
			return nil, err
		}
	}
	if len(goalIDs) > 0 {
		goals, err := s.store.listGoals(ctx, s.store.db, userID, `AND id = ANY($2) ORDER BY created_at, id`, goalIDs)
		if err != nil {
			return nil, err
		}
		if out.Goals, err = s.goalViews(ctx, userID, goals, today); err != nil {
			return nil, err
		}
	}
	if out.Spare, err = s.spare(ctx, s.store.db, userID); err != nil {
		return nil, err
	}
	savings, err := shared.LoadWalletSavings(ctx, s.store.db, userID, walletIDs)
	if err != nil {
		return nil, err
	}
	refs, err := shared.LoadAccountRefs(ctx, s.store.db, walletIDs)
	if err != nil {
		return nil, err
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range walletIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		ref, ok := refs[id]
		if !ok {
			ref = shared.AccountRef{ID: id}
		}
		out.Wallets = append(out.Wallets, WalletOut{Wallet: ref, Savings: savings[id]})
	}
	switch {
	case page.goal != nil:
		if out.GoalPage, err = s.Get(ctx, userID, *page.goal, entriesPerPage); err != nil {
			return nil, err
		}
	case page.spare:
		if out.SparePage, err = s.Spare(ctx, userID, entriesPerPage); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// --- hooks (pending / recurring) ---

// SaveFromDraftTx — pending: a saving draft submitted (save `amount` from
// the wallet into the goal, nil = spare). Returns the entry id.
func (s *Service) SaveFromDraftTx(ctx context.Context, tx pgx.Tx, userID, accountID uuid.UUID, goalID *uuid.UUID,
	amount float64, date string, note *string, source string) (uuid.UUID, error) {
	d := date
	ids, _, _, err := s.createEntriesTx(ctx, tx, userID, EntryRequest{
		Kind: KindSave, Amount: amount, Date: &d, Note: note, GoalID: goalID, WalletID: &accountID,
	}, source)
	if err != nil {
		return uuid.Nil, err
	}
	return ids[0], nil
}

// UpdateEntryTx — recurring also_update_link: the entry takes the item's
// amount / date / note.
func (s *Service) UpdateEntryTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID, amount *float64, date *string, note *string, noteSet bool) error {
	r := EntryUpdateRequest{Amount: amount, Date: date, Note: note, present: map[string]bool{"note": noteSet}}
	_, err := s.updateEntryTx(ctx, tx, userID, id, r)
	return err
}

// DeleteEntryTx — recurring delete_linked_tx.
func (s *Service) DeleteEntryTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID) error {
	_, err := s.deleteEntryTx(ctx, tx, userID, id)
	return err
}

// walletList — the wallet an entry touched (none for money with no wallet).
func walletList(id uuid.UUID) []uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return []uuid.UUID{id}
}
