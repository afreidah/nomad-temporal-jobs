// -------------------------------------------------------------------------------
// Backup Activities - Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Tests for config validation, quota error detection, and helper functions.
// External dependencies (S3, Nomad CLI, Consul CLI, pg_dumpall) are not
// tested here.
// -------------------------------------------------------------------------------

package activities

import (
	"fmt"
	"testing"
)

// -------------------------------------------------------------------------
// CONFIG VALIDATION
// -------------------------------------------------------------------------

func TestConfig_Validate_AllRequired(t *testing.T) {
	cfg := Config{
		S3Endpoint:  "http://localhost:9000",
		S3Bucket:    "test",
		S3AccessKey: "access",
		S3SecretKey: "secret",
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() unexpected error: %v", err)
	}
}

func TestConfig_ApplyDefaults(t *testing.T) {
	cfg := Config{
		S3Endpoint:  "http://localhost:9000",
		S3Bucket:    "test",
		S3AccessKey: "access",
		S3SecretKey: "secret",
	}
	cfg.ApplyDefaults()

	if cfg.NomadBackupDir != "/mnt/gdrive/nomad-snapshots" {
		t.Errorf("NomadBackupDir = %q, want default", cfg.NomadBackupDir)
	}
	if cfg.ConsulBackupDir != "/mnt/gdrive/consul-snapshots" {
		t.Errorf("ConsulBackupDir = %q, want default", cfg.ConsulBackupDir)
	}
	if cfg.PostgresBackupDir != "/mnt/gdrive/postgres-backups" {
		t.Errorf("PostgresBackupDir = %q, want default", cfg.PostgresBackupDir)
	}
	if cfg.RegistryBackupDir != "/mnt/gdrive/registry-backups" {
		t.Errorf("RegistryBackupDir = %q, want default", cfg.RegistryBackupDir)
	}
	if cfg.PostgresHost != "postgres-primary.service.consul" {
		t.Errorf("PostgresHost = %q, want default", cfg.PostgresHost)
	}
	if cfg.PostgresUser != "postgres" {
		t.Errorf("PostgresUser = %q, want default", cfg.PostgresUser)
	}
}

func TestConfig_ApplyDefaults_PreservesCustomDirs(t *testing.T) {
	cfg := Config{
		S3Endpoint:        "http://localhost:9000",
		S3Bucket:          "test",
		S3AccessKey:       "access",
		S3SecretKey:       "secret",
		NomadBackupDir:    "/custom/nomad",
		ConsulBackupDir:   "/custom/consul",
		PostgresBackupDir: "/custom/postgres",
		RegistryBackupDir: "/custom/registry",
	}
	cfg.ApplyDefaults()

	if cfg.NomadBackupDir != "/custom/nomad" {
		t.Errorf("NomadBackupDir = %q, want /custom/nomad", cfg.NomadBackupDir)
	}
}

func TestConfig_Validate_MissingEndpoint(t *testing.T) {
	cfg := Config{S3Bucket: "test", S3AccessKey: "a", S3SecretKey: "s"}
	if err := cfg.Validate(); err == nil {
		t.Error("Expected error for missing S3Endpoint")
	}
}

func TestConfig_Validate_MissingBucket(t *testing.T) {
	cfg := Config{S3Endpoint: "http://localhost:9000", S3AccessKey: "a", S3SecretKey: "s"}
	if err := cfg.Validate(); err == nil {
		t.Error("Expected error for missing S3Bucket")
	}
}

func TestConfig_Validate_MissingAccessKey(t *testing.T) {
	cfg := Config{S3Endpoint: "http://localhost:9000", S3Bucket: "test", S3SecretKey: "s"}
	if err := cfg.Validate(); err == nil {
		t.Error("Expected error for missing S3AccessKey")
	}
}

func TestConfig_Validate_MissingSecretKey(t *testing.T) {
	cfg := Config{S3Endpoint: "http://localhost:9000", S3Bucket: "test", S3AccessKey: "a"}
	if err := cfg.Validate(); err == nil {
		t.Error("Expected error for missing S3SecretKey")
	}
}

// -------------------------------------------------------------------------
// QUOTA ERROR DETECTION
// -------------------------------------------------------------------------

func TestIsQuotaError_InsufficientStorage(t *testing.T) {
	err := fmt.Errorf("InsufficientStorage: bucket quota exceeded")
	if !isQuotaError(err) {
		t.Error("Expected true for InsufficientStorage error")
	}
}

func TestIsQuotaError_507(t *testing.T) {
	err := fmt.Errorf("upload failed: 507 status code")
	if !isQuotaError(err) {
		t.Error("Expected true for 507 error")
	}
}

func TestIsQuotaError_OtherError(t *testing.T) {
	err := fmt.Errorf("connection refused")
	if isQuotaError(err) {
		t.Error("Expected false for non-quota error")
	}
}

func TestIsQuotaError_404(t *testing.T) {
	err := fmt.Errorf("404 not found")
	if isQuotaError(err) {
		t.Error("Expected false for 404 error")
	}
}

// -------------------------------------------------------------------------
// BACKUP RESULT TYPES
// -------------------------------------------------------------------------

func TestBackupResult_ZeroValue(t *testing.T) {
	var r BackupResult
	if r.Success {
		t.Error("Expected Success=false for zero value")
	}
	if r.NomadSnapshot != "" || r.ConsulSnapshot != "" || r.PostgresGlobals != "" {
		t.Error("Expected empty paths for zero value")
	}
	if len(r.PostgresDatabases) != 0 {
		t.Error("Expected no databases for zero value")
	}
}

// -------------------------------------------------------------------------
// BACKUP CONFIG DEFAULTS
// -------------------------------------------------------------------------

func TestBackupConfig_ApplyDefaults(t *testing.T) {
	var c BackupConfig
	c.ApplyDefaults()
	if c.LocalDays != 7 {
		t.Errorf("LocalDays = %d, want 7", c.LocalDays)
	}
	if c.S3Days != 30 {
		t.Errorf("S3Days = %d, want 30", c.S3Days)
	}
	if c.DumpConcurrency != 4 {
		t.Errorf("DumpConcurrency = %d, want 4", c.DumpConcurrency)
	}
}

func TestBackupConfig_ApplyDefaults_PreservesValues(t *testing.T) {
	c := BackupConfig{LocalDays: 3, S3Days: 14, DumpConcurrency: 8}
	c.ApplyDefaults()
	if c.LocalDays != 3 || c.S3Days != 14 || c.DumpConcurrency != 8 {
		t.Errorf("ApplyDefaults overwrote set values: %+v", c)
	}
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

func TestSanitizeDBName(t *testing.T) {
	cases := map[string]string{
		"app":            "app",
		"my_db-1":        "my_db-1",
		"weird name":     "weird_name",
		"drop;table":     "drop_table",
		"a/b\\c":         "a_b_c",
		"backups.legacy": "backups.legacy",
	}
	for in, want := range cases {
		if got := SanitizeDBName(in); got != want {
			t.Errorf("SanitizeDBName(%q) = %q, want %q", in, got, want)
		}
	}
}
