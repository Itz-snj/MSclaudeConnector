package logger

import (
	"log/slog"
	"os"
)

// Logger is a small alias so callers can depend on a project-specific type.
type Logger = *slog.Logger

func New() Logger {
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	return slog.New(handler)
}

func NewWithLevel(level slog.Level) Logger {
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})
	return slog.New(handler)
}
