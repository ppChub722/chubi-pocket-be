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

// CounterpartyInput — the debt a debt rule books (without debt_plan_id): exactly one of a contact /
// typed name / institution.
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

// LineInput — a rule (POST / PUT /recurring/{id}/rules, an entry of rules[]): a full replace.
type LineInput struct {
	ID *uuid.UUID `json:"id"`
	// Type — expense | income | transfer | saving | debt; with its category (expense / income), tags,
	// goal (saving) and debt target (debt: debt_plan_id, else counterparty + debt_direction).
	Type          *string            `json:"type"`
	CategoryID    *uuid.UUID         `json:"category_id"`
	TagIDs        []uuid.UUID        `json:"tag_ids"`
	GoalID        *uuid.UUID         `json:"goal_id"`
	Counterparty  *CounterpartyInput `json:"counterparty"`
	DebtDirection *string            `json:"debt_direction"`
	DebtPlanID    *uuid.UUID         `json:"debt_plan_id"`
	Amount        *float64           `json:"amount"`
	AmountMode    *string            `json:"amount_mode"` // fixed | ask (default: ask when no amount is sent)
	// RepaysRuleID — type expense: this rule pays back the person of that person-paid rule of the
	// same recurring (repay_paid_by).
	RepaysRuleID *uuid.UUID  `json:"repays_rule_id"`
	Source       SourceInput `json:"source"`
	ToAccountID  *uuid.UUID  `json:"to_account_id"`
	DebtKind     *string     `json:"debt_kind"` // debt: debt (borrow / lend) | repayment (default)
	Schedule     *Schedule   `json:"schedule"`
	// Notify / AutoAccept — null = the user's default.
	Notify     *bool `json:"notify"`
	AutoAccept *bool `json:"auto_accept"`
}

type PlanInput struct {
	Principal         float64  `json:"principal"`
	Count             int      `json:"count"`
	StartDate         string   `json:"start_date"`
	InterestMethod    string   `json:"interest_method"`
	InterestRate      *float64 `json:"interest_rate"`
	InstallmentAmount *float64 `json:"installment_amount"`
	// Installments — interest_method manual: every installment as typed (count rows).
	Installments []InstallmentInput `json:"installments"`
	// OpeningAccountID / OpeningDate — a cash loan's opening debt row: its wallet transaction (the
	// money lent / borrowed; absent = ledger only) and date (default today; a purchase's expense
	// takes it too).
	OpeningAccountID *uuid.UUID `json:"opening_account_id"`
	OpeningDate      *string    `json:"opening_date"`
}

type InstallmentInput struct {
	DueDate   *string  `json:"due_date"`
	Amount    *float64 `json:"amount"`
	Principal *float64 `json:"principal"`
	Interest  *float64 `json:"interest"`
}

type RecurringInput struct {
	Name         *string          `json:"name"`
	Description  *string          `json:"description"`
	Note         *string          `json:"note"`
	IconCode     *shared.IconCode `json:"icon_code"`
	TrackingType *string          `json:"tracking_type"`
	Lines        []LineInput      `json:"rules"`

	present map[string]bool
}

// RuleCreateInput — POST /recurring-rules: a rule made from another page, into an existing recurring
// (recurring_id) or a new one (new_recurring).
type RuleCreateInput struct {
	RecurringID  *uuid.UUID      `json:"recurring_id"`
	NewRecurring *RecurringInput `json:"new_recurring"`
	Rule         LineInput       `json:"rule"`
}

func (in *RecurringInput) has(k string) bool { return in.present[k] }

// ---- output ----

// Counterparty — a debt rule's counterparty, page-ready.
type Counterparty struct {
	Key         string                 `json:"key"`  // c: / n: / i:, the debt page key
	Kind        string                 `json:"kind"` // person | institution | card
	Name        string                 `json:"name"`
	Person      *shared.PersonRef      `json:"person"`
	Institution *shared.InstitutionRef `json:"institution"`
	Account     *shared.AccountRef     `json:"account"`
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

// Line — one rule, page-ready.
type Line struct {
	ID          uuid.UUID `json:"id"`
	RecurringID uuid.UUID `json:"recurring_id"`
	// Seq — "Rule n": its place among its recurring's rules (1-based, oldest first).
	Seq           int                   `json:"seq"`
	Type          string                `json:"type"`
	CategoryID    *uuid.UUID            `json:"category_id"`
	Category      *shared.CategoryRef   `json:"category"`
	TagIDs        []uuid.UUID           `json:"tag_ids"`
	Tags          []shared.TagRef       `json:"tags"`
	GoalID        *uuid.UUID            `json:"goal_id"`
	Goal          *shared.SavingGoalRef `json:"goal"`
	Counterparty  *Counterparty         `json:"counterparty"`
	DebtDirection *string               `json:"debt_direction"` // the debt's side: paid = they owe me
	DebtPlanID    *uuid.UUID            `json:"debt_plan_id"`
	Amount        *float64              `json:"amount"`
	// AmountMode — fixed | ask | schedule (a plan's installments) | follow (the latest round of the
	// rule it repays).
	AmountMode string `json:"amount_mode"`
	// PrefillHint — an asked person repayment next to another rule of that person: the net balance,
	// shown, not filled.
	PrefillHint  *float64           `json:"prefill_hint"`
	Action       string             `json:"action"` // expense | income | transfer | saving | paid_by | repay_paid_by | repay | collect | borrow | lend
	RepaysRuleID *uuid.UUID         `json:"repays_rule_id"`
	Source       LineSource         `json:"source"`
	Account      *shared.AccountRef `json:"account"`
	PaidBy       *shared.PersonRef  `json:"paid_by"`
	ToAccountID  *uuid.UUID         `json:"to_account_id"`
	ToAccount    *shared.AccountRef `json:"to_account"`
	DebtKind     *string            `json:"debt_kind"`
	Schedule     Schedule           `json:"schedule"`
	// EndText — a rule that stops by itself: goal_reached (saving into a goal) | debt_cleared
	// (repaying a debt); null = the schedule's end.
	EndText *string `json:"end_text"`
	// Notify / AutoAccept — as stored (null = the user's default); NotifyOn / AutoAcceptOn — in effect.
	Notify            *bool        `json:"notify"`
	AutoAccept        *bool        `json:"auto_accept"`
	NotifyOn          bool         `json:"notify_on"`
	AutoAcceptOn      bool         `json:"auto_accept_on"`
	Status            string       `json:"status"` // active | paused | archived | done
	PauseReason       *string      `json:"pause_reason"`
	NextDate          *string      `json:"next_date"`
	NextAmount        *float64     `json:"next_amount"`
	DaysUntil         *int         `json:"days_until"`
	DueCount          int          `json:"due_count"`
	MonthlyAmount     *float64     `json:"monthly_amount"`
	YearlyCost        float64      `json:"yearly_cost"`
	Progress          Progress     `json:"progress"`
	Chain             []ChainStep  `json:"chain"`
	EndingSoon        bool         `json:"ending_soon"`
	Counts            StatusCounts `json:"counts"`
	CurrentOccurrence *Occurrence  `json:"current_occurrence"`
	Actions           LineActions  `json:"actions"`
	CreatedAt         time.Time    `json:"created_at"`
	UpdatedAt         time.Time    `json:"updated_at"`
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

type ItemActions struct {
	Pause   bool `json:"pause"`
	Resume  bool `json:"resume"`
	Archive bool `json:"archive"`
	Restore bool `json:"restore"`
	Delete  bool `json:"delete"`
	AddLine bool `json:"add_rule"`
}

// PlanView — a debt plan, page-ready (GET /debt-plans/{id}; a debt item's plan).
type PlanView struct {
	ID                uuid.UUID           `json:"id"`
	Name              string              `json:"name"`
	Description       *string             `json:"description"`
	Note              *string             `json:"note"`
	CategoryID        *uuid.UUID          `json:"category_id"`
	Category          *shared.CategoryRef `json:"category"`
	Direction         string              `json:"direction"` // received = I owe | paid = they owe me
	Origin            string              `json:"origin"`    // purchase | cash
	Creditor          Counterparty        `json:"creditor"`
	RecurringID       *uuid.UUID          `json:"recurring_id"` // the recurring of the first rule that pays it
	Principal         float64             `json:"principal"`
	Count             int                 `json:"count"`
	StartDate         string              `json:"start_date"`
	InterestMethod    string              `json:"interest_method"`
	InterestRate      *float64            `json:"interest_rate"`
	InstallmentAmount *float64            `json:"installment_amount"`
	Purchase          *PlanPurchase       `json:"purchase"` // the purchase: on the card, or the expense of a purchase on credit
	Closed            bool                `json:"closed"`
	Done              int                 `json:"done"` // installments ticked paid
	LeftCount         int                 `json:"left_count"`
	PaidPrincipal     float64             `json:"paid_principal"`
	PaidTotal         float64             `json:"paid_total"` // principal + interest of the ticked installments
	LeftPrincipal     float64             `json:"left_principal"`
	LeftInterest      float64             `json:"left_interest"`
	LeftTotal         float64             `json:"left_total"`
	LastDate          string              `json:"last_date"`
	Next              *Installment        `json:"next"`
	Due               []Installment       `json:"due"` // past their date, not ticked
	Chain             []InstallmentStatus `json:"chain"`
	Installments      []Installment       `json:"installments"`
	CreatedAt         time.Time           `json:"created_at"`
}

// PlanPurchase — the expense a card installment was made from (quick create ผ่อน).
type PlanPurchase struct {
	ID          uuid.UUID           `json:"id"`
	Amount      float64             `json:"amount"`
	Date        string              `json:"date"`
	Description *string             `json:"description"`
	Category    *shared.CategoryRef `json:"category"`
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

// ItemPage — GET /recurring/{id} and every recurring / rule write.
type ItemPage struct {
	ID           uuid.UUID        `json:"id"`
	Name         string           `json:"name"`
	Description  *string          `json:"description"`
	Note         *string          `json:"note"`
	IconCode     *shared.IconCode `json:"icon_code"`
	TrackingType string           `json:"tracking_type"`
	Status       string           `json:"status"` // active | paused | archived | done
	// Touches — what its rules touch on other pages (goal / person / institution / plan / card);
	// empty = self-made (ตั้งเอง).
	Touches         []Touch                `json:"touches"`
	Plan            *PlanView              `json:"plan"` // the plan of its first plan rule
	Lines           []Line                 `json:"rules"`
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

// Touch — one page a recurring's rules show on besides the recurring list.
type Touch struct {
	Kind string    `json:"kind"` // goal | person | institution | plan | card
	ID   uuid.UUID `json:"id"`
	Key  string    `json:"key,omitempty"` // a person / institution: the debt page key
	Name string    `json:"name"`
}

// RecurringSummary — one row of the recurring list (a recurring, not a rule).
type RecurringSummary struct {
	ID           uuid.UUID           `json:"id"`
	Name         string              `json:"name"`
	TrackingType string              `json:"tracking_type"`
	IconCode     *shared.IconCode    `json:"icon_code"`
	Status       string              `json:"status"`
	PauseReason  *string             `json:"pause_reason"`
	Category     *shared.CategoryRef `json:"category"` // the single rule's
	LinesCount   int                 `json:"rules_count"`
	// Related — any rule touches a goal / person / institution / plan or pays into a card
	// (เกี่ยวกับหน้าอื่น); Touches says what.
	Related bool    `json:"related"`
	Touches []Touch `json:"touches"`
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
	CurrentOccurrence *Occurrence `json:"current_occurrence"`
	DueCount          int         `json:"due_count"`
	EndingSoon        bool        `json:"ending_soon"`
	Plan              *PlanBrief  `json:"plan"`
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
