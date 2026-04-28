package logging

import (
	"fmt"
	"log/slog"
	"os"

	"gomall-cli/internal/config"
)

func New(cfg config.Config) (*slog.Logger, error) {
	level, err := parseLevel(cfg.Log.Level)
	if err != nil {
		return nil, err
	}

	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	switch cfg.Log.Format {
	case "json":
		handler = slog.NewJSONHandler(os.Stderr, opts)
	case "text":
		handler = slog.NewTextHandler(os.Stderr, opts)
	default:
		return nil, fmt.Errorf("unsupported log format %q", cfg.Log.Format)
	}

	return slog.New(handler), nil
}

func parseLevel(level string) (slog.Level, error) {
	switch level {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q", level)
	}
}
