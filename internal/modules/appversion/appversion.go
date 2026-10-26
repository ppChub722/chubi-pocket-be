package appversion

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

const Header = "X-App-Build"

const CodeOutdated = "APP_OUTDATED"

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
	c.JSON(http.StatusOK, h.info.view())
}

// PubspecBuild — the app's build as pubspec has it. The APKs are split per ABI, so Android's
// versionCode (what a phone sends) is ABI × 1000 + build: 2170 on arm64 is build 170. Holds while
// the build stays under 1000.
func PubspecBuild(build int) int {
	if build >= 1000 {
		return build % 1000
	}
	return build
}

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
		build = PubspecBuild(build)
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

type VersionInfo struct {
	MinBuild    int     `json:"min_build"`
	LatestBuild int     `json:"latest_build"`
	DownloadURL string  `json:"download_url"`
	MessageTH   *string `json:"message_th"`
	MessageEN   *string `json:"message_en"`
}

func (i Info) view() VersionInfo {
	v := VersionInfo{MinBuild: i.MinBuild, LatestBuild: i.LatestBuild, DownloadURL: i.DownloadURL}
	if i.MessageTH != "" {
		v.MessageTH = &i.MessageTH
	}
	if i.MessageEN != "" {
		v.MessageEN = &i.MessageEN
	}
	return v
}
