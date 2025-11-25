package utils

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Update: Accept 'email' as a parameter
func GenerateToken(userID int64, username, email, secret string, duration time.Duration) (string, error) {
	claims := jwt.MapClaims{
		"sub":      userID,                          // Subject (User ID)
		"username": username,                        // Custom Claim
		"email":    email,                           // ✅ NEW: Add Email
		"exp":      time.Now().Add(duration).Unix(), // Expiration
		"iat":      time.Now().Unix(),               // Issued At
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}