package auth

import (
	"net/http"
	"strings"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/gin-gonic/gin"
	"github.com/ppChub722/finna-bbear-be/internal/config"
	"github.com/ppChub722/finna-bbear-be/internal/models"
	"github.com/ppChub722/finna-bbear-be/internal/response"
	"github.com/ppChub722/finna-bbear-be/internal/store"
)

// Auth holds the dependencies for the auth handlers.

type Auth struct {
	Store *store.Store
	Cfg   *config.Config
}

func New(store *store.Store, cfg *config.Config) *Auth {
	return &Auth{Store: store, Cfg: cfg}
}

// Register godoc
// @Summary Register a new user
// @Description Register a new user
// @Tags auth
// @Accept  json
// @Produce  json
// @Param user body models.User true "User"
// @Success 201 {object} response.Response
// @Failure 400 {object} response.Response
// @Failure 500 {object} response.Response
// @Router /auth/register [post]
func (a *Auth) Register(c *gin.Context) {
	var user models.User
	if err := c.ShouldBindJSON(&user); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}

	hashedPassword, err := HashPassword(user.Password)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "Failed to hash password")
		return
	}
	user.Password = hashedPassword

	if err := a.Store.CreateUser(c.Request.Context(), &user); err != nil {
		response.Error(c, http.StatusInternalServerError, "Failed to create user")
		return
	}

	response.Success(c, http.StatusCreated, "User created successfully", user)
}

// Login godoc
// @Summary Login a user
// @Description Login a user
// @Tags auth
// @Accept  json
// @Produce  json
// @Param user body models.User true "User"
// @Success 200 {object} response.Response
// @Failure 400 {object} response.Response
// @Failure 401 {object} response.Response
// @Failure 404 {object} response.Response
// @Failure 500 {object} response.Response
// @Router /auth/login [post]
func (a *Auth) Login(c *gin.Context) {
	var req models.User
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}

	user, err := a.Store.GetUserByEmail(c.Request.Context(), req.Email)
	if err != nil {
		response.Error(c, http.StatusNotFound, "User not found")
		return
	}

	if !CheckPasswordHash(req.Password, user.Password) {
		response.Error(c, http.StatusUnauthorized, "Invalid credentials")
		return
	}

	token, err := GenerateToken(user, a.Cfg.JWT.Secret)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "Failed to generate token")
		return
	}

	response.Success(c, http.StatusOK, "Login successful", gin.H{"token": token})
}

// Logout godoc
// @Summary Logout a user
// @Description Logout a user by blacklisting their token
// @Tags auth
// @Accept  json
// @Produce  json
// @Security BearerAuth
// @Success 200 {object} response.Response
// @Failure 401 {object} response.Response
// @Failure 500 {object} response.Response
// @Router /auth/logout [post]
func (a *Auth) Logout(c *gin.Context) {
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		response.Error(c, http.StatusUnauthorized, "Missing authorization header")
		return
	}

	tokenString := strings.TrimPrefix(authHeader, "Bearer ")
	if tokenString == authHeader {
		response.Error(c, http.StatusUnauthorized, "Invalid token format")
		return
	}

	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		return []byte(a.Cfg.JWT.Secret), nil
	})

	if err != nil {
		if err == jwt.ErrSignatureInvalid {
			response.Error(c, http.StatusUnauthorized, "Invalid token signature")
			return
		}
		response.Error(c, http.StatusBadRequest, "Invalid token")
		return
	}

	if !token.Valid {
		response.Error(c, http.StatusUnauthorized, "Invalid token")
		return
	}

	expiresAt := time.Unix(claims.ExpiresAt, 0)
	err = a.Store.BlacklistToken(c.Request.Context(), tokenString, expiresAt)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "Failed to logout")
		return
	}

	response.Success(c, http.StatusOK, "Logout successful", nil)
}
