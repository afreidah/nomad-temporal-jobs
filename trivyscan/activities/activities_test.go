// -------------------------------------------------------------------------------
// Trivy Scan Activities - Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Tests for config validation and helper functions. External dependencies
// (Nomad API, Trivy CLI, PostgreSQL) are not tested here.
// -------------------------------------------------------------------------------

package activities

import (
	"database/sql"
	"testing"
)

// -------------------------------------------------------------------------
// CONFIG VALIDATION
// -------------------------------------------------------------------------

func TestConfig_Validate_AllRequired(t *testing.T) {
	cfg := Config{
		TrivyServerAddr: "http://localhost:4954",
		DBHost:          "localhost",
		DBUser:          "user",
		DBPassword:      "pass",
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() unexpected error: %v", err)
	}
}

func TestConfig_Validate_MissingTrivyServer(t *testing.T) {
	cfg := Config{
		DBHost:     "localhost",
		DBUser:     "user",
		DBPassword: "pass",
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Expected error for missing TrivyServerAddr")
	}
}

func TestConfig_Validate_MissingDBHost(t *testing.T) {
	cfg := Config{
		TrivyServerAddr: "http://localhost:4954",
		DBUser:          "user",
		DBPassword:      "pass",
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Expected error for missing DBHost")
	}
}

func TestConfig_Validate_MissingDBUser(t *testing.T) {
	cfg := Config{
		TrivyServerAddr: "http://localhost:4954",
		DBHost:          "localhost",
		DBPassword:      "pass",
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Expected error for missing DBUser")
	}
}

func TestConfig_Validate_MissingDBPassword(t *testing.T) {
	cfg := Config{
		TrivyServerAddr: "http://localhost:4954",
		DBHost:          "localhost",
		DBUser:          "user",
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Expected error for missing DBPassword")
	}
}

// -------------------------------------------------------------------------
// NULL STRING HELPER
// -------------------------------------------------------------------------

func TestNullString_Empty(t *testing.T) {
	ns := nullString("")
	if ns.Valid {
		t.Error("Expected invalid NullString for empty input")
	}
}

func TestNullString_NonEmpty(t *testing.T) {
	ns := nullString("hello")
	if !ns.Valid {
		t.Error("Expected valid NullString for non-empty input")
	}
	if ns.String != "hello" {
		t.Errorf("String = %q, want %q", ns.String, "hello")
	}
}

// -------------------------------------------------------------------------
// SCAN RESULT TYPES
// -------------------------------------------------------------------------

func TestScanResult_ZeroValue(t *testing.T) {
	var r ScanResult
	if r.Status != "" {
		t.Errorf("Status = %q, want empty", r.Status)
	}
	if r.CriticalCount != 0 || r.HighCount != 0 || r.MediumCount != 0 || r.LowCount != 0 {
		t.Error("Expected zero vulnerability counts")
	}
	if len(r.Vulnerabilities) != 0 {
		t.Error("Expected empty vulnerability slice")
	}
}

// nullString is tested directly since it's an unexported helper
var _ = sql.NullString{} // ensure sql import is used
