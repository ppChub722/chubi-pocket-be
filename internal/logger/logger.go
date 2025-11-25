package logger

import (
	"log/slog"
	"os"
)

// New returns a new slog logger based on the application environment.
// It uses a JSON handler for "production" and a text handler for others.
func New(env string) *slog.Logger {
	var handler slog.Handler

	opts := &slog.HandlerOptions{
		// Add source file and line number to logs
		AddSource: true,
	}

	switch env {
	case "production":
		handler = slog.NewJSONHandler(os.Stdout, opts)
	default:
		// Use a more readable text handler for development
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	logger := slog.New(handler)
	return logger
}
