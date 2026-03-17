// -------------------------------------------------------------------------------
// Shared Logging - Structured slog Logger for Temporal Workers
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Provides a JSON-formatted slog logger compatible with the Temporal SDK's
// log adapter. Outputs structured logs to stdout for Alloy/Loki collection.
// -------------------------------------------------------------------------------

package shared

import (
	"log/slog"
	"os"

	tlog "go.temporal.io/sdk/log"
)

// NewLogger creates a JSON slog logger wrapped for Temporal SDK compatibility.
// Returns both the Temporal-compatible logger (for client/worker options) and
// the underlying slog logger (for use outside Temporal contexts).
func NewLogger(serviceName string) (tlog.Logger, *slog.Logger) {
	slogger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})).With(slog.String("service", serviceName))

	return tlog.NewStructuredLogger(slogger), slogger
}
