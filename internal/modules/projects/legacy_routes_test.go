package projects

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

// legacyRoutes — the 0.4.0 event routes retired by 0.4.1-2 V16 (quick create, add / remove bills,
// copy, my-books), test-only: older tests still set up their data through these service paths. No
// route in main.go or the spec serves them any more.
func legacyRoutes(g *gin.RouterGroup, s *Service) {
	user := func(c *gin.Context) (bool, func(int, any, error)) {
		_, ok := auth.UserIDFromContext(c)
		if !ok {
			response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		}
		return ok, func(status int, out any, err error) {
			if err != nil {
				mapQuickCreateError(c, err)
				return
			}
			c.JSON(status, out)
		}
	}
	h := NewHandler(s)
	g.POST("/projects/quick", func(c *gin.Context) {
		ok, done := user(c)
		var req QuickCreateRequest
		if !ok || !bindJSON(c, &req) {
			return
		}
		uid, _ := auth.UserIDFromContext(c)
		out, err := s.QuickCreate(c.Request.Context(), uid, req)
		done(http.StatusCreated, out, err)
	})
	g.POST("/projects/:id/bills", func(c *gin.Context) {
		uid, id, ok := h.bindProjectID(c)
		var req AddBillsRequest
		if !ok || !bindJSON(c, &req) {
			return
		}
		out, err := s.AddBills(c.Request.Context(), uid, id, req)
		_, done := user(c)
		done(http.StatusOK, out, err)
	})
	g.DELETE("/projects/:id/bills/:transaction_id", func(c *gin.Context) {
		uid, id, ok := h.bindProjectID(c)
		txID, ok2 := pathID(c, "transaction_id", "transaction id")
		if !ok || !ok2 {
			return
		}
		out, err := s.RemoveBill(c.Request.Context(), uid, id, txID)
		_, done := user(c)
		done(http.StatusOK, out, err)
	})
	g.POST("/projects/:id/project-transactions/:pt_id/copy", func(c *gin.Context) {
		uid, id, ok := h.bindProjectID(c)
		ptID, ok2 := pathID(c, "pt_id", "project transaction id")
		if !ok || !ok2 {
			return
		}
		out, err := s.CopyToPersonal(c.Request.Context(), uid, id, ptID)
		reply(c, http.StatusCreated, out, err)
	})
	g.PUT("/projects/:id/project-transactions/:pt_id/my-books", func(c *gin.Context) {
		uid, id, ok := h.bindProjectID(c)
		ptID, ok2 := pathID(c, "pt_id", "project transaction id")
		var req MyBooksRequest
		if !ok || !ok2 || !bindJSON(c, &req) {
			return
		}
		out, err := s.SetMyBooks(c.Request.Context(), uid, id, ptID, req)
		reply(c, http.StatusOK, out, err)
	})
}
