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
	"strings"
	"testing"

	"go.temporal.io/sdk/testsuite"
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

// -------------------------------------------------------------------------
// SCAN CONFIG DEFAULTS
// -------------------------------------------------------------------------

func TestScanConfig_ApplyDefaults(t *testing.T) {
	var c ScanConfig
	c.ApplyDefaults()
	if c.Concurrency != 10 {
		t.Errorf("Concurrency = %d, want 10", c.Concurrency)
	}
}

func TestScanConfig_ApplyDefaults_PreservesValue(t *testing.T) {
	c := ScanConfig{Concurrency: 4}
	c.ApplyDefaults()
	if c.Concurrency != 4 {
		t.Errorf("ApplyDefaults overwrote set value: %d", c.Concurrency)
	}
}

// nullString is tested directly since it's an unexported helper
var _ = sql.NullString{} // ensure sql import is used

// -------------------------------------------------------------------------
// TRIVY ERROR CLASSIFICATION
// -------------------------------------------------------------------------

func TestClassifyTrivyError(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   scanErrClass
	}{
		{"manifest unknown is permanent", "manifest unknown: image gone", scanErrPermanent},
		{"not found is permanent", "GET https://...: not found", scanErrPermanent},
		{"connection refused is transient", "dial tcp: connection refused", scanErrTransient},
		{"timeout is transient", "context deadline exceeded: timeout", scanErrTransient},
		{"connection reset is transient", "read tcp: connection reset by peer", scanErrTransient},
		{"unrecognized is unknown", "something else exploded", scanErrUnknown},
		{"empty is unknown", "", scanErrUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyTrivyError(tt.stderr); got != tt.want {
				t.Errorf("classifyTrivyError(%q) = %d, want %d", tt.stderr, got, tt.want)
			}
		})
	}
}

// -------------------------------------------------------------------------
// TRIVY OUTPUT PARSING
// -------------------------------------------------------------------------

func TestParseTrivyOutput_DedupAndCount(t *testing.T) {
	longDesc := strings.Repeat("a", 1500)
	raw := `{"Results":[
		{"Vulnerabilities":[
			{"VulnerabilityID":"CVE-1","Severity":"CRITICAL","Description":"` + longDesc + `"},
			{"VulnerabilityID":"CVE-2","Severity":"high"},
			{"VulnerabilityID":"CVE-1","Severity":"CRITICAL"}
		]},
		{"Vulnerabilities":[
			{"VulnerabilityID":"CVE-3","Severity":"MEDIUM"},
			{"VulnerabilityID":"CVE-4","Severity":"LOW"},
			{"VulnerabilityID":"CVE-5","Severity":"UNKNOWN"}
		]}
	]}`

	vulns, counts, err := parseTrivyOutput([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// CVE-1 appears twice and must dedup to one of five unique entries.
	if len(vulns) != 5 {
		t.Fatalf("got %d vulns, want 5 (deduped)", len(vulns))
	}
	// "high" (lowercase) still counts via ToUpper; UNKNOWN falls through uncounted.
	want := SeverityCounts{Critical: 1, High: 1, Medium: 1, Low: 1}
	if counts != want {
		t.Errorf("counts = %+v, want %+v", counts, want)
	}
	// The 1500-char description is truncated to exactly maxDescriptionLen with
	// a trailing ellipsis.
	got := vulns[0].Description
	if len(got) != maxDescriptionLen {
		t.Errorf("description length = %d, want %d", len(got), maxDescriptionLen)
	} else if !strings.HasSuffix(got, "...") {
		t.Errorf("truncated description should end with an ellipsis")
	}
}

func TestParseTrivyOutput_InvalidJSON(t *testing.T) {
	if _, _, err := parseTrivyOutput([]byte("not json")); err == nil {
		t.Error("expected an error for invalid JSON")
	}
}

func TestParseTrivyOutput_Empty(t *testing.T) {
	vulns, counts, err := parseTrivyOutput([]byte(`{"Results":[]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vulns) != 0 || counts != (SeverityCounts{}) {
		t.Errorf("expected empty result, got %d vulns, counts %+v", len(vulns), counts)
	}
}

// -------------------------------------------------------------------------
// SCAN IMAGE
// -------------------------------------------------------------------------

// TestScanImage_BinaryFailure runs ScanImage where the trivy binary can't run
// (it isn't present at trivyBin in the test environment), so cmd.Run fails.
// ScanImage must surface that as an error -- so Temporal retries -- rather than
// reporting a silent success. Exercises the command build and the failure path.
func TestScanImage_BinaryFailure(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	a := &Activities{config: Config{TrivyServerAddr: "127.0.0.1:0"}}
	env.RegisterActivity(a.ScanImage)

	if _, err := env.ExecuteActivity(a.ScanImage, "alpine:3"); err == nil {
		t.Fatal("expected an error when the trivy binary cannot run, got nil")
	}
}
