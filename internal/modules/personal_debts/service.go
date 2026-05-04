package personal_debts

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

type Service struct {
	store *Store
	txs   *transactions.Service
}

func NewService(store *Store, txs *transactions.Service) *Service {
	return &Service{store: store, txs: txs}
}

func (s *Service) Create(ctx context.Context, userID uuid.UUID, req CreateRequest) (*PersonalDebt, error) {
	return s.store.Create(ctx, userID, req)
}

func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (*PersonalDebt, error) {
	return s.store.GetByID(ctx, userID, id)
}

func (s *Service) List(ctx context.Context, userID uuid.UUID, f ListFilter) (*ListResponse, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PerPage <= 0 {
		f.PerPage = 20
	}
	if f.PerPage > 100 {
		f.PerPage = 100
	}
	rows, total, err := s.store.List(ctx, userID, f)
	if err != nil {
		return nil, err
	}
	totalPages := (total + f.PerPage - 1) / f.PerPage
	if totalPages == 0 {
		totalPages = 1
	}
	return &ListResponse{
		Data: rows,
		Pagination: Pagination{
			Page: f.Page, PerPage: f.PerPage, Total: total, TotalPages: totalPages,
		},
	}, nil
}

func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, req UpdateRequest) (*PersonalDebt, error) {
	return s.store.Update(ctx, userID, id, req)
}

func (s *Service) Cancel(ctx context.Context, userID, id uuid.UUID) (*PersonalDebt, error) {
	return s.store.Cancel(ctx, userID, id)
}

func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) error {
	return s.store.Delete(ctx, userID, id)
}

// SettleResult bundles the resulting transaction (if any) and the
// updated debt row.
type SettleResult struct {
	Transaction any           `json:"transaction,omitempty"` // *transactions.TransactionDetail or nil
	Debt        *PersonalDebt `json:"debt"`
}

// Settle records a settlement event. If `req.AccountID` is provided AND
// non-zero, a transaction is created (income for owed_to_me, expense for
// i_owe) AND the debt's settled_amount is bumped via the auto-bump hook.
//
// Otherwise this falls back to a direct edit (just bump settled_amount,
// no transaction). Used for non-cash settles: forgiveness, barter, or
// users who don't track a cash account.
func (s *Service) Settle(ctx context.Context, userID, id uuid.UUID, req SettleRequest, recordTransaction bool) (*SettleResult, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	current, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if current.Status == StatusSettled {
		return nil, ErrAlreadySettled
	}
	if current.Status == StatusCancelled {
		return nil, ErrAlreadyCancelled
	}

	outstanding := current.Amount - current.SettledAmount
	amount := outstanding
	if req.Amount != nil {
		amount = *req.Amount
	}
	if amount <= 0 {
		return nil, errors.New("amount must be > 0")
	}
	if amount > outstanding {
		return nil, ErrOverpayment
	}

	date := req.Date
	if date == nil {
		var today string
		if err := tx.QueryRow(ctx, `SELECT to_char(CURRENT_DATE, 'YYYY-MM-DD')`).Scan(&today); err != nil {
			return nil, fmt.Errorf("today: %w", err)
		}
		date = &today
	}

	var txn any
	if recordTransaction && req.AccountID != uuid.Nil {
		// Direction-aware transaction: i_owe → expense, owed_to_me → income
		var txType transactions.TxType
		switch current.Direction {
		case DirectionIOwe:
			txType = transactions.TypeExpense
		case DirectionOwedToMe:
			txType = transactions.TypeIncome
		default:
			return nil, fmt.Errorf("unknown direction: %s", current.Direction)
		}

		createReq := transactions.CreateRequest{
			Type:      txType,
			AccountID: req.AccountID,
			Amount:    amount,
			Date:      *date,
			Note:      req.Note,
		}
		txn, err = s.txs.CreateInTxWithSourceDebt(ctx, tx, userID, id, createReq)
		if err != nil {
			return nil, err
		}
		// CreateInTxWithSourceDebt's hook bumps settled_amount via
		// AutoBumpInTx — but only when the user_id + debt_id match. The
		// auto-bumper handled it; nothing to do here.
	} else {
		// Direct edit path: no transaction, just bump.
		if _, err := s.store.SettleInTx(ctx, tx, userID, id, amount); err != nil {
			return nil, err
		}
	}

	updated, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return &SettleResult{Transaction: txn, Debt: updated}, nil
}

func (s *Service) People(ctx context.Context, userID uuid.UUID) (*PeopleResponse, error) {
	rows, totalOwed, totalIOwe, err := s.store.People(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &PeopleResponse{
		Data:          rows,
		TotalOwedToMe: totalOwed,
		TotalIOwe:     totalIOwe,
		NetPosition:   totalOwed - totalIOwe,
		Currency:      "THB", // 1c+ multi-currency
	}, nil
}

// --- Hooks for transactions module ---

// CreateForTransactionTx is called by transactions.Service when an
// expense is created with `splits` array. Inserts one debt row per
// split entry on the splitter's side ('owed_to_me'). Optionally
// creates the partner-side row ('i_owe') for linked contacts.
func (s *Service) CreateForTransactionTx(
	ctx context.Context, tx pgx.Tx,
	parentTxID, userID uuid.UUID,
	parentCurrency string,
	inputs []transactions.SplitInput,
) error {
	if len(inputs) == 0 {
		return nil
	}
	for i := range inputs {
		in := inputs[i]
		// 1. Splitter's side — always created.
		_, err := s.store.CreateAttachedTx(ctx, tx, userID, DirectionOwedToMe,
			in.ContactID, in.PersonName,
			&parentTxID, nil, nil,
			in.OwedAmount, parentCurrency, nil,
		)
		if err != nil {
			return err
		}

		// 2. Partner's side — only if contact is linked.
		if in.ContactID != nil {
			var linkedUserID *uuid.UUID
			if err := tx.QueryRow(ctx,
				`SELECT linked_user_id FROM contacts WHERE id = $1`,
				*in.ContactID).Scan(&linkedUserID); err != nil {
				continue // contact missing or not linked
			}
			if linkedUserID == nil || *linkedUserID == userID {
				continue
			}
			// Find the contact in the partner's book that links back to
			// the splitter (us). Lookup by linked_user_id = userID.
			var partnerContactID *uuid.UUID
			var partnerPersonName string
			row := tx.QueryRow(ctx,
				`SELECT id, COALESCE(nickname, display_name) FROM contacts
				 WHERE user_id = $1 AND linked_user_id = $2 LIMIT 1`,
				*linkedUserID, userID)
			var pcID uuid.UUID
			if err := row.Scan(&pcID, &partnerPersonName); err == nil {
				partnerContactID = &pcID
			} else {
				// Partner has no contact for us yet; fall back to user's display name.
				_ = tx.QueryRow(ctx,
					`SELECT display_name FROM users WHERE id = $1`, userID).
					Scan(&partnerPersonName)
				if partnerPersonName == "" {
					partnerPersonName = "Unknown"
				}
			}
			_, err := s.store.CreateAttachedTx(ctx, tx, *linkedUserID, DirectionIOwe,
				partnerContactID, partnerPersonName,
				nil, // partner has no transaction in their book
				nil, nil,
				in.OwedAmount, parentCurrency, nil,
			)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// AutoBumpInTx is the hook the transactions module calls when a transaction
// with `source_personal_debt_id` lands. Bumps that debt's settled_amount.
func (s *Service) AutoBumpInTx(
	ctx context.Context, tx pgx.Tx, userID, debtID uuid.UUID, delta float64,
) error {
	return s.store.AutoBumpInTx(ctx, tx, userID, debtID, delta)
}

// ValidateOwnership checks the caller actually owns the debt. Used by
// transactions module before allowing source_personal_debt_id to be set.
// Returns the debt's amount/settled_amount so the transaction service
// can validate sane settle amount.
func (s *Service) ValidateOwnership(
	ctx context.Context, tx pgx.Tx, userID, debtID uuid.UUID,
) (direction string, outstanding float64, err error) {
	q := `SELECT direction, (amount - settled_amount) AS outstanding
		FROM personal_debts WHERE id = $1 AND user_id = $2 AND status = 'open'`
	err = tx.QueryRow(ctx, q, debtID, userID).Scan(&direction, &outstanding)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, ErrDebtNotFound
	}
	return direction, outstanding, err
}

// SumOwedToMeForTransaction is a public hook used by the transactions
// summary endpoint to compute "my share" — for transactions that
// originated split debts on the caller's side, the report-time amount
// should subtract the open lent amount.
func (s *Service) SumOwedToMeForTransaction(
	ctx context.Context, userID, txID uuid.UUID,
) (float64, error) {
	return s.store.SumDebtsForTransaction(ctx, userID, txID)
}

// DebtsForTransaction is exposed so the FE transaction-detail view can
// pull the split breakdown.
func (s *Service) DebtsForTransaction(
	ctx context.Context, userID, txID uuid.UUID,
) ([]PersonalDebt, error) {
	return s.store.DebtsForTransaction(ctx, userID, txID)
}
