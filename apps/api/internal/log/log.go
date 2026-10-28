// Package log provides structured logging for the API process built on
// log/slog.
//
// M0 foundation only.
package log

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// New builds a JSON-backed slog.Logger at the requested level writing to w.
//
// Accepted levels are "debug", "info", "warn", and "error" (case-insensitive).
// An unrecognised level is an error.
func New(level string, w io.Writer) (*slog.Logger, error) {
	lvl, err := parseLevel(level)
	if err != nil {
		return nil, err
	}
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler), nil
}

func parseLevel(level string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q", level)
	}
}
