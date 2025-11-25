package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ppChub722/finna-bbear-be/internal/config"
	"github.com/ppChub722/finna-bbear-be/internal/models"
	"github.com/ppChub722/finna-bbear-be/internal/store"
	"github.com/stretchr/testify/assert"
)

func TestLogout(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		JWT: config.JWTConfig{
			Secret: "test-secret",
		},
	}

	mockStore := &store.Store{}

	authHandler := New(mockStore, cfg)

	r := gin.Default()
	r.POST("/logout", authHandler.Logout)

	user := &models.User{
		ID:       1,
		Username: "testuser",
	}
	token, err := GenerateToken(user, cfg.JWT.Secret)
	assert.NoError(t, err)

	req, _ := http.NewRequest(http.MethodPost, "/logout", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Logout successful")
}
