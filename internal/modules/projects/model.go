package projects

import (
	"time"

	"github.com/google/uuid"
)

// Project status enum values.
const (
	StatusActive    = "active"
	StatusCompleted = "completed"
	StatusCancelled = "cancelled"
	StatusArchived  = "archived"
)

// Member role enum.
const (
	RoleOwner       = "owner"
	RoleContributor = "contributor"
	RoleViewer      = "viewer"
)

// Member status enum.
const (
	MemberStatusPending = "pending"
	MemberStatusActive  = "active"
	MemberStatusLeft    = "left"
)

// Project transaction type enum.
const (
	PTTypeExpense = "expense"
	PTTypeIncome  = "income"
)

type Project struct {
	ID            uuid.UUID `json:"id"`
	OwnerUserID   uuid.UUID `json:"owner_user_id"`
	Name          string    `json:"name"`
	Type          *string   `json:"type"`
	Description   *string   `json:"description"`
	StartDate     *string   `json:"start_date"`
	EndDate       *string   `json:"end_date"`
	Status        string    `json:"status"`
	IconID        *string   `json:"icon_id"`
	ColorID       *string   `json:"color_id"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	// Hydrated for list/get views.
	MembersCount int `json:"members_count,omitempty"`
}

// ProjectMember has 2 kinds post-migration 27:
//
//	linked  (user_id IS NOT NULL)
//	ad-hoc  (user_id IS NULL)
//
// The previous "contact" kind has been collapsed into ad-hoc — contacts are
// user-scoped, so storing a contact_id on this shared row was ambiguous. FE
// resolves contact -> linked user_id (via contacts.linked_user_id) before
// calling AddMember.
type ProjectMember struct {
	ID           uuid.UUID  `json:"id"`
	ProjectID    uuid.UUID  `json:"project_id"`
	UserID       *uuid.UUID `json:"user_id"`
	DisplayName  string     `json:"display_name"`
	Role         string     `json:"role"`
	Status       string     `json:"status"`
	InvitedAt    *time.Time `json:"invited_at"`
	JoinedAt     *time.Time `json:"joined_at"`
	LeftAt       *time.Time `json:"left_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	AvatarURL    *string    `json:"avatar_url,omitempty"`
}

// ProjectTransaction. Splits are represented as child rows linked via
// ParentProjectTransactionID. Children inherit the parent's type/currency/
// date/note (BE copies them at create-time). `Marks` is the set of
// project_member ids who have flagged this row "resolved on the board"; it
// is independent of personal-book actions.
type ProjectTransaction struct {
	ID                          uuid.UUID   `json:"id"`
	ProjectID                   uuid.UUID   `json:"project_id"`
	ParentProjectTransactionID  *uuid.UUID  `json:"parent_project_transaction_id"`
	TransactionMemberID         uuid.UUID   `json:"transaction_member_id"`
	RecordUserID                uuid.UUID   `json:"record_user_id"`
	Type                        string      `json:"type"`
	Amount                      float64     `json:"amount"`
	Currency                    string      `json:"currency"`
	Date                        string      `json:"date"`
	Note                        *string     `json:"note"`
	Description                 *string     `json:"description"`
	CategoryName                *string     `json:"category_name"`
	CategoryIconID              *string     `json:"category_icon_id"`
	CategoryColorID             *string     `json:"category_color_id"`
	Marks                       []uuid.UUID `json:"marks"`
	CreatedAt                   time.Time   `json:"created_at"`
	UpdatedAt                   time.Time   `json:"updated_at"`
}

// --- Request bodies ---

type CreateProjectRequest struct {
	Name        string  `json:"name"        binding:"required,min=1,max=100"`
	Type        *string `json:"type"        binding:"omitempty,max=30"`
	Description *string `json:"description"`
	StartDate   *string `json:"start_date"  binding:"omitempty,datetime=2006-01-02"`
	EndDate     *string `json:"end_date"    binding:"omitempty,datetime=2006-01-02"`
	IconID      *string `json:"icon_id"`
	ColorID     *string `json:"color_id"`
}

type UpdateProjectRequest struct {
	Name        *string `json:"name"        binding:"omitempty,min=1,max=100"`
	Type        *string `json:"type"        binding:"omitempty,max=30"`
	Description *string `json:"description"`
	StartDate   *string `json:"start_date"  binding:"omitempty,datetime=2006-01-02"`
	EndDate     *string `json:"end_date"    binding:"omitempty,datetime=2006-01-02"`
	Status      *string `json:"status"      binding:"omitempty,oneof=active completed cancelled archived"`
	IconID      *string `json:"icon_id"`
	ColorID     *string `json:"color_id"`
}

// AddMemberRequest carries one of two variants. Caller picks by setting
// the discriminator fields:
//   (a) Email + DisplayName            → invite-by-email (status=pending)
//   (b) AdHoc=true + DisplayName       → ad-hoc (status=active, no user link)
//
// The from-contact variant is gone: project_members no longer stores
// contact_id (user-scoped resource on a shared table = ambiguous). FE looks
// up the contact's linked_user_id locally and either calls (a) with the
// contact's email or (b) ad-hoc.
type AddMemberRequest struct {
	Email       *string `json:"email"        binding:"omitempty,email"`
	DisplayName *string `json:"display_name" binding:"omitempty,min=1,max=100"`
	Role        string  `json:"role"         binding:"omitempty,oneof=owner contributor viewer"`
	AdHoc       bool    `json:"ad_hoc"`
}

type UpdateMemberRequest struct {
	Role *string `json:"role" binding:"omitempty,oneof=owner contributor viewer"`
}

// ProjectSplitInput — one share line on a CreateProjectTransactionRequest.
// MemberID must be a project member of the same project, must differ from
// the parent's TransactionMemberID (no self-split), and the sum of split
// amounts on a parent must be <= parent.amount (remainder = parent payer's
// own share).
type ProjectSplitInput struct {
	MemberID uuid.UUID `json:"member_id" binding:"required"`
	Amount   float64   `json:"amount"    binding:"required,gt=0"`
}

type CreateProjectTransactionRequest struct {
	TransactionMemberID uuid.UUID           `json:"transaction_member_id" binding:"required"`
	Type                string              `json:"type"                  binding:"required,oneof=expense income"`
	Amount              float64             `json:"amount"                binding:"required,gt=0"`
	Currency            string              `json:"currency"              binding:"required,len=3"`
	Date                string              `json:"date"                  binding:"required,datetime=2006-01-02"`
	Note                *string             `json:"note"`
	Description         *string             `json:"description"`
	CategoryName        *string             `json:"category_name"`
	CategoryIconID      *string             `json:"category_icon_id"`
	CategoryColorID     *string             `json:"category_color_id"`
	Splits              []ProjectSplitInput `json:"splits"                binding:"omitempty,dive"`
}

// UpdateProjectTransactionRequest — full-replacement on splits when present.
// If Splits is nil, existing children are untouched. If Splits is a non-nil
// (possibly empty) slice, all existing children are deleted and the slice
// is re-inserted.
type UpdateProjectTransactionRequest struct {
	Amount          *float64             `json:"amount"           binding:"omitempty,gt=0"`
	Date            *string              `json:"date"             binding:"omitempty,datetime=2006-01-02"`
	Note            *string              `json:"note"`
	Description     *string              `json:"description"`
	CategoryName    *string              `json:"category_name"`
	CategoryIconID  *string              `json:"category_icon_id"`
	CategoryColorID *string              `json:"category_color_id"`
	Splits          *[]ProjectSplitInput `json:"splits"           binding:"omitempty,dive"`
}

// MarkRequest — body for PUT /projects/:id/project-transactions/:pt_id/mark.
type MarkRequest struct {
	Marked bool `json:"marked"`
}

// --- List filters / responses ---

type ListFilter struct {
	Status string
	Type   *string
	Page   int
	PerPage int
}

type Pagination struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	Total      int `json:"total"`
	TotalPages int `json:"total_pages"`
}

type ListResponse struct {
	Data       []Project  `json:"data"`
	Pagination Pagination `json:"pagination"`
}

type ListMembersResponse struct {
	Data []ProjectMember `json:"data"`
}

type ListPTResponse struct {
	Data       []ProjectTransaction `json:"data"`
	Pagination Pagination           `json:"pagination"`
}

// SummaryResponse — high-level project summary.
type SummaryResponse struct {
	ProjectID       uuid.UUID            `json:"project_id"`
	TotalExpense    float64              `json:"total_expense"`
	TotalIncome     float64              `json:"total_income"`
	TransactionCount int                 `json:"transaction_count"`
	MemberCount     int                  `json:"member_count"`
	MyPosition      *float64             `json:"my_position,omitempty"`
}
