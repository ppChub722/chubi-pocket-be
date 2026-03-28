package project

import "time"

type Project struct {
	ID          int64     `json:"id"`
	UserID      int64     `json:"user_id"`
	Name        string    `json:"name"`
	Type        *string   `json:"type"`
	BudgetGoal  *float64  `json:"budget_goal"`
	StartDate   *string   `json:"start_date"`
	EndDate     *string   `json:"end_date"`
	Status      string    `json:"status"` // active | completed | cancelled
	Description *string   `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ProjectListItem struct {
	Project
	TotalExpense float64 `json:"total_expense"`
	TotalIncome  float64 `json:"total_income"`
	MyRole       string  `json:"my_role"`
	MemberCount  int     `json:"member_count"`
}

type ProjectDetail struct {
	Project
	Summary ProjectSummary  `json:"summary"`
	Members []ProjectMember `json:"members"`
}

type ProjectSummary struct {
	TotalIncome         float64           `json:"total_income"`
	TotalExpense        float64           `json:"total_expense"`
	Net                 float64           `json:"net"`
	TransactionCount    int               `json:"transaction_count"`
	BudgetUtilizationPct *int             `json:"budget_utilization_pct,omitempty"`
	ByCategory          []CategoryTotal   `json:"by_category,omitempty"`
}

type CategoryTotal struct {
	Category         string  `json:"category"`
	Total            float64 `json:"total"`
	TransactionCount int     `json:"transaction_count"`
}

type ProjectMember struct {
	ID       int64     `json:"id"`
	ProjectID int64    `json:"project_id"`
	UserID   int64     `json:"user_id"`
	Name     string    `json:"name"`
	Email    string    `json:"email"`
	Role     string    `json:"role"` // owner | contributor | viewer
	JoinedAt time.Time `json:"joined_at"`
}

// =============================================================
// Request Models
// =============================================================

type CreateProjectRequest struct {
	Name        string   `json:"name" binding:"required"`
	Type        *string  `json:"type"`
	BudgetGoal  *float64 `json:"budget_goal"`
	StartDate   *string  `json:"start_date"`
	EndDate     *string  `json:"end_date"`
	Description *string  `json:"description"`
}

type UpdateProjectRequest struct {
	Name        *string  `json:"name"`
	Type        *string  `json:"type"`
	BudgetGoal  *float64 `json:"budget_goal"`
	StartDate   *string  `json:"start_date"`
	EndDate     *string  `json:"end_date"`
	Status      *string  `json:"status"`
	Description *string  `json:"description"`
}

type AddMemberRequest struct {
	UserID int64  `json:"user_id" binding:"required"`
	Role   string `json:"role"`
}

type UpdateMemberRoleRequest struct {
	Role string `json:"role" binding:"required,oneof=contributor viewer"`
}
