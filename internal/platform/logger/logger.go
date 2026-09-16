package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// ANSI Color Codes
const (
	Reset   = "\033[0m"
	Red     = "\033[31m"
	Green   = "\033[32m"
	Yellow  = "\033[33m"
	Blue    = "\033[34m"
	Magenta = "\033[35m"
	Cyan    = "\033[36m"
	Gray    = "\033[90m"
)

// New builds the app logger. `level` comes from the LOG_LEVEL env var (read
// by the config package and passed through main.go — config.Load() already
// runs before logger construction, so no direct os.Getenv here). Accepted
// values: debug|info|warn|error. Empty/unknown falls back to debug in
// development, info otherwise (logging-plan.md, component 1).
func New(env, level string) *slog.Logger {
	minLevel := ParseLevel(level, env)

	var handler slog.Handler
	if env == "production" {
		// Prod: JSON format (Machine readable)
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level:     minLevel,
			AddSource: true,
		})
	} else {
		// Dev: Custom Pretty Logger
		handler = NewDevLogger(os.Stdout, minLevel)
	}

	return slog.New(handler)
}

// ParseLevel maps a LOG_LEVEL string to a slog.Level. Unknown or empty
// values default per environment: debug when APP_ENV=development, info
// otherwise.
func ParseLevel(level, env string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		if env == "development" {
			return slog.LevelDebug
		}
		return slog.LevelInfo
	}
}

// ---------------------------------------------------------
// Custom Development Logger
// ---------------------------------------------------------

type DevLogHandler struct {
	out   io.Writer
	mu    *sync.Mutex
	level slog.Level
	attrs []slog.Attr // pre-bound attrs from Logger.With(...)
	group string      // dot-joined group prefix from WithGroup
}

func NewDevLogger(out io.Writer, level slog.Level) *DevLogHandler {
	return &DevLogHandler{
		out:   out,
		mu:    &sync.Mutex{},
		level: level,
	}
}

func (h *DevLogHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// 1. Colorize the Level
	levelColor := Reset
	levelLabel := r.Level.String()

	switch r.Level {
	case slog.LevelDebug:
		levelColor = Magenta
		levelLabel = "DEBG"
	case slog.LevelInfo:
		levelColor = Green
		levelLabel = "INFO"
	case slog.LevelWarn:
		levelColor = Yellow
		levelLabel = "WARN"
	case slog.LevelError:
		levelColor = Red
		levelLabel = "ERRO"
	}

	// 2. Format Time (Dimmed)
	// Format: 15:04:05 (We don't need the date in dev usually, just time)
	timeStr := fmt.Sprintf("%s%s%s", Gray, r.Time.Format("15:04:05"), Reset)

	// 3. Format Source (Minimal: "main.go:50")
	sourceStr := ""
	if r.PC != 0 {
		fs := runtime.CallersFrames([]uintptr{r.PC})
		f, _ := fs.Next()
		// Only take the filename and line number
		sourceStr = fmt.Sprintf("%s%s:%d%s", Gray, filepath.Base(f.File), f.Line, Reset)
	}

	// 4. Print Log Line
	// Format: TIME | LEVEL | SOURCE > Message key=value
	fmt.Fprintf(h.out, "%s | %s%s%s | %-15s > %s",
		timeStr,
		levelColor, levelLabel, Reset,
		sourceStr,
		r.Message,
	)

	// 5. Print Attributes (Dimmed keys). Pre-bound attrs (Logger.With — e.g.
	// request_id from FromCtx) first, then the record's own attrs.
	for _, a := range h.attrs {
		h.printAttr(a)
	}
	r.Attrs(func(a slog.Attr) bool {
		h.printAttr(a)
		return true
	})

	fmt.Fprintln(h.out)
	return nil
}

func (h *DevLogHandler) printAttr(a slog.Attr) {
	key := a.Key
	if h.group != "" {
		key = h.group + "." + key
	}
	fmt.Fprintf(h.out, " %s%s=%v%s", Gray, key, a.Value, Reset)
}

func (h *DevLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *DevLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	clone := *h
	clone.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &clone
}

func (h *DevLogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	if h.group == "" {
		clone.group = name
	} else {
		clone.group = h.group + "." + name
	}
	return &clone
}
