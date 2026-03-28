package project

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/ppChub722/finna-bbear-be/internal/platform/response"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func getUserID(c *gin.Context) int64 {
	id, _ := c.Get("userID")
	return id.(int64)
}

func getID(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil {
		response.BadRequest(c, "INVALID_ID", "Invalid ID", nil)
		return 0, false
	}
	return id, true
}

func handleErr(c *gin.Context, err error) {
	switch err {
	case ErrNotFound:
		response.NotFound(c, "NOT_FOUND", "Resource not found")
	case ErrForbidden:
		response.Fail(c, http.StatusForbidden, "FORBIDDEN", "Access denied", nil)
	case ErrAlreadyMember:
		response.BadRequest(c, "ALREADY_MEMBER", err.Error(), nil)
	case ErrCannotRemoveOwner:
		response.BadRequest(c, "CANNOT_REMOVE_OWNER", err.Error(), nil)
	default:
		msg := err.Error()
		if msg == "project has linked transactions" {
			response.BadRequest(c, "PROJECT_HAS_TRANSACTIONS", msg, nil)
			return
		}
		if msg == "cannot change owner role" {
			response.BadRequest(c, "CANNOT_CHANGE_OWNER_ROLE", msg, nil)
			return
		}
		if msg == "owner cannot leave project, delete it or transfer ownership" {
			response.BadRequest(c, "OWNER_CANNOT_LEAVE", msg, nil)
			return
		}
		response.InternalError(c, msg, nil)
	}
}

func (h *Handler) CreateProject(c *gin.Context) {
	var req CreateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	p, err := h.service.CreateProject(c.Request.Context(), getUserID(c), req)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.Created(c, "Project created", p)
}

func (h *Handler) GetProjects(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", "20"))
	var status, projType *string
	if v := c.Query("status"); v != "" {
		status = &v
	}
	if v := c.Query("type"); v != "" {
		projType = &v
	}

	items, total, err := h.service.GetProjects(c.Request.Context(), getUserID(c), status, projType, page, perPage)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Projects fetched", gin.H{"data": items, "pagination": gin.H{"page": page, "total": total}})
}

func (h *Handler) GetProject(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	detail, err := h.service.GetProjectByID(c.Request.Context(), getUserID(c), id)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Project fetched", detail)
}

func (h *Handler) UpdateProject(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	var req UpdateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	if err := h.service.UpdateProject(c.Request.Context(), getUserID(c), id, req); err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Project updated", nil)
}

func (h *Handler) DeleteProject(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteProject(c.Request.Context(), getUserID(c), id); err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Project deleted", nil)
}

func (h *Handler) GetProjectSummary(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	groupBy := c.DefaultQuery("group_by", "category")
	summary, err := h.service.GetProjectSummary(c.Request.Context(), getUserID(c), id, groupBy)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Project summary fetched", summary)
}

func (h *Handler) GetMembers(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	members, err := h.service.GetMembers(c.Request.Context(), getUserID(c), id)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Members fetched", gin.H{"data": members})
}

func (h *Handler) AddMember(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	var req AddMemberRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	member, err := h.service.AddMember(c.Request.Context(), getUserID(c), id, req)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.Created(c, "Member added", member)
}

func (h *Handler) UpdateMemberRole(c *gin.Context) {
	projectID, ok := getID(c, "id")
	if !ok {
		return
	}
	targetUserID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "INVALID_ID", "Invalid user ID", nil)
		return
	}
	var req UpdateMemberRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	if err := h.service.UpdateMemberRole(c.Request.Context(), getUserID(c), projectID, targetUserID, req); err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Member role updated", nil)
}

func (h *Handler) RemoveMember(c *gin.Context) {
	projectID, ok := getID(c, "id")
	if !ok {
		return
	}
	targetUserID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "INVALID_ID", "Invalid user ID", nil)
		return
	}
	if err := h.service.RemoveMember(c.Request.Context(), getUserID(c), projectID, targetUserID); err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Member removed", nil)
}

func (h *Handler) LeaveProject(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	if err := h.service.LeaveProject(c.Request.Context(), getUserID(c), id); err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Left project successfully", nil)
}
