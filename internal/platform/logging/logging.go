// Package logging provides structured JSON logging via log/slog. Message
// bodies must never be logged (INSTRUCTIONS.md §37); loggers here only ever
// carry identifiers (request_id, event_id, channel_id, region, shard).
package logging

import (
	"log/slog"
	"os"
)

func New(service, region string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	return slog.New(handler).With(
		slog.String("service", service),
		slog.String("region", region),
	)
}
