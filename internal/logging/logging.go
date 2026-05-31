// Package logging builds the slog logger used across the server and client.
//
// Format is cloud-native by default: "json" for machines, "text" for humans,
// and "auto" (the default) which picks text on a TTY and json otherwise.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// New returns a configured *slog.Logger. level is one of
// debug|info|warn|error; format is auto|text|json.
func New(level, format string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}

	useJSON := false
	switch strings.ToLower(format) {
	case "json":
		useJSON = true
	case "text":
		useJSON = false
	default: // auto
		useJSON = !isatty(os.Stderr)
	}

	var h slog.Handler
	if useJSON {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

// isatty reports whether f is a character device (terminal).
func isatty(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
