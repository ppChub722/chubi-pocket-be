package pending

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/projects"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/saving_goals"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/tags"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

const dateLayout = "2006-01-02"

var ErrInvalidDraft = errors.New("invalid draft")

type submitErr struct {
	code apierr.Code
	msg  string
}

func (e *submitErr) Error() string { return e.msg }

type Service struct {
	store    *Store
	txs      *transactions.Service
	tags     *tags.Service
	debts    *personal_debts.Service
	projects *projects.Service
	onSubmit SubmitHook
	savings  *saving_goals.Service
}

const typeSaving = transactions.TxType("saving")

func (s *Service) WithSavings(sv *saving_goals.Service) { s.savings = sv }

func NewService(store *Store, txs *transactions.Service, tg *tags.Service, debts *personal_debts.Service, prj *projects.Service) *Service {
	return &Service{store: store, txs: txs, tags: tg, debts: debts, projects: prj}
}

func (s *Service) List(ctx context.Context, userID uuid.UUID) (*ListResponse, error) {
	rows, err := s.store.List(ctx, userID)
	if err != nil {
		return nil, err
	}
	if err := ResolveRows(ctx, s.store.Pool(), userID, rows); err != nil {
		return nil, err
	}
	return &ListResponse{Data: rows, Count: len(rows), Summary: summarize(rows)}, nil
}

func checkDraft(d Draft) error {
	if d.Type != nil {
		switch transactions.TxType(*d.Type) {
		case transactions.TypeExpense, transactions.TypeIncome, transactions.TypeTransfer, typeSaving:
		default:
			return apierr.Wrap(ErrInvalidDraft, "type must be expense, income or transfer", apierr.Field("type", apierr.FieldInvalid, "allowed", []string{"expense", "income", "transfer"}))
		}
	}
	if d.Amount != nil && *d.Amount < 0 {
		return apierr.Wrap(ErrInvalidDraft, "amount can't be negative", apierr.Field("amount", apierr.FieldOutOfRange, "min", 0))
	}
	if d.Date != nil {
		if _, err := time.Parse(dateLayout, *d.Date); err != nil {
			return apierr.Wrap(ErrInvalidDraft, "date must be YYYY-MM-DD", apierr.Field("date", apierr.FieldInvalid, "format", "date"))
		}
	}
	if p := d.Paper; p != nil {
		if d.Type != nil && *d.Type != string(transactions.TypeExpense) && *d.Type != string(transactions.TypeIncome) {
			return apierr.Wrap(ErrInvalidDraft, "a shared bill is an expense or an income", apierr.Field("type", apierr.FieldInvalid, "allowed", []string{"expense", "income"}))
		}
		if p.SplitMode != "" && p.SplitMode != "equal" && p.SplitMode != "custom" {
			return apierr.Wrap(ErrInvalidDraft, "split_mode must be equal or custom", apierr.Field("paper.split_mode", apierr.FieldInvalid, "allowed", []string{"equal", "custom"}))
		}
		if p.Pay != "" && p.Pay != "debt" && p.Pay != "wallet" {
			return apierr.Wrap(ErrInvalidDraft, "pay must be debt or wallet", apierr.Field("paper.pay", apierr.FieldInvalid, "allowed", []string{"debt", "wallet"}))
		}
	}
	return nil
}

func (s *Service) Create(ctx context.Context, userID uuid.UUID, req CreateRequest) ([]PendingTransaction, error) {
	for _, it := range req.Items {
		if err := checkDraft(it.Draft); err != nil {
			return nil, err
		}
	}
	out := make([]PendingTransaction, 0, len(req.Items))
	for _, it := range req.Items {
		p, err := s.store.Insert(ctx, userID, SourceManual, KindCreate, it.Draft, nil, nil, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	if err := ResolveRows(ctx, s.store.Pool(), userID, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, req UpdateRequest) (*PendingTransaction, error) {
	if err := checkDraft(req.Draft); err != nil {
		return nil, err
	}
	p, err := s.store.UpdateDraft(ctx, userID, id, req.Draft)
	if err != nil {
		return nil, err
	}
	return p, s.resolveOne(ctx, userID, p)
}

func (s *Service) resolveOne(ctx context.Context, userID uuid.UUID, p *PendingTransaction) error {
	rows := []PendingTransaction{*p}
	if err := ResolveRows(ctx, s.store.Pool(), userID, rows); err != nil {
		return err
	}
	p.Resolved, p.NeedsInput, p.CanConfirm = rows[0].Resolved, rows[0].NeedsInput, rows[0].CanConfirm
	return nil
}

func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) (*DiscardResult, error) {
	if err := s.store.Delete(ctx, userID, id); err != nil {
		return nil, err
	}
	n, err := s.store.Count(ctx, userID)
	if err != nil {
		return nil, err
	}
	sum, err := s.freshSummary(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &DiscardResult{Discarded: []uuid.UUID{id}, Skipped: []uuid.UUID{}, RemainingCount: n, Summary: sum}, nil
}

func (s *Service) Discard(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) (*DiscardResult, error) {
	gone, err := s.store.DeleteMany(ctx, userID, ids)
	if err != nil {
		return nil, err
	}
	removed := map[uuid.UUID]bool{}
	out := &DiscardResult{Discarded: []uuid.UUID{}, Skipped: []uuid.UUID{}}
	for _, id := range gone {
		removed[id] = true
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if removed[id] {
			out.Discarded = append(out.Discarded, id)
		} else {
			out.Skipped = append(out.Skipped, id)
		}
	}
	if out.RemainingCount, err = s.store.Count(ctx, userID); err != nil {
		return nil, err
	}
	if out.Summary, err = s.freshSummary(ctx, userID); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) Submit(ctx context.Context, userID uuid.UUID, req SubmitRequest) (*SubmitResponse, error) {
	out := &SubmitResponse{Submitted: []Submitted{}, Failed: []Failed{}}
	var touched []uuid.UUID
	seen := map[uuid.UUID]bool{}
	for _, id := range req.IDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		p, err := s.store.Get(ctx, userID, id)
		if err != nil {
			out.Failed = append(out.Failed, Failed{ID: id, Error: toSubmitError(err)})
			continue
		}
		before := s.accountsOfTarget(ctx, userID, p)
		txID, err := s.submitOne(ctx, userID, p)
		if err == nil && p.Kind == KindSaving {
			out.Submitted = append(out.Submitted, Submitted{ID: id, Rows: []transactions.TransactionDetail{}, SavingEntryID: txID})
			continue
		}
		if err != nil {
			e := toSubmitError(err)
			_ = s.store.SetError(ctx, userID, id, e)
			f := Failed{ID: id, Error: e}
			if cur, gerr := s.store.Get(ctx, userID, id); gerr == nil {
				if rerr := s.resolveOne(ctx, userID, cur); rerr != nil {
					return nil, rerr
				}
				f.Pending = cur
			}
			out.Failed = append(out.Failed, f)
			continue
		}
		rows, err := s.rowsOf(ctx, userID, txID)
		if err != nil {
			return nil, err
		}
		for i := range rows {
			if rows[i].AccountID != nil {
				touched = append(touched, *rows[i].AccountID)
			}
		}
		touched = append(touched, before...)
		out.Submitted = append(out.Submitted, Submitted{ID: id, Rows: rows, TransactionID: txID})
	}
	var err error
	if out.AccountBalances, err = shared.LoadWalletBalances(ctx, s.store.Pool(), touched); err != nil {
		return nil, err
	}
	if out.Badges, err = shared.LoadBadges(ctx, s.store.Pool(), userID); err != nil {
		return nil, err
	}
	if out.Summary, err = s.freshSummary(ctx, userID); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) accountsOfTarget(ctx context.Context, userID uuid.UUID, p *PendingTransaction) []uuid.UUID {
	if p.Kind != KindUpdateTx || p.TargetTransactionID == nil {
		return nil
	}
	d, err := s.txs.Get(ctx, userID, *p.TargetTransactionID)
	if err != nil {
		return nil
	}
	var ids []uuid.UUID
	if d.AccountID != nil {
		ids = append(ids, *d.AccountID)
	}
	if d.TransferPeer != nil {
		ids = append(ids, d.TransferPeer.Account.ID)
	}
	return ids
}

func (s *Service) rowsOf(ctx context.Context, userID uuid.UUID, txID *uuid.UUID) ([]transactions.TransactionDetail, error) {
	rows := []transactions.TransactionDetail{}
	if txID == nil {
		return rows, nil
	}
	d, err := s.txs.Get(ctx, userID, *txID)
	if err != nil {
		return nil, err
	}
	rows = append(rows, *d)
	if d.TransferPeer != nil {
		peer, err := s.txs.Get(ctx, userID, d.TransferPeer.ID)
		if err != nil {
			return nil, err
		}
		if d.TransferPeer.Direction == "out" {
			rows = []transactions.TransactionDetail{*d, *peer}
		} else {
			rows = []transactions.TransactionDetail{*peer, *d}
		}
	}
	return rows, nil
}

func (s *Service) submitOne(ctx context.Context, userID uuid.UUID, p *PendingTransaction) (*uuid.UUID, error) {
	switch p.Kind {
	case KindCreate:
		return s.submitCreate(ctx, userID, p)
	case KindSettleDebt:
		return s.submitSettle(ctx, userID, p)
	case KindUpdateTx:
		return s.submitUpdate(ctx, userID, p)
	case KindSaving:
		return s.submitSaving(ctx, userID, p)
	}
	return nil, fmt.Errorf("unknown pending kind %q", p.Kind)
}

func createRequest(d Draft) (transactions.CreateRequest, error) {
	if d.Type == nil {
		return transactions.CreateRequest{}, &submitErr{apierr.MissingType, "type is required"}
	}
	if d.Amount == nil || *d.Amount <= 0 {
		return transactions.CreateRequest{}, &submitErr{apierr.MissingAmount, "amount must be more than 0"}
	}
	if d.Date == nil {
		return transactions.CreateRequest{}, &submitErr{apierr.MissingDate, "date is required"}
	}
	return transactions.CreateRequest{
		Type:                transactions.TxType(*d.Type),
		AccountID:           d.AccountID,
		Amount:              *d.Amount,
		CategoryID:          d.CategoryID,
		Date:                *d.Date,
		Description:         d.Description,
		Note:                d.Note,
		TransferToAccountID: d.TransferToAccountID,
	}, nil
}

func (s *Service) submitCreate(ctx context.Context, userID uuid.UUID, p *PendingTransaction) (*uuid.UUID, error) {
	req, err := createRequest(p.Draft)
	if err != nil {
		return nil, err
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	var txID uuid.UUID
	if p.Draft.Paper != nil {
		if txID, err = s.submitPaperTx(ctx, tx, userID, p.Draft); err != nil {
			return nil, err
		}
	} else {
		created, err := s.txs.CreateInTx(ctx, tx, userID, req)
		if err != nil {
			return nil, err
		}
		switch c := created.(type) {
		case *transactions.TransactionDetail:
			txID = c.ID
		case *transactions.TransferResponse:
			for _, r := range c.Rows {
				if r.AccountID != nil && req.AccountID != nil && *r.AccountID == *req.AccountID {
					txID = r.ID
				}
			}
		}
		if txID != uuid.Nil && s.tags != nil {
			if err := s.tags.AttachTagsTx(ctx, tx, userID, txID, p.Draft.TagIDs); err != nil {
				return nil, err
			}
		}
	}
	if txID != uuid.Nil && s.onSubmit != nil {
		if err := s.onSubmit(ctx, tx, userID, p.ID, txID, KindCreate); err != nil {
			return nil, err
		}
	}
	if err := s.store.DeleteTx(ctx, tx, userID, p.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	if txID == uuid.Nil {
		return nil, nil
	}
	return &txID, nil
}

// submitPaperTx — a shared-bill draft: the paper with my claim; answers my transaction of it
// (uuid.Nil when the claim made none).
func (s *Service) submitPaperTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, d Draft) (uuid.UUID, error) {
	if s.projects == nil {
		return uuid.Nil, errors.New("papers not available")
	}
	p := d.Paper
	if len(p.People) == 0 {
		return uuid.Nil, &submitErr{apierr.ValidationError, "a shared bill needs its people"}
	}
	if *d.Type != string(transactions.TypeExpense) && *d.Type != string(transactions.TypeIncome) {
		return uuid.Nil, &submitErr{apierr.ValidationError, "a shared bill is an expense or an income"}
	}
	pay := p.Pay
	if pay == "" {
		pay = "debt"
	}
	eff := shared.NewEffects()
	paperID, err := s.projects.CreatePaperTx(ctx, tx, userID, p.ProjectID, projects.CreatePaperRequest{
		Paper: projects.PaperInput{
			Type: *d.Type, Amount: *d.Amount, Date: *d.Date, Description: d.Description, Note: d.Note,
			SplitMode: p.SplitMode, People: p.People, TagIDs: p.EventTagIDs,
		},
		MyBooks: &projects.ClaimInput{Pay: pay, AccountID: d.AccountID, CategoryID: d.CategoryID, TagIDs: d.TagIDs},
	}, &eff)
	if err != nil {
		return uuid.Nil, err
	}
	var txID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM transactions WHERE split_transaction_id = $1 AND user_id = $2
		ORDER BY created_at LIMIT 1`, paperID, userID).Scan(&txID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, err
	}
	return txID, nil
}

func (s *Service) submitSaving(ctx context.Context, userID uuid.UUID, p *PendingTransaction) (*uuid.UUID, error) {
	if s.savings == nil {
		return nil, errors.New("savings not available")
	}
	d := p.Draft
	if d.Amount == nil || *d.Amount <= 0 {
		return nil, &submitErr{apierr.MissingAmount, "amount must be more than 0"}
	}
	if d.Date == nil {
		return nil, &submitErr{apierr.MissingDate, "date is required"}
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	source := saving_goals.SourcePending
	if p.Source == "recurring" || p.Source == "upcoming" {
		source = saving_goals.SourceRecurring
	}
	note := d.Note
	if note == nil {
		note = d.Description
	}
	id, err := s.savings.SaveFromDraftTx(ctx, tx, userID, d.AccountID, d.GoalID, *d.Amount, *d.Date, note, source)
	if err != nil {
		return nil, err
	}
	if s.onSubmit != nil {
		if err := s.onSubmit(ctx, tx, userID, p.ID, id, KindSaving); err != nil {
			return nil, err
		}
	}
	if err := s.store.DeleteTx(ctx, tx, userID, p.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return &id, nil
}

func (s *Service) submitSettle(ctx context.Context, userID uuid.UUID, p *PendingTransaction) (*uuid.UUID, error) {
	if s.debts == nil || p.TargetDebtID == nil {
		return nil, errors.New("debt settle not available")
	}
	if p.Draft.Amount == nil || *p.Draft.Amount <= 0 {
		return nil, &submitErr{apierr.MissingAmount, "amount must be more than 0"}
	}
	res, err := s.debts.RepayEntry(ctx, userID, *p.TargetDebtID, *p.Draft.Amount, p.Draft.AccountID,
		p.Draft.Date, p.Draft.Description, p.Draft.Note)
	if err != nil {
		return nil, err
	}
	_ = s.store.Delete(ctx, userID, p.ID)
	if res.Transaction != nil {
		return &res.Transaction.ID, nil
	}
	return nil, nil
}

func (s *Service) submitUpdate(ctx context.Context, userID uuid.UUID, p *PendingTransaction) (*uuid.UUID, error) {
	if p.TargetTransactionID == nil {
		return nil, errors.New("update target missing")
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := s.txs.UpdateInTx(ctx, tx, userID, *p.TargetTransactionID, transactions.UpdateRequest{
		Amount:      p.Draft.Amount,
		Date:        p.Draft.Date,
		Description: p.Draft.Description,
		Note:        p.Draft.Note,
		CategoryID:  p.Draft.CategoryID,
		AccountID:   p.Draft.AccountID,
	}); err != nil {
		return nil, err
	}
	if err := s.store.DeleteTx(ctx, tx, userID, p.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return p.TargetTransactionID, nil
}

func toSubmitError(err error) SubmitError {
	var se *submitErr
	if errors.As(err, &se) {
		return SubmitError{Code: se.code, Message: se.msg}
	}
	codes := []struct {
		target error
		code   apierr.Code
	}{
		{ErrPendingNotFound, apierr.NotFound},
		{transactions.ErrAccountForbidden, apierr.AccountNotFound},
		{transactions.ErrAccountArchived, apierr.AccountArchived},
		{transactions.ErrCategoryForbidden, apierr.CategoryNotFound},
		{transactions.ErrCategoryTypeMismatch, apierr.CategoryTypeMismatch},
		{transactions.ErrSystemCategoryNotAllowed, apierr.CategoryNotFound},
		{transactions.ErrTransferSameAccount, apierr.TransferSameAccount},
		{transactions.ErrTransferToAccountRequired, apierr.TransferNeedsWallets},
		{transactions.ErrTransferRequiresAccount, apierr.TransferNeedsWallets},
		{tags.ErrTagNotFound, apierr.TagNotFound},
		{personal_debts.ErrEntryNotFound, apierr.DebtNotFound},
		{transactions.ErrTxNotFound, apierr.TransactionNotFound},
		{saving_goals.ErrAccountNotFound, apierr.AccountNotFound},
		{saving_goals.ErrWalletNotTracked, apierr.WalletNotTracked},
		{saving_goals.ErrGoalNotActive, apierr.GoalNotActive},
		{saving_goals.ErrGoalNotFound, apierr.NotFound},
		{projects.ErrProjectNotFound, apierr.NotFound},
		{projects.ErrEventTagNotFound, apierr.NotFound},
		{projects.ErrContactNotFound, apierr.ContactNotFound},
		{projects.ErrMemberNotInProject, apierr.MemberNotInProject},
		{projects.ErrViewerReadOnly, apierr.ForbiddenRole},
		{projects.ErrEventCompleted, apierr.EventCompleted},
		{projects.ErrProjectLocked, apierr.ProjectLocked},
		{projects.ErrUnknownTag, apierr.TagNotFound},
		{transactions.ErrTagNotFound, apierr.TagNotFound},
	}
	for _, c := range codes {
		if errors.Is(err, c.target) {
			return SubmitError{Code: c.code, Message: c.target.Error()}
		}
	}
	if apierr.FieldsOf(err) != nil {
		return SubmitError{Code: apierr.ValidationError, Message: err.Error()}
	}
	slog.Error("pending: submit failed", "error", err)
	return SubmitError{Code: apierr.SubmitFailed, Message: "Couldn't save this item"}
}

type SubmitHook func(ctx context.Context, tx pgx.Tx, userID, draftID, id uuid.UUID, kind string) error

func (s *Service) WithSubmitHook(fn SubmitHook) { s.onSubmit = fn }

func (s *Service) Store() *Store { return s.store }

func (s *Service) SubmitReady(ctx context.Context, userID uuid.UUID) (*SubmitResponse, error) {
	lst, err := s.List(ctx, userID)
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	for i := range lst.Data {
		if len(lst.Data[i].NeedsInput) == 0 {
			ids = append(ids, lst.Data[i].ID)
		}
	}
	if len(ids) == 0 {
		return s.Submit(ctx, userID, SubmitRequest{})
	}
	return s.Submit(ctx, userID, SubmitRequest{IDs: ids})
}

// freshSummary — the page's summary after a write (FE never re-fetches).
func (s *Service) freshSummary(ctx context.Context, userID uuid.UUID) (ListSummary, error) {
	rows, err := s.store.List(ctx, userID)
	if err != nil {
		return ListSummary{}, err
	}
	if err := ResolveRows(ctx, s.store.Pool(), userID, rows); err != nil {
		return ListSummary{}, err
	}
	return summarize(rows), nil
}
