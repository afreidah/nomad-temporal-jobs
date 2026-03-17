// -------------------------------------------------------------------------------
// Backup Workflow - Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Tests workflow orchestration using the Temporal test suite. Activities are
// mocked to verify the workflow executes snapshots sequentially, handles S3
// upload failures gracefully, and applies retention defaults.
// -------------------------------------------------------------------------------

package workflows

import (
	"testing"

	"go.temporal.io/sdk/testsuite"
	"github.com/stretchr/testify/mock"

	"munchbox/temporal-workers/backup/activities"
)

// TestBackup_Success verifies the happy path: all snapshots succeed, all
// S3 uploads succeed, cleanup runs.
func TestBackup_Success(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.OnActivity(a.TakeNomadSnapshot, mock.Anything).Return("/nomad.snap", nil)
	env.OnActivity(a.UploadToS3, mock.Anything, "/nomad.snap", "backups/nomad").Return("backups/nomad/nomad.snap", nil)
	env.OnActivity(a.TakeConsulSnapshot, mock.Anything).Return("/consul.snap", nil)
	env.OnActivity(a.UploadToS3, mock.Anything, "/consul.snap", "backups/consul").Return("backups/consul/consul.snap", nil)
	env.OnActivity(a.TakePostgresBackup, mock.Anything).Return("/postgres.sql.gz", nil)
	env.OnActivity(a.UploadToS3, mock.Anything, "/postgres.sql.gz", "backups/postgres").Return("backups/postgres/postgres.sql.gz", nil)
	env.OnActivity(a.CleanupOldBackups, mock.Anything, 7).Return(nil)
	env.OnActivity(a.CleanupOldS3Backups, mock.Anything, 30).Return(nil)

	retention := activities.RetentionConfig{LocalDays: 7, S3Days: 30}
	env.ExecuteWorkflow(Backup, retention)

	if !env.IsWorkflowCompleted() {
		t.Fatal("Workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("Workflow failed: %v", err)
	}

	var result activities.BackupResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("Failed to get result: %v", err)
	}
	if !result.Success {
		t.Error("Expected Success=true")
	}
	if result.NomadSnapshot != "/nomad.snap" {
		t.Errorf("NomadSnapshot = %q", result.NomadSnapshot)
	}
	if result.NomadS3Key != "backups/nomad/nomad.snap" {
		t.Errorf("NomadS3Key = %q", result.NomadS3Key)
	}
}

// TestBackup_RetentionDefaults verifies that zero-value retention config
// gets populated with defaults (7 local, 30 S3).
func TestBackup_RetentionDefaults(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.OnActivity(a.TakeNomadSnapshot, mock.Anything).Return("/nomad.snap", nil)
	env.OnActivity(a.UploadToS3, mock.Anything, "/nomad.snap", "backups/nomad").Return("", nil)
	env.OnActivity(a.TakeConsulSnapshot, mock.Anything).Return("/consul.snap", nil)
	env.OnActivity(a.UploadToS3, mock.Anything, "/consul.snap", "backups/consul").Return("", nil)
	env.OnActivity(a.TakePostgresBackup, mock.Anything).Return("/pg.sql.gz", nil)
	env.OnActivity(a.UploadToS3, mock.Anything, "/pg.sql.gz", "backups/postgres").Return("", nil)
	env.OnActivity(a.CleanupOldBackups, mock.Anything, 7).Return(nil)
	env.OnActivity(a.CleanupOldS3Backups, mock.Anything, 30).Return(nil)

	// Pass zero retention — workflow should apply defaults
	env.ExecuteWorkflow(Backup, activities.RetentionConfig{})

	if !env.IsWorkflowCompleted() {
		t.Fatal("Workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("Workflow failed: %v", err)
	}
}

// TestBackup_NomadFailure verifies the workflow terminates when the Nomad
// snapshot fails.
func TestBackup_NomadFailure(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.OnActivity(a.TakeNomadSnapshot, mock.Anything).
		Return("", testsuite.ErrMockStartChildWorkflowFailed)

	env.ExecuteWorkflow(Backup, activities.RetentionConfig{LocalDays: 7, S3Days: 30})

	if !env.IsWorkflowCompleted() {
		t.Fatal("Workflow did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("Expected workflow error when Nomad snapshot fails")
	}
}

// TestBackup_S3UploadFailureContinues verifies that S3 upload failures are
// non-fatal — the workflow continues with remaining snapshots.
func TestBackup_S3UploadFailureContinues(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.OnActivity(a.TakeNomadSnapshot, mock.Anything).Return("/nomad.snap", nil)
	env.OnActivity(a.UploadToS3, mock.Anything, "/nomad.snap", "backups/nomad").
		Return("", testsuite.ErrMockStartChildWorkflowFailed)
	env.OnActivity(a.TakeConsulSnapshot, mock.Anything).Return("/consul.snap", nil)
	env.OnActivity(a.UploadToS3, mock.Anything, "/consul.snap", "backups/consul").Return("key", nil)
	env.OnActivity(a.TakePostgresBackup, mock.Anything).Return("/pg.sql.gz", nil)
	env.OnActivity(a.UploadToS3, mock.Anything, "/pg.sql.gz", "backups/postgres").Return("key", nil)
	env.OnActivity(a.CleanupOldBackups, mock.Anything, 7).Return(nil)
	env.OnActivity(a.CleanupOldS3Backups, mock.Anything, 30).Return(nil)

	env.ExecuteWorkflow(Backup, activities.RetentionConfig{LocalDays: 7, S3Days: 30})

	if !env.IsWorkflowCompleted() {
		t.Fatal("Workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("Workflow should succeed despite S3 failure: %v", err)
	}

	var result activities.BackupResult
	_ = env.GetWorkflowResult(&result)
	if result.NomadS3Key != "" {
		t.Errorf("Expected empty NomadS3Key after upload failure, got %q", result.NomadS3Key)
	}
	if !result.Success {
		t.Error("Expected Success=true despite S3 failure")
	}
}

// TestBackup_PostgresFailure verifies the workflow terminates when the
// PostgreSQL backup fails.
func TestBackup_PostgresFailure(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.OnActivity(a.TakeNomadSnapshot, mock.Anything).Return("/nomad.snap", nil)
	env.OnActivity(a.UploadToS3, mock.Anything, "/nomad.snap", "backups/nomad").Return("", nil)
	env.OnActivity(a.TakeConsulSnapshot, mock.Anything).Return("/consul.snap", nil)
	env.OnActivity(a.UploadToS3, mock.Anything, "/consul.snap", "backups/consul").Return("", nil)
	env.OnActivity(a.TakePostgresBackup, mock.Anything).
		Return("", testsuite.ErrMockStartChildWorkflowFailed)

	env.ExecuteWorkflow(Backup, activities.RetentionConfig{LocalDays: 7, S3Days: 30})

	if !env.IsWorkflowCompleted() {
		t.Fatal("Workflow did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("Expected workflow error when PostgreSQL backup fails")
	}
}
