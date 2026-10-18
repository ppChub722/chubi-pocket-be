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

func New(env, level string) *slog.Logger {
	minLevel := ParseLevel(level, env)

	var handler slog.Handler
	if env == "production" {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level:     minLevel,
			AddSource: true,
		})
	} else {
		handler = NewDevLogger(os.Stdout, minLevel)
	}

	return slog.New(handler)
}

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

	timeStr := fmt.Sprintf("%s%s%s", Gray, r.Time.Format("15:04:05"), Reset)

	sourceStr := ""
	if r.PC != 0 {
		fs := runtime.CallersFrames([]uintptr{r.PC})
		f, _ := fs.Next()
		sourceStr = fmt.Sprintf("%s%s:%d%s", Gray, filepath.Base(f.File), f.Line, Reset)
	}

	fmt.Fprintf(h.out, "%s | %s%s%s | %-15s > %s",
		timeStr,
		levelColor, levelLabel, Reset,
		sourceStr,
		r.Message,
	)

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
