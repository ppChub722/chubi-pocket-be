package utils

import (
	"golang.org/x/crypto/bcrypt"
)

// HashPassword encrypts the user's password using bcrypt
func HashPassword(password string) (string, error) {
	// DefaultCost is 10, which is a good balance of security and performance
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(bytes), err
}

// CheckPasswordHash compares a raw password string with the encrypted hash
func CheckPasswordHash(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}