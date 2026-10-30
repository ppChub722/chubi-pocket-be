package connections

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Bot is what the Telegram side gives this module (telegram.Client); nil = the bot is off.
type Bot interface {
	// Username — the bot's @username (without @), for the t.me link.
	Username(ctx context.Context) (string, error)
	// ProfilePhoto — the user's newest profile picture as JPEG; nil, nil = none (or hidden).
	ProfilePhoto(ctx context.Context, userID int64) ([]byte, error)
}

var (
	ErrUnknownProvider       = errors.New("unknown provider")
	ErrUnavailable           = errors.New("this app can't be linked yet")
	ErrNotLinked             = errors.New("not linked")
	ErrPermissionUnavailable = errors.New("permission not available yet")
	ErrBadPermissions        = errors.New("invalid permissions")
	ErrNoPhoto               = errors.New("the app gave no picture")
	ErrProvider              = errors.New("the app did not answer")
)

// maxPhotoBytes — a picture larger than this is refused (Telegram's 160 px JPEG is ~10 KB).
const maxPhotoBytes = 1 << 20

// codeFormat — the start payload Telegram allows: 1-64 chars of A-Z a-z 0-9 _ -.
var codeFormat = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type Service struct {
	store *Store
	bot   Bot
	auth  *auth.Service // 1b-2 log in / sign up with Telegram (nil = off)
	now   func() time.Time
}

func NewService(store *Store, bot Bot) *Service {
	return &Service{store: store, bot: bot, now: time.Now}
}

func (s *Service) available(p provider) bool {
	return p.key == ProviderTelegram && s.bot != nil
}

func (s *Service) List(ctx context.Context, userID uuid.UUID) (ConnectionList, error) {
	links, photos, err := s.load(ctx, userID)
	if err != nil {
		return ConnectionList{}, err
	}
	out := ConnectionList{Data: make([]Connection, 0, len(providers))}
	for _, p := range providers {
		out.Data = append(out.Data, s.view(p, links, photos))
	}
	out.Count = len(out.Data)
	return out, nil
}

func (s *Service) Get(ctx context.Context, userID uuid.UUID, key string) (*Connection, error) {
	p, ok := providerOf(key)
	if !ok {
		return nil, ErrUnknownProvider
	}
	links, photos, err := s.load(ctx, userID)
	if err != nil {
		return nil, err
	}
	c := s.view(p, links, photos)
	return &c, nil
}

func (s *Service) load(ctx context.Context, userID uuid.UUID) (map[string]link, map[string]photoRow, error) {
	links, err := s.store.links(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	photos, err := s.store.photos(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	return links, photos, nil
}

func (s *Service) view(p provider, links map[string]link, photos map[string]photoRow) Connection {
	c := Connection{Provider: p.key, Name: p.name, BrandColor: p.color, LogoURL: logoURL(p.key),
		Available: s.available(p), Permissions: make([]Permission, 0, len(p.perms))}
	l, linked := links[p.key]
	if linked {
		c.Linked, c.Account = true, l.Account
		at := l.LinkedAt
		c.LinkedAt = &at
	}
	if ph, ok := photos[p.key]; ok {
		c.Photo = &Photo{Source: p.key, URL: shared.AvatarPhotoURL(ph.ID, ph.Version), ImportedAt: ph.ImportedAt}
	}
	for _, d := range p.perms {
		avail := d.live && c.Available
		c.Permissions = append(c.Permissions, Permission{Key: d.key, Available: avail,
			On: avail && linked && l.Permissions[d.key]})
	}
	return c
}

// StartLink makes a one-time code (10 min) and the URL that opens the app with it.
func (s *Service) StartLink(ctx context.Context, userID uuid.UUID, key string) (*LinkStart, error) {
	p, ok := providerOf(key)
	if !ok {
		return nil, ErrUnknownProvider
	}
	if !s.available(p) {
		return nil, ErrUnavailable
	}
	bot, err := s.bot.Username(ctx)
	if err != nil || bot == "" {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	raw := make([]byte, 16) // 128 bits
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("code: %w", err)
	}
	code := base64.RawURLEncoding.EncodeToString(raw)
	expires := s.now().Add(LinkCodeTTL).UTC().Truncate(time.Second)
	if err := s.store.newCode(ctx, userID, p.key, hashCode(code), expires); err != nil {
		return nil, err
	}
	return &LinkStart{URL: "https://t.me/" + bot + "?start=" + code, ExpiresAt: expires}, nil
}

func hashCode(code string) []byte {
	h := sha256.Sum256([]byte(code))
	return h[:]
}

// Unlink — the imported picture stays (an avatar picking it falls back to the icon while unlinked).
func (s *Service) Unlink(ctx context.Context, userID uuid.UUID, key string) (*Connection, error) {
	if _, ok := providerOf(key); !ok {
		return nil, ErrUnknownProvider
	}
	if err := s.store.unlink(ctx, userID, key); err != nil {
		return nil, fmt.Errorf("unlink: %w", err)
	}
	return s.Get(ctx, userID, key)
}

// UpdatePermissions — {key: bool}; only live keys of a linked app.
func (s *Service) UpdatePermissions(ctx context.Context, userID uuid.UUID, key string, patch map[string]bool) (*Connection, error) {
	p, ok := providerOf(key)
	if !ok {
		return nil, ErrUnknownProvider
	}
	if len(patch) == 0 {
		return nil, fmt.Errorf("%w: send at least one key", ErrBadPermissions)
	}
	for k := range patch {
		var def *permDef
		for i := range p.perms {
			if p.perms[i].key == k {
				def = &p.perms[i]
			}
		}
		switch {
		case def == nil:
			return nil, fmt.Errorf("%w: unknown key %q", ErrBadPermissions, k)
		case !def.live || !s.available(p):
			return nil, fmt.Errorf("%w: %s", ErrPermissionUnavailable, k)
		}
	}
	linked, err := s.store.setPermissions(ctx, userID, key, patch)
	if err != nil {
		return nil, fmt.Errorf("permissions: %w", err)
	}
	if !linked {
		return nil, ErrNotLinked
	}
	return s.Get(ctx, userID, key)
}

// ImportPhoto fetches the user's picture from the app and stores it (it is not picked here).
func (s *Service) ImportPhoto(ctx context.Context, userID uuid.UUID, key string) (*Connection, error) {
	p, ok := providerOf(key)
	if !ok {
		return nil, ErrUnknownProvider
	}
	if !s.available(p) {
		return nil, ErrUnavailable
	}
	links, err := s.store.links(ctx, userID)
	if err != nil {
		return nil, err
	}
	l, linked := links[key]
	if !linked {
		return nil, ErrNotLinked
	}
	tgUser, err := strconv.ParseInt(l.ExternalID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("telegram id %q: %w", l.ExternalID, err)
	}
	img, err := s.bot.ProfilePhoto(ctx, tgUser)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProvider, err)
	}
	if len(img) == 0 {
		return nil, ErrNoPhoto
	}
	if len(img) > maxPhotoBytes {
		return nil, fmt.Errorf("%w: picture is %d bytes", ErrProvider, len(img))
	}
	if err := s.store.savePhoto(ctx, userID, key, img); err != nil {
		return nil, fmt.Errorf("save picture: %w", err)
	}
	return s.Get(ctx, userID, key)
}

// Image — the bytes behind an avatar photo_url; ok false = gone.
func (s *Service) Image(ctx context.Context, id uuid.UUID) ([]byte, bool, error) {
	return s.store.image(ctx, id)
}

// ── The Telegram bot's side (telegram.Accounts) ──

// TelegramUser — who a chat is linked to; linked false = nobody.
func (s *Service) TelegramUser(ctx context.Context, chatID int64) (userID uuid.UUID, chatOn, linked bool, err error) {
	id, perms, ok, err := s.store.byExternal(ctx, ProviderTelegram, strconv.FormatInt(chatID, 10))
	if err != nil || !ok {
		return uuid.Nil, false, false, err
	}
	return id, perms[PermChat], true, nil
}

// TelegramBind — /start <code>: the chat becomes the code owner's. account = "@username" or the
// first name. ok false = bad, used or expired code.
func (s *Service) TelegramBind(ctx context.Context, code string, chatID int64, account string) (displayName string, ok bool, err error) {
	if !codeFormat.MatchString(code) {
		return "", false, nil
	}
	_, name, ok, err := s.store.bind(ctx, ProviderTelegram, hashCode(code), strconv.FormatInt(chatID, 10), account,
		map[string]bool{PermChat: true})
	return name, ok, err
}

// TelegramUnlink — /unlink in the chat; false = the chat wasn't linked.
func (s *Service) TelegramUnlink(ctx context.Context, chatID int64) (bool, error) {
	return s.store.unlinkExternal(ctx, ProviderTelegram, strconv.FormatInt(chatID, 10))
}
