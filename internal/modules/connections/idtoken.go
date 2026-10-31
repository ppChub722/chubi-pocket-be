package connections

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Log In With Telegram (OpenID Connect). The app runs the whole OIDC round trip with Telegram
// (public client + PKCE) and sends us the id_token; we only verify it.
// https://core.telegram.org/bots/telegram-login

const (
	telegramIssuer  = "https://oauth.telegram.org"
	telegramJWKSURL = telegramIssuer + "/.well-known/jwks.json"
	// idTokenLeeway — clock skew allowed on exp / iat (Telegram's tokens live 30 s).
	idTokenLeeway = 30 * time.Second
	// jwksRefetchGap — an unknown kid refetches the keys at most this often.
	jwksRefetchGap = time.Minute
)

var ErrIDTokenInvalid = errors.New("telegram id_token invalid, expired or already used")

// TelegramIdentity — who the id_token says logged in.
type TelegramIdentity struct {
	ID       string // Telegram user id = the private chat id
	Name     string
	Username string // "" when the user has none
	Picture  string
}

// IDTokenVerifier checks Telegram id_tokens: RS256 with a key from Telegram's JWKS, our client id
// as audience, not expired, each token once.
type IDTokenVerifier struct {
	clientID string
	jwksURL  string
	http     *http.Client
	now      func() time.Time

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
	used      map[[32]byte]time.Time // token hash → when it can be forgotten
}

// NewIDTokenVerifier — clientID = the bot id (config.TelegramConfig.BotID).
func NewIDTokenVerifier(clientID string) *IDTokenVerifier {
	return &IDTokenVerifier{clientID: clientID, jwksURL: telegramJWKSURL,
		http: &http.Client{Timeout: 10 * time.Second}, now: time.Now,
		keys: map[string]*rsa.PublicKey{}, used: map[[32]byte]time.Time{}}
}

func (v *IDTokenVerifier) Verify(ctx context.Context, raw string) (*TelegramIdentity, error) {
	var claims struct {
		jwt.RegisteredClaims
		ID                string `json:"id"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		Picture           string `json:"picture"`
	}
	_, err := jwt.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return v.key(ctx, kid)
	},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(telegramIssuer),
		jwt.WithAudience(v.clientID),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(idTokenLeeway),
		jwt.WithTimeFunc(v.now),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIDTokenInvalid, err)
	}
	if claims.ID == "" || strings.Trim(claims.ID, "0123456789") != "" {
		return nil, fmt.Errorf("%w: no numeric id claim", ErrIDTokenInvalid)
	}
	if !v.useOnce(raw, claims.ExpiresAt.Time) {
		return nil, fmt.Errorf("%w: used before", ErrIDTokenInvalid)
	}
	return &TelegramIdentity{ID: claims.ID, Name: strings.TrimSpace(claims.Name),
		Username: claims.PreferredUsername, Picture: claims.Picture}, nil
}

// useOnce — false when this token was verified before (kept until it expires).
func (v *IDTokenVerifier) useOnce(raw string, exp time.Time) bool {
	sum := sha256.Sum256([]byte(raw))
	now := v.now()
	v.mu.Lock()
	defer v.mu.Unlock()
	for k, until := range v.used {
		if now.After(until) {
			delete(v.used, k)
		}
	}
	if _, seen := v.used[sum]; seen {
		return false
	}
	v.used[sum] = exp.Add(idTokenLeeway)
	return true
}

// key — the JWKS key for kid; an unknown kid refetches (Telegram rotated its keys).
func (v *IDTokenVerifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	k, ok := v.keys[kid]
	stale := v.now().Sub(v.fetchedAt) > jwksRefetchGap
	v.mu.Unlock()
	if ok {
		return k, nil
	}
	if !stale {
		return nil, fmt.Errorf("unknown kid %q", kid)
	}
	keys, err := v.fetch(ctx)
	v.mu.Lock()
	defer v.mu.Unlock()
	v.fetchedAt = v.now()
	if err != nil {
		return nil, err
	}
	v.keys = keys
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("unknown kid %q", kid)
}

// fetch — the RSA keys of the JWKS (Telegram also lists EC / Ed25519 keys; we take RS256 only).
func (v *IDTokenVerifier) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return nil, err
	}
	res, err := v.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks: HTTP %d", res.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(res.Body).Decode(&set); err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}
	out := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" {
			continue
		}
		n, errN := base64.RawURLEncoding.DecodeString(k.N)
		e, errE := base64.RawURLEncoding.DecodeString(k.E)
		if errN != nil || errE != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		out[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	return out, nil
}
