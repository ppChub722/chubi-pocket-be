package auth

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/ppChub722/finna-bbear-be/internal/platform/config"
	"github.com/ppChub722/finna-bbear-be/internal/platform/response"
)

// Middleware checks for a valid JWT token
func Middleware(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1. Get the Authorization Header
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Authorization header required", nil)
			c.Abort()
			return
		}

		// 2. Check format "Bearer <token>"
		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid header format. Use 'Bearer <token>'", nil)
			c.Abort()
			return
		}
		tokenString := parts[1]

		// 3. Parse and Verify Token
		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			// Validate the signing method is HMAC
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return []byte(cfg.JWT.Secret), nil
		})

		// 4. Handle Invalid/Expired Token
		if err != nil || !token.Valid {
			response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid or expired token", nil)
			c.Abort()
			return
		}

		// 5. Extract Claims (User Data)
		if claims, ok := token.Claims.(jwt.MapClaims); ok {
			// Extract UserID (stored as "sub" in utils.go)
			if sub, ok := claims["sub"].(float64); ok {
				c.Set("userID", int64(sub))
			}
			
			// Extract Username
			if username, ok := claims["username"].(string); ok {
				c.Set("username", username)
			}
			
			c.Next() // Pass to the next handler
		} else {
			response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid token claims", nil)
			c.Abort()
		}
	}
}