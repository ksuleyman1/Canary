package logger

import (
	"log/slog"
	"os"
)

// Log is the global structured logger instance
var Log *slog.Logger

// Init initializes the global logger with the specified level and format
func Init(level, format string) {
	opts := &slog.HandlerOptions{
		Level: parseLevel(level),
	}

	var handler slog.Handler
	switch format {
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, opts)
	case "text":
		handler = slog.NewTextHandler(os.Stdout, opts)
	default:
		// Default to JSON for production environments
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	Log = slog.New(handler)
}

// parseLevel converts a string log level to slog.Level
func parseLevel(s string) slog.Level {
	switch s {
	case "DEBUG":
		return slog.LevelDebug
	case "INFO":
		return slog.LevelInfo
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
