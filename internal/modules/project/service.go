package project

import (
	"context"
	"errors"
	"time"
)

type Service struct {
	store *Store
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

func (s *Service) CreateProject(ctx context.Context, userID int64, req CreateProjectRequest) (*Project, error) {
	now := time.Now()
	p := &Project{
		UserID:      userID,
		Name:        req.Name,
		Type:        req.Type,
		BudgetGoal:  req.BudgetGoal,
		StartDate:   req.StartDate,
		EndDate:     req.EndDate,
		Status:      "active",
		Description: req.Description,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.store.CreateProject(ctx, p); err != nil {
		return nil, err
	}
	// Auto-add owner as member
	s.store.AddMember(ctx, p.ID, userID, "owner")
	return p, nil
}

func (s *Service) GetProjects(ctx context.Context, userID int64, status, projType *string, page, perPage int) ([]ProjectListItem, int, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 20
	}
	return s.store.GetProjects(ctx, userID, status, projType, page, perPage)
}

func (s *Service) GetProjectByID(ctx context.Context, userID, projectID int64) (*ProjectDetail, error) {
	hasAccess, err := s.store.HasAccess(ctx, projectID, userID)
	if err != nil {
		return nil, err
	}
	if !hasAccess {
		return nil, ErrForbidden
	}

	p, err := s.store.GetProjectByID(ctx, projectID)
	if err != nil {
		return nil, err
	}

	summary, _ := s.store.GetProjectSummary(ctx, projectID, "category")
	members, _ := s.store.GetMembers(ctx, projectID)

	detail := &ProjectDetail{Project: *p, Members: members}
	if summary != nil {
		if p.BudgetGoal != nil && *p.BudgetGoal > 0 {
			pct := int((summary.TotalExpense / *p.BudgetGoal) * 100)
			summary.BudgetUtilizationPct = &pct
		}
		detail.Summary = *summary
	}

	return detail, nil
}

func (s *Service) UpdateProject(ctx context.Context, userID, projectID int64, req UpdateProjectRequest) error {
	isOwner, err := s.store.IsOwner(ctx, projectID, userID)
	if err != nil {
		return err
	}
	if !isOwner {
		return ErrForbidden
	}
	return s.store.UpdateProject(ctx, projectID, req)
}

func (s *Service) DeleteProject(ctx context.Context, userID, projectID int64) error {
	isOwner, err := s.store.IsOwner(ctx, projectID, userID)
	if err != nil {
		return err
	}
	if !isOwner {
		return ErrForbidden
	}
	return s.store.DeleteProject(ctx, projectID)
}

func (s *Service) GetProjectSummary(ctx context.Context, userID, projectID int64, groupBy string) (*ProjectSummary, error) {
	hasAccess, err := s.store.HasAccess(ctx, projectID, userID)
	if err != nil {
		return nil, err
	}
	if !hasAccess {
		return nil, ErrForbidden
	}

	p, err := s.store.GetProjectByID(ctx, projectID)
	if err != nil {
		return nil, err
	}

	summary, err := s.store.GetProjectSummary(ctx, projectID, groupBy)
	if err != nil {
		return nil, err
	}

	if p.BudgetGoal != nil && *p.BudgetGoal > 0 {
		pct := int((summary.TotalExpense / *p.BudgetGoal) * 100)
		summary.BudgetUtilizationPct = &pct
	}

	return summary, nil
}

func (s *Service) GetMembers(ctx context.Context, userID, projectID int64) ([]ProjectMember, error) {
	hasAccess, err := s.store.HasAccess(ctx, projectID, userID)
	if err != nil {
		return nil, err
	}
	if !hasAccess {
		return nil, ErrForbidden
	}
	return s.store.GetMembers(ctx, projectID)
}

func (s *Service) AddMember(ctx context.Context, userID, projectID int64, req AddMemberRequest) (*ProjectMember, error) {
	isOwner, err := s.store.IsOwner(ctx, projectID, userID)
	if err != nil {
		return nil, err
	}
	if !isOwner {
		return nil, ErrForbidden
	}

	role := req.Role
	if role == "" {
		role = "viewer"
	}

	// Check if already member
	_, err = s.store.GetMemberRole(ctx, projectID, req.UserID)
	if err == nil {
		return nil, ErrAlreadyMember
	}

	return s.store.AddMember(ctx, projectID, req.UserID, role)
}

func (s *Service) UpdateMemberRole(ctx context.Context, userID, projectID, targetUserID int64, req UpdateMemberRoleRequest) error {
	isOwner, err := s.store.IsOwner(ctx, projectID, userID)
	if err != nil {
		return err
	}
	if !isOwner {
		return ErrForbidden
	}

	// Can't change owner role
	role, err := s.store.GetMemberRole(ctx, projectID, targetUserID)
	if err != nil {
		return err
	}
	if role == "owner" {
		return errors.New("cannot change owner role")
	}

	return s.store.UpdateMemberRole(ctx, projectID, targetUserID, req.Role)
}

func (s *Service) RemoveMember(ctx context.Context, userID, projectID, targetUserID int64) error {
	isOwner, err := s.store.IsOwner(ctx, projectID, userID)
	if err != nil {
		return err
	}
	if !isOwner {
		return ErrForbidden
	}

	// Can't remove owner
	role, err := s.store.GetMemberRole(ctx, projectID, targetUserID)
	if err != nil {
		return err
	}
	if role == "owner" {
		return ErrCannotRemoveOwner
	}

	return s.store.RemoveMember(ctx, projectID, targetUserID)
}

func (s *Service) LeaveProject(ctx context.Context, userID, projectID int64) error {
	isOwner, err := s.store.IsOwner(ctx, projectID, userID)
	if err != nil {
		return err
	}
	if isOwner {
		return errors.New("owner cannot leave project, delete it or transfer ownership")
	}
	return s.store.RemoveMember(ctx, projectID, userID)
}
