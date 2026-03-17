// -------------------------------------------------------------------------------
// Node Cleanup Activities - Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Tests for pure functions: output parsing, script generation, and SSH config
// construction. External dependencies (Nomad API, SSH) are not tested here.
// -------------------------------------------------------------------------------

package activities

import (
	"strings"
	"testing"
)

// -------------------------------------------------------------------------
// PARSE CLEANUP OUTPUT
// -------------------------------------------------------------------------

func TestParseCleanupOutput_ValidResult(t *testing.T) {
	output := `Running jobs on this node:
  - prometheus
  - loki

OK (active): prometheus
OK (active): loki
SKIP (system): alloc
WOULD DELETE (14d old, 1.2G): old-job

RESULT: scanned=4 orphaned=1 deleted=0 skipped=1 docker_freed=0B`

	var result CleanupResult
	parseCleanupOutput(&result, output)

	if result.Scanned != 4 {
		t.Errorf("Scanned = %d, want 4", result.Scanned)
	}
	if result.Orphaned != 1 {
		t.Errorf("Orphaned = %d, want 1", result.Orphaned)
	}
	if result.Deleted != 0 {
		t.Errorf("Deleted = %d, want 0", result.Deleted)
	}
	if result.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", result.Skipped)
	}
	if result.DockerSpaceFreed != "0B" {
		t.Errorf("DockerSpaceFreed = %q, want %q", result.DockerSpaceFreed, "0B")
	}
}

func TestParseCleanupOutput_WithDeletion(t *testing.T) {
	output := "RESULT: scanned=10 orphaned=3 deleted=3 skipped=2 docker_freed=1.5GB"

	var result CleanupResult
	parseCleanupOutput(&result, output)

	if result.Scanned != 10 {
		t.Errorf("Scanned = %d, want 10", result.Scanned)
	}
	if result.Deleted != 3 {
		t.Errorf("Deleted = %d, want 3", result.Deleted)
	}
	if result.DockerSpaceFreed != "1.5GB" {
		t.Errorf("DockerSpaceFreed = %q, want %q", result.DockerSpaceFreed, "1.5GB")
	}
}

func TestParseCleanupOutput_NoResultLine(t *testing.T) {
	output := "some random output with no RESULT line"

	var result CleanupResult
	parseCleanupOutput(&result, output)

	if result.Scanned != 0 || result.Orphaned != 0 || result.Deleted != 0 || result.Skipped != 0 {
		t.Error("Expected all zero values when no RESULT line present")
	}
}

func TestParseCleanupOutput_EmptyOutput(t *testing.T) {
	var result CleanupResult
	parseCleanupOutput(&result, "")

	if result.Scanned != 0 {
		t.Errorf("Scanned = %d, want 0", result.Scanned)
	}
}

// -------------------------------------------------------------------------
// BUILD CLEANUP SCRIPT
// -------------------------------------------------------------------------

func TestBuildCleanupScript_DryRun(t *testing.T) {
	script := buildCleanupScript("node-123", "10.0.0.1:4646", "/opt/nomad/data", 7, true, false, "", "my-token")

	if !strings.Contains(script, `DRY_RUN="true"`) {
		t.Error("Expected DRY_RUN=true in script")
	}
	if !strings.Contains(script, `DOCKER_PRUNE="false"`) {
		t.Error("Expected DOCKER_PRUNE=false in script")
	}
	if !strings.Contains(script, `NODE_ID="node-123"`) {
		t.Error("Expected NODE_ID in script")
	}
	if !strings.Contains(script, `NOMAD_HTTP_ADDR="10.0.0.1:4646"`) {
		t.Error("Expected NOMAD_HTTP_ADDR in script")
	}
	if !strings.Contains(script, `GRACE_DAYS=7`) {
		t.Error("Expected GRACE_DAYS=7 in script")
	}
}

func TestBuildCleanupScript_LiveMode(t *testing.T) {
	script := buildCleanupScript("node-456", "10.0.0.2:4646", "/opt/nomad/data", 14, false, true, "", "token")

	if !strings.Contains(script, `DRY_RUN="false"`) {
		t.Error("Expected DRY_RUN=false in script")
	}
	if !strings.Contains(script, `DOCKER_PRUNE="true"`) {
		t.Error("Expected DOCKER_PRUNE=true in script")
	}
	if !strings.Contains(script, `GRACE_DAYS=14`) {
		t.Error("Expected GRACE_DAYS=14 in script")
	}
}

func TestBuildCleanupScript_SudoPrefix(t *testing.T) {
	script := buildCleanupScript("node-789", "10.0.0.3:4646", "/opt/nomad/data", 7, true, true, "sudo ", "token")

	// sudo should appear before commands like curl, jq, stat, du, rm, docker
	if !strings.Contains(script, "sudo curl") {
		t.Error("Expected sudo prefix on curl")
	}
	if !strings.Contains(script, "sudo docker") {
		t.Error("Expected sudo prefix on docker commands")
	}
}

// -------------------------------------------------------------------------
// CONFIG VALIDATION
// -------------------------------------------------------------------------

func TestConfig_Validate_Defaults(t *testing.T) {
	cfg := Config{}
	cfg.Validate()

	if cfg.SSHKeyPath != "/root/.ssh/id_ed25519" {
		t.Errorf("SSHKeyPath = %q, want default", cfg.SSHKeyPath)
	}
	if cfg.SSHCertPath != "/root/.ssh/id_ed25519-cert.pub" {
		t.Errorf("SSHCertPath = %q, want default", cfg.SSHCertPath)
	}
	if cfg.SSHHostCAPath != "/root/.ssh/ssh-host-ca.pub" {
		t.Errorf("SSHHostCAPath = %q, want default", cfg.SSHHostCAPath)
	}
}

func TestConfig_Validate_PreservesCustomValues(t *testing.T) {
	cfg := Config{
		SSHKeyPath:    "/custom/key",
		SSHCertPath:   "/custom/cert",
		SSHHostCAPath: "/custom/ca",
	}
	cfg.Validate()

	if cfg.SSHKeyPath != "/custom/key" {
		t.Errorf("SSHKeyPath = %q, want /custom/key", cfg.SSHKeyPath)
	}
}
