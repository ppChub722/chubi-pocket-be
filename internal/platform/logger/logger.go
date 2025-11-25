package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
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

func New(env string) *slog.Logger {
	var handler slog.Handler

	if env == "production" {
		// Prod: JSON format (Machine readable)
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level:     slog.LevelInfo,
			AddSource: true,
		})
	} else {
		// Dev: Custom Pretty Logger
		handler = NewDevLogger(os.Stdout)
	}

	return slog.New(handler)
}

// ---------------------------------------------------------
// Custom Development Logger
// ---------------------------------------------------------

type DevLogHandler struct {
	out io.Writer
	mu  *sync.Mutex
}

func NewDevLogger(out io.Writer) *DevLogHandler {
	return &DevLogHandler{
		out: out,
		mu:  &sync.Mutex{},
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

	// 5. Print Attributes (Dimmed keys)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(h.out, " %s%s=%v%s", Gray, a.Key, a.Value, Reset)
		return true
	})

	fmt.Fprintln(h.out)
	return nil
}

// Boilerplate to satisfy interface
func (h *DevLogHandler) Enabled(ctx context.Context, level slog.Level) bool { return true }
func (h *DevLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler           { return h }
func (h *DevLogHandler) WithGroup(name string) slog.Handler                 { return h }