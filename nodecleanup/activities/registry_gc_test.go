// -------------------------------------------------------------------------------
// Registry GC Activity - Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Tests pure functions: script generation, output parsing, byte humanization.
// External dependencies (Nomad API, SSH, docker run) are not exercised.
// -------------------------------------------------------------------------------

package activities

import (
	"strings"
	"testing"
)

// -------------------------------------------------------------------------
// PARSE REGISTRY GC OUTPUT
// -------------------------------------------------------------------------

func TestParseRegistryGCOutput_FullOutput(t *testing.T) {
	output := `Scaling registry to count=0
Waiting for registry allocs to drain (running=1)...
=== Running registry garbage-collect ===
blob eligible for deletion: sha256:aaa
blob eligible for deletion: sha256:bbb
blob eligible for deletion: sha256:ccc
=== End of registry garbage-collect output ===
BEFORE_BYTES=10737418240
BEFORE_HUMAN=10G
AFTER_BYTES=8589934592
AFTER_HUMAN=8.0G
BLOBS_DELETED=3
RECLAIMED_BYTES=2147483648
RESULT: blobs_deleted=3 reclaimed_bytes=2147483648 before_bytes=10737418240 after_bytes=8589934592`

	var result RegistryGCResult
	parseRegistryGCOutput(&result, output)

	if result.BlobsDeleted != 3 {
		t.Errorf("BlobsDeleted = %d, want 3", result.BlobsDeleted)
	}
	if result.BeforeBytes != "10G" {
		t.Errorf("BeforeBytes = %q, want %q", result.BeforeBytes, "10G")
	}
	if result.AfterBytes != "8.0G" {
		t.Errorf("AfterBytes = %q, want %q", result.AfterBytes, "8.0G")
	}
	if result.BytesReclaimed != "2.0GiB" {
		t.Errorf("BytesReclaimed = %q, want %q", result.BytesReclaimed, "2.0GiB")
	}
}

func TestParseRegistryGCOutput_NoResultLine(t *testing.T) {
	output := `BEFORE_HUMAN=5G
some random output without RESULT line`

	var result RegistryGCResult
	parseRegistryGCOutput(&result, output)

	if result.BlobsDeleted != 0 {
		t.Errorf("BlobsDeleted = %d, want 0", result.BlobsDeleted)
	}
	if result.BeforeBytes != "5G" {
		t.Errorf("BeforeBytes = %q, want %q", result.BeforeBytes, "5G")
	}
}

func TestParseRegistryGCOutput_Empty(t *testing.T) {
	var result RegistryGCResult
	parseRegistryGCOutput(&result, "")

	if result.BlobsDeleted != 0 || result.BytesReclaimed != "" {
		t.Error("Expected zero values for empty input")
	}
}

// -------------------------------------------------------------------------
// BUILD REGISTRY GC SCRIPT
// -------------------------------------------------------------------------

func TestBuildRegistryGCScript_DryRunWithDeleteUntagged(t *testing.T) {
	cfg := RegistryGCConfig{
		JobName:         "registry",
		RegistryDataDir: "/mnt/gdrive/munchbox-data/registry",
		RegistryImage:   "registry:3",
		DryRun:          true,
		DeleteUntagged:  true,
	}
	script := buildRegistryGCScript(cfg, "10.0.0.1:4646", "tok", "")

	for _, want := range []string{
		`JOB_NAME="registry"`,
		`DATA_DIR="/mnt/gdrive/munchbox-data/registry"`,
		`IMAGE="registry:3"`,
		`DRY_RUN_FLAG="--dry-run"`,
		`DELETE_UNTAGGED_FLAG="--delete-untagged"`,
		`scale_job 0`,
		`scale_job 1`,
		`wait_for_no_running_allocs`,
		`wait_for_running_alloc`,
		`docker run --rm`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("Expected script to contain %q", want)
		}
	}
}

func TestBuildRegistryGCScript_LiveRunWithoutFlags(t *testing.T) {
	cfg := RegistryGCConfig{
		JobName:         "registry",
		RegistryDataDir: "/data",
		RegistryImage:   "registry:3",
		DryRun:          false,
		DeleteUntagged:  false,
	}
	script := buildRegistryGCScript(cfg, "10.0.0.1:4646", "tok", "")

	if !strings.Contains(script, `DRY_RUN_FLAG=""`) {
		t.Error("Expected empty DRY_RUN_FLAG when DryRun=false")
	}
	if !strings.Contains(script, `DELETE_UNTAGGED_FLAG=""`) {
		t.Error("Expected empty DELETE_UNTAGGED_FLAG when DeleteUntagged=false")
	}
}

func TestBuildRegistryGCScript_SudoPrefix(t *testing.T) {
	cfg := RegistryGCConfig{
		JobName:         "registry",
		RegistryDataDir: "/data",
		RegistryImage:   "registry:3",
		DeleteUntagged:  true,
	}
	script := buildRegistryGCScript(cfg, "10.0.0.1:4646", "tok", "sudo ")

	for _, want := range []string{"sudo curl", "sudo du", "sudo docker"} {
		if !strings.Contains(script, want) {
			t.Errorf("Expected sudo prefix on %q", strings.TrimPrefix(want, "sudo "))
		}
	}
}

// -------------------------------------------------------------------------
// HUMAN BYTES
// -------------------------------------------------------------------------

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{2048, "2.0KiB"},
		{2 * 1024 * 1024, "2.0MiB"},
		{2 * 1024 * 1024 * 1024, "2.0GiB"},
		{150 * 1024 * 1024, "150MiB"},
		{-1, "-1B"},
	}
	for _, c := range cases {
		got := humanBytes(c.in)
		if got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
