package logging

import (
	"io"
	"log/slog"
)

func NewJSON(writer io.Writer, level slog.Leveler) *slog.Logger {
	if level == nil {
		level = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: level}))
}
