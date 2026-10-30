// Package connections is the outside apps a user links (0.4.2 1b: Telegram now, LINE / Google
// later) and the profile pictures they give: GET /connections, link codes, permissions, photo
// import, the stored pictures behind avatar.photo_url and the brand logos.
package connections

import (
	"embed"
	"fmt"
	"time"
)

const (
	ProviderTelegram = "telegram"
	ProviderLine     = "line"
	ProviderGoogle   = "google"

	PermChat = "chat"

	// LinkCodeTTL — how long a link code works (single use).
	LinkCodeTTL = 10 * time.Minute

	// logoVersion is the ?v= of every logo_url; bump it when a logo file changes.
	logoVersion = 1
)

//go:embed logos/*.png
var logos embed.FS

type permDef struct {
	key  string
	live bool // false = "เร็วๆ นี้"
}

type provider struct {
	key, name, color string
	perms            []permDef
}

// providers in display order. Colours and words are the mock's (owner 2026-10-28).
var providers = []provider{
	{ProviderTelegram, "Telegram", "#26A5E4", []permDef{{PermChat, true}, {"ask", false}, {"slip", false}, {"notify", false}}},
	{ProviderLine, "LINE", "#06C755", []permDef{{PermChat, false}, {"ask", false}, {"slip", false}, {"notify", false}}},
	{ProviderGoogle, "Google", "#4285F4", []permDef{{"login", false}, {"sheets", false}, {"calendar", false}}},
}

func providerOf(key string) (provider, bool) {
	for _, p := range providers {
		if p.key == key {
			return p, true
		}
	}
	return provider{}, false
}

func logoURL(key string) string {
	return fmt.Sprintf("/api/v1/brand-logos/%s.png?v=%d", key, logoVersion)
}

type Connection struct {
	Provider    string       `json:"provider"`
	Name        string       `json:"name"`
	BrandColor  string       `json:"brand_color"`
	LogoURL     string       `json:"logo_url"`
	Available   bool         `json:"available"`
	Linked      bool         `json:"linked"`
	Account     *string      `json:"account"`
	LinkedAt    *time.Time   `json:"linked_at"`
	Photo       *Photo       `json:"photo"`
	Permissions []Permission `json:"permissions"`
}

type Photo struct {
	Source     string    `json:"source"`
	URL        string    `json:"url"`
	ImportedAt time.Time `json:"imported_at"`
}

type Permission struct {
	Key       string `json:"key"`
	Available bool   `json:"available"`
	On        bool   `json:"on"`
}

type ConnectionList struct {
	Data  []Connection `json:"data"`
	Count int          `json:"count"`
}

type LinkStart struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}
