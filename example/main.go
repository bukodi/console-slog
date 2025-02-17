package main

import (
	"errors"
	"log/slog"
	"os"

	"github.com/ansel1/console-slog"
)

func main() {
	logger := slog.New(
		console.NewHandler(os.Stderr, &console.HandlerOptions{
			Level:              slog.LevelDebug,
			AddSource:          true,
			TruncateSourcePath: 2,
			TimeFormat:         "15:04:05.000",
		}),
	)
	slog.SetDefault(logger)
	slog.Info("Hello world!", "foo", "bar")
	slog.Debug("Debug message")
	slog.Warn("Warning message")
	slog.Error("Error message", "err", errors.New("the error"))

	logger = logger.With("foo", "bar").
		WithGroup("the-group").
		With("bar", "baz", "logger", "main")

	logger.Info("group info", "multiline", "hello\nworld", "attr", "value")
}
