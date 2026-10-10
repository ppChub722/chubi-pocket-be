// Package appversion — the app version check (owner 2026-10-10). The app
// sends its build number in X-App-Build on every call; builds older than
// MIN_APP_BUILD get 426 APP_OUTDATED with where to download the new one.
// GET /v1/app/version tells the app the same up front.
package appversion

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

// Header carries the app's build number (an integer, e.g. 42).
const Header = "X-App-Build"

// CodeOutdated — the error code on the 426.
const CodeOutdated = "APP_OUTDATED"

// Info — GET /v1/app/version's data, and the 426's details.
type Info struct {
	MinBuild    int    `json:"min_build"`    // 0 = check off
	LatestBuild int    `json:"latest_build"` // 0 = not set
	DownloadURL string `json:"download_url"`
	MessageTH   string `json:"message_th,omitempty"`
	MessageEN   string `json:"message_en,omitempty"`
}

type Handler struct {
	info Info
}

func NewHandler(info Info) *Handler {
	return &Handler{info: info}
}

// Version implements GET /v1/app/version (public, never blocked).
func (h *Handler) Version(c *gin.Context) {
	response.OK(c, "App version", h.info)
}

// Middleware blocks builds older than MinBuild. No header, or one that
// isn't a number (web, Postman, scripts), passes. `exempt` paths (full
// paths, e.g. "/api/v1/app/version") always pass.
func (h *Handler) Middleware(exempt ...string) gin.HandlerFunc {
	skip := make(map[string]bool, len(exempt))
	for _, p := range exempt {
		skip[p] = true
	}
	return func(c *gin.Context) {
		if h.info.MinBuild <= 0 || skip[c.Request.URL.Path] {
			c.Next()
			return
		}
		build, err := strconv.Atoi(strings.TrimSpace(c.GetHeader(Header)))
		if err != nil || build >= h.info.MinBuild {
			c.Next()
			return
		}
		response.Fail(c, http.StatusUpgradeRequired, CodeOutdated,
			fmt.Sprintf("App build %d is older than the minimum %d — please update", build, h.info.MinBuild),
			h.info)
		c.Abort()
	}
}
