// -------------------------------------------------------------------------------
// Registry GC Activities - Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Tests pure helpers (parser, formatter, shell quoting) and config defaults.
// Activities that touch the Nomad API or SSH are exercised via the workflow
// test suite with mocks rather than directly here.
// -------------------------------------------------------------------------------

package activities

import (
	"strings"
	"testing"
)

// -------------------------------------------------------------------------
// CONFIG DEFAULTS
// -------------------------------------------------------------------------

func TestRegistryGCConfig_ApplyDefaults(t *testing.T) {
	cfg := RegistryGCConfig{}
	cfg.ApplyDefaults()
	if cfg.JobName != "registry" {
		t.Errorf("JobName = %q, want %q", cfg.JobName, "registry")
	}
	if cfg.GroupName != "registry" {
		t.Errorf("GroupName = %q, want %q (defaults to JobName)", cfg.GroupName, "registry")
	}
	if cfg.RegistryDataDir != "/mnt/gdrive/munchbox-data/registry" {
		t.Errorf("RegistryDataDir = %q, want default", cfg.RegistryDataDir)
	}
	if cfg.RegistryImage != "registry:3" {
		t.Errorf("RegistryImage = %q, want %q", cfg.RegistryImage, "registry:3")
	}
}

func TestRegistryGCConfig_ApplyDefaults_PreservesCustom(t *testing.T) {
	cfg := RegistryGCConfig{
		JobName:         "myreg",
		GroupName:       "mygroup",
		RegistryDataDir: "/custom/dir",
		RegistryImage:   "registry:2",
	}
	cfg.ApplyDefaults()
	if cfg.JobName != "myreg" || cfg.GroupName != "mygroup" ||
		cfg.RegistryDataDir != "/custom/dir" || cfg.RegistryImage != "registry:2" {
		t.Error("ApplyDefaults overwrote custom values")
	}
}

func TestRegistryGCConfig_ApplyDefaults_GroupDefaultsToJob(t *testing.T) {
	cfg := RegistryGCConfig{JobName: "alt-registry"}
	cfg.ApplyDefaults()
	if cfg.GroupName != "alt-registry" {
		t.Errorf("GroupName = %q, expected to default to JobName %q", cfg.GroupName, cfg.JobName)
	}
}

// -------------------------------------------------------------------------
// PARSE BLOBS DELETED
// -------------------------------------------------------------------------

func TestParseBlobsDeleted(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"none", "Some other output\nnothing here\n", 0},
		{"three", `Scanning manifests...
blob eligible for deletion: sha256:aaa
blob eligible for deletion: sha256:bbb
blob eligible for deletion: sha256:ccc
done`, 3},
		{"with trailing whitespace", "blob eligible for deletion: sha256:aaa  \n", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseBlobsDeleted(c.in)
			if got != c.want {
				t.Errorf("parseBlobsDeleted = %d, want %d", got, c.want)
			}
		})
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
		got := HumanBytes(c.in)
		if got != c.want {
			t.Errorf("HumanBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// -------------------------------------------------------------------------
// SHELL QUOTE
// -------------------------------------------------------------------------

func TestShellQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", `'plain'`},
		{"/path/with spaces", `'/path/with spaces'`},
		{"it's quoted", `'it'\''s quoted'`},
		{"", `''`},
	}
	for _, c := range cases {
		got := shellQuote(c.in)
		if got != c.want {
			t.Errorf("shellQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// -------------------------------------------------------------------------
// SANITY: package compiles with all the types declared
// -------------------------------------------------------------------------

func TestRegistryGCConfig_ApplyDefaults_LeavesBoolFieldsAlone(t *testing.T) {
	// DeleteUntagged and DryRun are explicit-opt-in booleans. ApplyDefaults
	// should leave the zero values in place — callers (the workflow input
	// or the periodic-trigger env vars) decide.
	cfg := RegistryGCConfig{}
	cfg.ApplyDefaults()
	if cfg.DryRun {
		t.Error("DryRun should be false by default")
	}
	if cfg.DeleteUntagged {
		t.Error("DeleteUntagged should be false by default")
	}
}

// strings is imported elsewhere in this file; this avoids the unused-import
// removal pass once we drop the placeholder test.
var _ = strings.Builder{}
