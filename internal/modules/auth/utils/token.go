package utils

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Claims matches the JWT shape from 01-auth.md §2.4.
// Phase 0/1 tokens are 30-day access tokens; refresh tokens land in Phase 2+.
type Claims struct {
	jwt.RegisteredClaims
	Type string `json:"type"`
}

func GenerateToken(userID uuid.UUID, secret string, ttl time.Duration) (string, error) {
	now := time.Now()
	jti, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate jti: %w", err)
	}

	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			ID:        jti.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
		Type: "access",
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString([]byte(secret))
}

// ParseToken verifies signature + expiry and returns the user ID from `sub`.
func ParseToken(tokenString, secret string) (uuid.UUID, error) {
	tok, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return uuid.Nil, err
	}

	claims, ok := tok.Claims.(*Claims)
	if !ok || !tok.Valid {
		return uuid.Nil, fmt.Errorf("invalid token claims")
	}

	id, err := uuid.Parse(claims.Subject)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid sub claim: %w", err)
	}
	return id, nil
}
