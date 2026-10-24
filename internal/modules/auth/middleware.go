package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth/utils"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/config"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

const ctxKeyUserID = "user_id"

func Middleware(cfg *config.Config, store *Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" {
			response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Authorization header required", nil)
			c.Abort()
			return
		}

		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid header format. Use 'Bearer <token>'", nil)
			c.Abort()
			return
		}

		userID, err := utils.ParseToken(parts[1], cfg.JWT.Secret)
		if err != nil {
			response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid or expired token", nil)
			c.Abort()
			return
		}

		status, err := store.StatusByID(c.Request.Context(), userID)
		if err != nil {
			response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "User not found", nil)
			c.Abort()
			return
		}

		if status != "active" && !inactiveAllowed(c) {
			response.Fail(c, http.StatusForbidden, "ACCOUNT_INACTIVE", "Account is inactive — reactivate to continue", nil)
			c.Abort()
			return
		}

		c.Set(ctxKeyUserID, userID)
		c.Next()
	}
}

func inactiveAllowed(c *gin.Context) bool {
	method, path := c.Request.Method, c.Request.URL.Path
	if method == http.MethodGet && path == "/api/v1/users/me" {
		return true
	}
	if method == http.MethodPost && path == "/api/v1/users/me/reactivate" {
		return true
	}
	return false
}

func UserIDFromContext(c *gin.Context) (uuid.UUID, bool) {
	v, ok := c.Get(ctxKeyUserID)
	if !ok {
		return uuid.Nil, false
	}
	id, ok := v.(uuid.UUID)
	return id, ok
}
