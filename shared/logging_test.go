// -------------------------------------------------------------------------------
// Shared Logging - Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Tests for the structured logger factory.
// -------------------------------------------------------------------------------

package shared

import (
	"testing"
)

// TestNewLogger_ReturnsNonNil verifies both the Temporal logger and slog
// logger are created successfully.
func TestNewLogger_ReturnsNonNil(t *testing.T) {
	temporalLogger, slogger := NewLogger("test-service")

	if temporalLogger == nil {
		t.Error("Expected non-nil Temporal logger")
	}
	if slogger == nil {
		t.Error("Expected non-nil slog logger")
	}
}
