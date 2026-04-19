package logging

import (
	"log/slog"
	"strings"
)

// ParseLevel converts a string like "debug" / "info" / "warn" / "error" into
// a slog.Level. Unknown values default to LevelInfo.
func ParseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
