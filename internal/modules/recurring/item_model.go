package recurring

import (
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// ---- input (POST / PUT /recurring, lines) ----

// PersonInput — a person: one of my contacts, else a typed name.
type PersonInput struct {
	ContactID  *uuid.UUID `json:"contact_id"`
	PersonName *string    `json:"person_name"`
}

// CounterpartyInput — a debt item's counterparty: exactly one of contact / typed name / institution.
type CounterpartyInput struct {
	ContactID     *uuid.UUID `json:"contact_id"`
	PersonName    *string    `json:"person_name"`
	InstitutionID *uuid.UUID `json:"institution_id"`
}

// SourceInput — where a line's money comes from: a wallet, or a person paying for me.
type SourceInput struct {
	AccountID *uuid.UUID   `json:"account_id"`
	PaidBy    *PersonInput `json:"paid_by"`
}

// Schedule — when a line comes round: the recurrence rule (freq, interval, weekdays, monthly,
// dates; as today's rule editor) + start_date + end.
type Schedule struct {
	RecurrenceRule
	StartDate string         `json:"start_date"`
	End       *RecurrenceEnd `json:"end"`
}

type LineInput struct {
	ID          *uuid.UUID  `json:"id"`
	Amount      *float64    `json:"amount"`
	Source      SourceInput `json:"source"`
	ToAccountID *uuid.UUID  `json:"to_account_id"`
	DebtKind    *string     `json:"debt_kind"`
	Schedule    *Schedule   `json:"schedule"`
	AutoCreate  *bool       `json:"auto_create"`
}

type PlanInput struct {
	Principal         float64    `json:"principal"`
	Count             int        `json:"count"`
	StartDate         string     `json:"start_date"`
	InterestMethod    string     `json:"interest_method"`
	InterestRate      *float64   `json:"interest_rate"`
	InstallmentAmount *float64   `json:"installment_amount"`
	CardAccountID     *uuid.UUID `json:"card_account_id"`
	// Installments — interest_method manual: every installment as typed (count rows).
	Installments []InstallmentInput `json:"installments"`
	// OpeningAccountID / OpeningDate — the opening debt row (written once, when the item gets its
	// plan, not on a card): its wallet transaction (the money lent / borrowed) and date (default
	// today). Absent = a ledger-only row.
	OpeningAccountID *uuid.UUID `json:"opening_account_id"`
	OpeningDate      *string    `json:"opening_date"`
}

type InstallmentInput struct {
	DueDate   *string  `json:"due_date"`
	Amount    *float64 `json:"amount"`
	Principal *float64 `json:"principal"`
	Interest  *float64 `json:"interest"`
}

type ReminderInput struct {
	Anchor     string `json:"anchor"`
	DaysBefore int    `json:"days_before"`
	Time       string `json:"time"`
}

type RecurringInput struct {
	Name          *string            `json:"name"`
	Description   *string            `json:"description"`
	Note          *string            `json:"note"`
	IconCode      *shared.IconCode   `json:"icon_code"`
	Type          *string            `json:"type"`
	CategoryID    *uuid.UUID         `json:"category_id"`
	TagIDs        []uuid.UUID        `json:"tag_ids"`
	GoalID        *uuid.UUID         `json:"goal_id"`
	Counterparty  *CounterpartyInput `json:"counterparty"`
	DebtDirection *string            `json:"debt_direction"`
	Plan          *PlanInput         `json:"plan"`
	Reminders     []ReminderInput    `json:"reminders"`
	Lines         []LineInput        `json:"lines"`

	present map[string]bool
}

func (in *RecurringInput) has(k string) bool { return in.present[k] }

// ---- output ----

// Counterparty — a debt item's counterparty, page-ready.
type Counterparty struct {
	Key         string                 `json:"key"` // c: / n: / i:, the debt page key
	Kind        string                 `json:"kind"`
	Name        string                 `json:"name"`
	Person      *shared.PersonRef      `json:"person"`
	Institution *shared.InstitutionRef `json:"institution"`
	// Net — the counterparty's balance now (> 0 = they owe me).
	Net float64 `json:"net"`
}

type LineSource struct {
	AccountID *uuid.UUID   `json:"account_id"`
	PaidBy    *PersonInput `json:"paid_by"`
}

type LineActions struct {
	Pause   bool `json:"pause"`
	Resume  bool `json:"resume"`
	Archive bool `json:"archive"`
	Restore bool `json:"restore"`
	Extend  bool `json:"extend"`
	Delete  bool `json:"delete"`
}

// Line — one line of an item, page-ready.
type Line struct {
	ID                uuid.UUID          `json:"id"`
	Amount            *float64           `json:"amount"`
	Source            LineSource         `json:"source"`
	Account           *shared.AccountRef `json:"account"`
	PaidBy            *shared.PersonRef  `json:"paid_by"`
	ToAccountID       *uuid.UUID         `json:"to_account_id"`
	ToAccount         *shared.AccountRef `json:"to_account"`
	DebtKind          *string            `json:"debt_kind"`
	Schedule          Schedule           `json:"schedule"`
	AutoCreate        bool               `json:"auto_create"`
	Status            string             `json:"status"` // active | paused | archived | done
	PauseReason       *string            `json:"pause_reason"`
	NextDate          *string            `json:"next_date"`
	NextAmount        *float64           `json:"next_amount"`
	DaysUntil         *int               `json:"days_until"`
	DueCount          int                `json:"due_count"`
	MonthlyAmount     *float64           `json:"monthly_amount"`
	YearlyCost        float64            `json:"yearly_cost"`
	Progress          Progress           `json:"progress"`
	Chain             []ChainStep        `json:"chain"`
	EndingSoon        bool               `json:"ending_soon"`
	Counts            StatusCounts       `json:"counts"`
	CurrentOccurrence *Occurrence        `json:"current_occurrence"`
	Actions           LineActions        `json:"actions"`
	CreatedAt         time.Time          `json:"created_at"`
	UpdatedAt         time.Time          `json:"updated_at"`
}

type NextRound struct {
	RecurringRuleID uuid.UUID `json:"recurring_rule_id"`
	Date            string    `json:"date"`
	Amount          *float64  `json:"amount"`
	DaysUntil       int       `json:"days_until"`
}

// ItemStats — the item page's numbers over every line's recorded rounds.
type ItemStats struct {
	PaidCount  int      `json:"paid_count"`
	PaidTotal  float64  `json:"paid_total"`
	LeftTotal  *float64 `json:"left_total"` // only when every line has an end
	LastDate   *string  `json:"last_date"`
	AvgAmount  *float64 `json:"avg_amount"`
	LastAmount *float64 `json:"last_amount"`
	MaxAmount  *float64 `json:"max_amount"`
	Series     []Point  `json:"series"`
}

type Reminder struct {
	ID         uuid.UUID `json:"id"`
	Anchor     string    `json:"anchor"`
	DaysBefore int       `json:"days_before"`
	Time       string    `json:"time"`
}

type ItemActions struct {
	Pause   bool `json:"pause"`
	Resume  bool `json:"resume"`
	Archive bool `json:"archive"`
	Restore bool `json:"restore"`
	Delete  bool `json:"delete"`
	AddLine bool `json:"add_line"`
}

// PlanView — a debt item's plan (information; the balance counts debt rows only).
type PlanView struct {
	Principal         float64             `json:"principal"`
	Count             int                 `json:"count"`
	StartDate         string              `json:"start_date"`
	InterestMethod    string              `json:"interest_method"`
	InterestRate      *float64            `json:"interest_rate"`
	InstallmentAmount *float64            `json:"installment_amount"`
	CardAccountID     *uuid.UUID          `json:"card_account_id"`
	CardAccount       *shared.AccountRef  `json:"card_account"`
	Closed            bool                `json:"closed"`
	Done              int                 `json:"done"` // installments ticked paid
	LeftCount         int                 `json:"left_count"`
	PaidPrincipal     float64             `json:"paid_principal"`
	LeftPrincipal     float64             `json:"left_principal"`
	LeftInterest      float64             `json:"left_interest"`
	LeftTotal         float64             `json:"left_total"`
	LastDate          string              `json:"last_date"`
	Next              *Installment        `json:"next"`
	Due               []Installment       `json:"due"` // past their date, not ticked
	Chain             []InstallmentStatus `json:"chain"`
}

type InstallmentStatus struct {
	Seq    int    `json:"seq"`
	Status string `json:"status"`
}

type Installment struct {
	Seq            int        `json:"seq"`
	DueDate        string     `json:"due_date"`
	Amount         float64    `json:"amount"`
	Principal      float64    `json:"principal"`
	Interest       float64    `json:"interest"`
	Status         string     `json:"status"` // upcoming | due | paid | skipped
	PaidAt         *time.Time `json:"paid_at"`
	PersonalDebtID *uuid.UUID `json:"personal_debt_id"`
	Edited         bool       `json:"edited"`
}

// ItemPage — GET /recurring/{id} and every item write.
type ItemPage struct {
	ID              uuid.UUID              `json:"id"`
	Name            string                 `json:"name"`
	Description     *string                `json:"description"`
	Note            *string                `json:"note"`
	IconCode        *shared.IconCode       `json:"icon_code"`
	Type            string                 `json:"type"`
	CategoryID      *uuid.UUID             `json:"category_id"`
	Category        *shared.CategoryRef    `json:"category"`
	TagIDs          []uuid.UUID            `json:"tag_ids"`
	Tags            []shared.TagRef        `json:"tags"`
	GoalID          *uuid.UUID             `json:"goal_id"`
	Goal            *shared.SavingGoalRef  `json:"goal"`
	Counterparty    *Counterparty          `json:"counterparty"`
	DebtDirection   *string                `json:"debt_direction"`
	Status          string                 `json:"status"` // active | paused | archived | done
	Plan            *PlanView              `json:"plan"`
	Reminders       []Reminder             `json:"reminders"`
	Lines           []Line                 `json:"lines"`
	Stats           ItemStats              `json:"stats"`
	DueCount        int                    `json:"due_count"`
	Next            *NextRound             `json:"next"`
	MonthTotal      float64                `json:"month_total"`
	Actions         ItemActions            `json:"actions"`
	CreatedAt       time.Time              `json:"created_at"`
	UpdatedAt       time.Time              `json:"updated_at"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
	Effects         shared.Effects         `json:"effects"`
}

type PlanBrief struct {
	Done  int `json:"done"`
	Count int `json:"count"`
}

// RecurringSummary — one row of the recurring list (an item, not a line).
type RecurringSummary struct {
	ID          uuid.UUID           `json:"id"`
	Name        string              `json:"name"`
	Type        string              `json:"type"`
	IconCode    *shared.IconCode    `json:"icon_code"`
	Status      string              `json:"status"`
	PauseReason *string             `json:"pause_reason"`
	Category    *shared.CategoryRef `json:"category"`
	LinesCount  int                 `json:"lines_count"`
	// Amount / Account / Chain — the single line's (null / empty with 2+ lines).
	Amount     *float64           `json:"amount"`
	Account    *shared.AccountRef `json:"account"`
	PaidBy     *shared.PersonRef  `json:"paid_by"`
	Chain      []ChainStep        `json:"chain"`
	Progress   *Progress          `json:"progress"`
	MonthTotal float64            `json:"month_total"`
	Next       *NextRound         `json:"next"`
	// CurrentOccurrence — the round a transaction links to now: the oldest due one over the lines,
	// else the nearest upcoming one (its line in recurring_rule_id); null when none is near.
	CurrentOccurrence *Occurrence   `json:"current_occurrence"`
	DueCount          int           `json:"due_count"`
	EndingSoon        bool          `json:"ending_soon"`
	Counterparty      *Counterparty `json:"counterparty"`
	// DebtDirection — type debt: the debt's side (paid = they owe me, received = I owe them).
	DebtDirection *string               `json:"debt_direction"`
	Goal          *shared.SavingGoalRef `json:"goal"`
	Plan          *PlanBrief            `json:"plan"`
	// Likely — for_type only: it matches the transaction (category / amount within 5%).
	Likely    bool      `json:"likely"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type SummaryList struct {
	Data       []RecurringSummary `json:"data"`
	Counts     ListCounts         `json:"counts"`
	Summary    ListSummary        `json:"summary"`
	Pagination shared.Page        `json:"pagination"`
}

type RecurringDeleteResult struct {
	ID              uuid.UUID              `json:"id"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
	Effects         shared.Effects         `json:"effects"`
}
