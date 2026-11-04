package projects

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

const (
	StatusActive    = "active"
	StatusCompleted = "completed"
	StatusCancelled = "cancelled"
	StatusArchived  = "archived"
)

const (
	RoleOwner       = "owner"
	RoleContributor = "contributor"
	RoleViewer      = "viewer"
)

const (
	MemberStatusPending = "pending"
	MemberStatusActive  = "active"
	MemberStatusLeft    = "left"
)

type Project struct {
	ID            uuid.UUID
	OwnerUserID   uuid.UUID
	Name          string
	Description   *string
	Note          *string
	StartDate     *string
	EndDate       *string
	Status        string
	IconCode      *shared.IconCode
	PlannedAmount *float64
	CreatedAt     time.Time
	UpdatedAt     time.Time
	CompletedAt   *time.Time // set = locked for good
}

type ProjectMember struct {
	ID          uuid.UUID
	ProjectID   uuid.UUID
	UserID      *uuid.UUID
	DisplayName string
	Role        string
	Status      string
	IconCode    *shared.IconCode
	InvitedAt   *time.Time
	JoinedAt    *time.Time
	LeftAt      *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Event struct {
	ID               uuid.UUID        `json:"id"`
	OwnerUserID      uuid.UUID        `json:"owner_user_id"`
	Name             string           `json:"name"`
	IconCode         *shared.IconCode `json:"icon_code"`
	Description      *string          `json:"description"`
	Note             *string          `json:"note"`
	Status           string           `json:"status"`
	StartDate        *string          `json:"start_date"`
	EndDate          *string          `json:"end_date"`
	PlannedAmount    *float64         `json:"planned_amount"`
	MembersCount     int              `json:"members_count"`
	TotalExpense     float64          `json:"total_expense"`
	TotalIncome      float64          `json:"total_income"`
	MyNet            float64          `json:"my_net"`
	MyPaid           float64          `json:"my_paid"`
	MyShare          float64          `json:"my_share"`
	MyReceivable     float64          `json:"my_receivable"`
	MyPayable        float64          `json:"my_payable"`
	Remaining        *float64         `json:"remaining"`
	Tags             []shared.TagRef  `json:"tags"`
	MyMemberID       *uuid.UUID       `json:"my_member_id"`
	MyRole           *string          `json:"my_role"`
	IsOwner          bool             `json:"is_owner"`
	IsLocked         bool             `json:"is_locked"`
	CanAddBill       bool             `json:"can_add_bill"`
	CanEdit          bool             `json:"can_edit"`
	CanManageMembers bool             `json:"can_manage_members"`
	CompletedAt      *time.Time       `json:"completed_at"`
	CanComplete      bool             `json:"can_complete"`
	// EmptyPersonBillCount — bills with a removed member's spot to assign (ต้องจัดการ).
	EmptyPersonBillCount int `json:"empty_person_bill_count"`
	// UnclaimedCount — bills I am on that are not in my books yet.
	UnclaimedCount int `json:"unclaimed_count"`
	// MyDefaults — my category / wallet for auto claim in this event.
	MyDefaults *MyDefaults `json:"my_defaults"`
	CreatedAt  time.Time   `json:"created_at"`
	UpdatedAt  time.Time   `json:"updated_at"`

	myStatus string
}

// MyDefaults — a member's own defaults for an event (auto claim).
type MyDefaults struct {
	CategoryID *uuid.UUID `json:"category_id"`
	AccountID  *uuid.UUID `json:"account_id"`
}

type EventList struct {
	Data       []Event          `json:"data"`
	Summary    EventListSummary `json:"summary"`
	Pagination shared.Page      `json:"pagination"`
}

type EventMember struct {
	ID     uuid.UUID     `json:"id"`
	UserID *uuid.UUID    `json:"user_id"`
	Name   string        `json:"name"`
	Avatar shared.Avatar `json:"avatar"`
	IsMe   bool          `json:"is_me"`
	Linked bool          `json:"linked"`
	// ContactID — MY contact for this member (bound by me, or my contact linked to that user).
	ContactID *uuid.UUID `json:"contact_id"`
	// ContactLinked — that contact of mine uses the app (it can be invited).
	ContactLinked bool `json:"contact_linked"`
	// Invited — invited and not joined yet (the invite can be sent again).
	Invited bool   `json:"invited"`
	Role    string `json:"role"`
	Status    string     `json:"status"`
	InvitedAt *time.Time `json:"invited_at"`
	JoinedAt  *time.Time `json:"joined_at"`
	LeftAt    *time.Time `json:"left_at"`
	// RemovedAt — the owner removed them (left_at alone = they left themselves).
	RemovedAt *time.Time `json:"removed_at"`
}

type EventMemberList struct {
	Data []EventMember `json:"data"`
}

type EventTagRef struct {
	ID       uuid.UUID        `json:"id"`
	Name     string           `json:"name"`
	IconCode *shared.IconCode `json:"icon_code"`
	Mine     bool             `json:"mine"`
}

type EventTag struct {
	EventTagRef
	Count int `json:"count"`
}

type EventTagList struct {
	Data []EventTag `json:"data"`
}

type EventPosition struct {
	Member shared.MemberRef `json:"member"`
	Paid   float64          `json:"paid"`
	Share  float64          `json:"share"`
	// Sent / Received — paybacks this member made / got; Net = what the others owe them overall.
	Sent       float64 `json:"sent"`
	Received   float64 `json:"received"`
	Net        float64 `json:"net"`
	Receivable float64 `json:"receivable"`
	Payable    float64 `json:"payable"`
}

type EventTagTotal struct {
	Tag   *EventTagRef `json:"tag"`
	Total float64      `json:"total"`
	Share float64      `json:"share"`
	Count int          `json:"count"`
}

type EventSummary struct {
	TotalExpense     float64         `json:"total_expense"`
	TotalIncome      float64         `json:"total_income"`
	TransactionCount int             `json:"transaction_count"`
	MemberCount      int             `json:"member_count"`
	PlannedAmount    *float64        `json:"planned_amount"`
	SpentNet         *float64        `json:"spent_net"`
	Remaining        *float64        `json:"remaining"`
	MyPosition       *EventPosition  `json:"my_position"`
	Members          []EventPosition `json:"members"`
	ByTag            []EventTagTotal `json:"by_tag"`
}

// PaperView — a paper (split or event bill) as one user sees it, with their books.
type PaperView struct {
	shared.Paper
	Event *shared.EventRef `json:"event"`
	Tags  []EventTagRef    `json:"tags"`
	// Me — my person on it (nil when I am not on it).
	Me *shared.PaperPerson `json:"me"`
	// MyBooks — what my books hold for it now.
	MyBooks MyPaperBooks `json:"my_books"`
	Rights  PaperRights  `json:"rights"`
}

// MyPaperBooks — my transaction and debts made from this paper, and whether they match it now.
type MyPaperBooks struct {
	Transaction *transactions.TransactionDetail `json:"transaction"`
	DebtIDs     []uuid.UUID                     `json:"debt_ids"`
	// Claimed — my books hold every piece the paper gives me (a tx, and debts on a split bill).
	Claimed bool `json:"claimed"`
	// Missing — what a claim would add now: transaction | debt.
	Missing []string `json:"missing"`
	InSync  bool     `json:"in_sync"`
}

// PaperRights — what I may do with this paper.
type PaperRights struct {
	Edit         bool `json:"edit"`
	RemovePeople bool `json:"remove_people"`
	Move         bool `json:"move"`
	Claim        bool `json:"claim"`
	Leave        bool `json:"leave"`
	// Join — a split bill I am invited to and have not answered.
	Join bool `json:"join"`
}

type PaperList struct {
	Data       []PaperView `json:"data"`
	Pagination shared.Page `json:"pagination"`
}

// PaperPersonInput — one person on a paper. Exactly one of: me, member_id (event bills), contact_id,
// person_name; person_id keeps an existing row (an edit).
type PaperPersonInput struct {
	PersonID   *uuid.UUID `json:"person_id"`
	Me         bool       `json:"me"`
	MemberID   *uuid.UUID `json:"member_id"`
	ContactID  *uuid.UUID `json:"contact_id"`
	PersonName *string    `json:"person_name" binding:"omitempty,max=100"`
	Share      float64    `json:"share"`
	Paid       float64    `json:"paid"`
}

// PaperInput — the paper's own fields (create and edit).
type PaperInput struct {
	Type        string             `json:"type"         binding:"required,oneof=expense income payback"`
	PaybackKind *string            `json:"payback_kind" binding:"omitempty,oneof=payback loan"`
	Amount      float64            `json:"amount"       binding:"required,gt=0"`
	Date        string             `json:"date"         binding:"required,datetime=2006-01-02"`
	Description *string            `json:"description"  binding:"omitempty,max=200"`
	Note        *string            `json:"note"         binding:"omitempty,max=500"`
	SplitMode   string             `json:"split_mode"   binding:"omitempty,oneof=equal custom"`
	People      []PaperPersonInput `json:"people"       binding:"required,min=1,dive"`
	// TagIDs — event tags (event bills only).
	TagIDs []uuid.UUID `json:"tag_ids"`
}

// ClaimInput — "ลงสมุดของฉัน": my side of a paper.
type ClaimInput struct {
	// Pay — wallet (I settled it from my wallet) | debt (I owe the payers). Default debt.
	Pay         string      `json:"pay"          binding:"omitempty,oneof=wallet debt"`
	AccountID   *uuid.UUID  `json:"account_id"`
	CategoryID  *uuid.UUID  `json:"category_id"`
	TagIDs      []uuid.UUID `json:"tag_ids"`
	PrivateNote *string     `json:"private_note" binding:"omitempty,max=500"`
	// BookDebts — an event bill: my debts of this bill now (a lump of one bill; the event's
	// lump skips these pairs). A split bill always books its debts.
	BookDebts bool `json:"book_debts"`
	// SkipTransaction — no transaction now, only the debts (claim it later).
	SkipTransaction bool `json:"skip_transaction"`
	// TransactionID — my existing plain expense / income becomes my book of the paper instead of a
	// new row (the tx edit switching "แบ่ง" on, owner 2026-11-03).
	TransactionID *uuid.UUID `json:"transaction_id"`
}

type CreatePaperRequest struct {
	Paper PaperInput `json:"paper" binding:"required"`
	// MyBooks — the creator's claim (omitted = auto claim).
	MyBooks *ClaimInput `json:"my_books"`
	// SkipMyBooks — the paper only; I claim it later.
	SkipMyBooks bool `json:"skip_my_books"`
	// NewEvent — POST /split-bills only: make this event with the bill (one
	// transaction); the bill is its first bill, its people become members.
	NewEvent *NewEventInput `json:"new_event"`
}

type NewEventInput struct {
	Name     string           `json:"name"      binding:"required,min=1,max=100"`
	IconCode *shared.IconCode `json:"icon_code" binding:"omitempty"`
}

type UpdatePaperRequest struct {
	Paper   PaperInput `json:"paper"   binding:"required"`
	Version int        `json:"version" binding:"required,min=1"`
}

type MarkPaidBackRequest struct {
	PaidBack bool `json:"paid_back"`
}

// LeaveRequest — leaving a paper (or being removed): my books linked to it are kept or deleted.
type LeaveRequest struct {
	Books string `json:"books" binding:"omitempty,oneof=keep delete"`
}

type MovePaperRequest struct {
	// ProjectID — the event to move into; null = out of its event (a split bill).
	ProjectID *uuid.UUID `json:"project_id"`
}

// PaperWriteResult — a write's answer: the paper as I see it and everything it changed.
type PaperWriteResult struct {
	Paper   *PaperView     `json:"paper"`
	Effects shared.Effects `json:"effects"`
}

type PaperDeleteResult struct {
	ID      uuid.UUID      `json:"id"`
	Deleted bool           `json:"deleted"`
	Effects shared.Effects `json:"effects"`
}

type EventDetail struct {
	Event         Event          `json:"event"`
	Summary       EventSummary   `json:"summary"`
	Members       []EventMember  `json:"members"`
	Tags          []EventTag     `json:"tags"`
	Board         PaperList      `json:"board"`
	DeletePreview *DeletePreview `json:"delete_preview"`
	// AutoSyncEvents / AutoAcceptEvents — my switches, shown on the event page.
	AutoSyncEvents   bool `json:"auto_sync_events"`
	AutoAcceptEvents bool `json:"auto_accept_events"`
}

type EventPatch struct {
	Event   Event         `json:"event"`
	Summary EventSummary  `json:"summary"`
	Members []EventMember `json:"members"`
	Tags    []EventTag    `json:"tags"`
}

type EventTagWriteResult struct {
	EventPatch
	Tag EventTag `json:"tag"`
}

type EventTagImportResult struct {
	Created []shared.TagRef `json:"created"`
	Tags    []EventTag      `json:"tags"`
}

// EventDeleteResult — DELETE /projects/{id}: its bills became split bills.
type EventDeleteResult struct {
	ID             uuid.UUID      `json:"id"`
	SplitBillCount int            `json:"split_bill_count"`
	Effects        shared.Effects `json:"effects"`
}

type DeletedResult struct {
	ID uuid.UUID `json:"id"`
}

type CreateProjectRequest struct {
	Name          string           `json:"name"           binding:"required,min=1,max=100"`
	Description   *string          `json:"description"    binding:"omitempty,max=200"`
	Note          *string          `json:"note"           binding:"omitempty,max=500"`
	StartDate     *string          `json:"start_date"     binding:"omitempty,datetime=2006-01-02"`
	EndDate       *string          `json:"end_date"       binding:"omitempty,datetime=2006-01-02"`
	IconCode      *shared.IconCode `json:"icon_code"      binding:"omitempty"`
	PlannedAmount *float64         `json:"planned_amount" binding:"omitempty,gt=0"`
	TagIDs        []uuid.UUID      `json:"tag_ids"`
}

type UpdateProjectRequest struct {
	Name          *string          `json:"name"           binding:"omitempty,min=1,max=100"`
	Description   *string          `json:"description"    binding:"omitempty,max=200"`
	Note          *string          `json:"note"           binding:"omitempty,max=500"`
	StartDate     *string          `json:"start_date"     binding:"omitempty,datetime=2006-01-02"`
	EndDate       *string          `json:"end_date"       binding:"omitempty,datetime=2006-01-02"`
	Status        *string          `json:"status"         binding:"omitempty,oneof=active completed cancelled archived"`
	IconCode      *shared.IconCode `json:"icon_code"      binding:"omitempty"`
	PlannedAmount *float64         `json:"planned_amount" binding:"omitempty,gt=0"`
	TagIDs        []uuid.UUID      `json:"tag_ids"`

	present map[string]bool
}

func (r *UpdateProjectRequest) UnmarshalJSON(data []byte) error {
	type alias UpdateProjectRequest
	present, err := shared.DecodeTracked(data, (*alias)(r))
	r.present = present
	return err
}

func (r *UpdateProjectRequest) has(k string) bool { return r.present[k] }

func (r *UpdateProjectRequest) onlyMyTags() bool {
	return r.has("tag_ids") && len(r.present) == 1
}

func (r *UpdateProjectRequest) DescriptionChange() (*string, bool) {
	return shared.TextChange(r.Description, r.has("description"))
}

func (r *UpdateProjectRequest) NoteChange() (*string, bool) {
	return shared.TextChange(r.Note, r.has("note"))
}

type AddMemberRequest struct {
	Email       *string          `json:"email"        binding:"omitempty,email"`
	DisplayName *string          `json:"display_name" binding:"omitempty,min=1,max=100"`
	ContactID   *uuid.UUID       `json:"contact_id"`
	Role        string           `json:"role"         binding:"omitempty,oneof=contributor viewer"`
	AdHoc       bool             `json:"ad_hoc"`
	IconCode    *shared.IconCode `json:"icon_code"    binding:"omitempty"`
}

type UpdateMemberRequest struct {
	Role     *string          `json:"role"      binding:"omitempty,oneof=contributor viewer"`
	IconCode *shared.IconCode `json:"icon_code" binding:"omitempty"`
}

// MergeMemberRequest — the member goes into Into (the better identified one stays).
type MergeMemberRequest struct {
	Into uuid.UUID `json:"into" binding:"required"`
}

type EventTagRequest struct {
	Name     *string          `json:"name"      binding:"omitempty,min=1,max=50"`
	IconCode *shared.IconCode `json:"icon_code"`

	iconPresent bool
}

func (r *EventTagRequest) UnmarshalJSON(data []byte) error {
	type alias EventTagRequest
	present, err := shared.DecodeTracked(data, (*alias)(r))
	r.iconPresent = present["icon_code"]
	return err
}

type EventTagImportRequest struct {
	TagIDs []uuid.UUID `json:"tag_ids"`
}

type ListFilter struct {
	Status  string
	Q       string
	TagIDs  []uuid.UUID
	Sort    string
	Page    int
	PerPage int
}

// PaperFilter — a list of papers (an event's board, or my split bills).
type PaperFilter struct {
	Type    string
	Mine    bool
	Q       string
	TagID   *uuid.UUID
	Status  string // split bills: invited | joined (default: both)
	Sort    string
	Page    int
	PerPage int
}

var _ json.Unmarshaler = (*UpdateProjectRequest)(nil)

type InviteAcceptResult struct {
	*EventDetail
	InboxSummary notifications.InboxSummary `json:"inbox_summary"`
}

type DeletePreview struct {
	Bills         int `json:"bills"`           // bills of the event
	Members       int `json:"members"`         // active members
	LinkedTxCount int `json:"linked_tx_count"` // my own book records linked to it
}
