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

func TestConfig_Validate_AppliesDefaults(t *testing.T) {
	cfg := Config{
		S3Endpoint:  "http://localhost:9000",
		S3Bucket:    "test",
		S3AccessKey: "access",
		S3SecretKey: "secret",
	}
	_ = cfg.Validate()

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
	if cfg.RegistryDataDir != "/mnt/gdrive/munchbox-data/registry" {
		t.Errorf("RegistryDataDir = %q, want default", cfg.RegistryDataDir)
	}
}

func TestConfig_Validate_PreservesCustomDirs(t *testing.T) {
	cfg := Config{
		S3Endpoint:        "http://localhost:9000",
		S3Bucket:          "test",
		S3AccessKey:       "access",
		S3SecretKey:       "secret",
		NomadBackupDir:    "/custom/nomad",
		ConsulBackupDir:   "/custom/consul",
		PostgresBackupDir: "/custom/postgres",
		RegistryBackupDir: "/custom/registry",
		RegistryDataDir:   "/custom/data",
	}
	_ = cfg.Validate()

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
	if r.NomadSnapshot != "" || r.ConsulSnapshot != "" || r.PostgresBackup != "" {
		t.Error("Expected empty paths for zero value")
	}
}

func TestRetentionConfig_ZeroValue(t *testing.T) {
	var r RetentionConfig
	if r.LocalDays != 0 || r.S3Days != 0 {
		t.Error("Expected zero retention days for zero value")
	}
}
