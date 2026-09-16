package auth

// Decoy-registration test: with REGISTRATION_OPEN=false the register
// flow must return a convincing success WITHOUT touching the database
// (the service is constructed with a nil store — any DB access would
// panic, which is exactly the guarantee this test wants).

import (
	"context"
	"testing"
	"time"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/config"
)

func TestRegisterClosedReturnsDecoySuccess(t *testing.T) {
	cfg := &config.Config{}
	cfg.App.RegistrationOpen = false
	cfg.JWT.Secret = "test-secret"
	cfg.JWT.ExpirationTime = time.Hour

	svc := NewService(nil, cfg) // nil store: DB access would panic

	email := "decoy@test.local"
	resp, err := svc.Register(context.Background(), RegisterRequest{
		Username:    "decoyuser",
		Password:    "Password123!",
		DisplayName: "Decoy",
		Email:       &email,
	})
	if err != nil {
		t.Fatalf("closed registration must still return success, got %v", err)
	}
	if resp.User.Username != "decoyuser" || resp.User.DisplayName != "Decoy" {
		t.Fatalf("decoy response must echo the request: %+v", resp.User)
	}
	if resp.Token.AccessToken == "" {
		t.Fatal("decoy response must carry a token like a real one")
	}
}
