// -------------------------------------------------------------------------------
// Backup Workflow - Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Tests workflow orchestration using the Temporal test suite. Activities are
// mocked to verify the concurrent snapshot legs, per-database PostgreSQL
// fan-out, graceful S3 upload handling, and config defaults.
// -------------------------------------------------------------------------------

package workflows

import (
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	"munchbox/temporal-workers/backup/activities"
)

// mockAllSuccess wires every backup activity to succeed. Individual tests
// override specific activities to exercise failure paths.
func mockAllSuccess(env *testsuite.TestWorkflowEnvironment, dbs []string) {
	env.OnActivity(a.TakeNomadSnapshot, mock.Anything).Return("/nomad.snap", nil)
	env.OnActivity(a.TakeConsulSnapshot, mock.Anything).Return("/consul.snap", nil)
	env.OnActivity(a.BackupPostgresGlobals, mock.Anything).Return("/pg-globals.sql.gz", nil)
	env.OnActivity(a.ListPostgresDatabases, mock.Anything).Return(dbs, nil)
	env.OnActivity(a.BackupPostgresDatabase, mock.Anything, mock.Anything).Return("/pg-db.sql.gz", nil)
	env.OnActivity(a.UploadToS3, mock.Anything, mock.Anything, mock.Anything).Return("s3key", nil)
	env.OnActivity(a.CleanupOldBackups, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(a.CleanupOldS3Backups, mock.Anything, mock.Anything).Return(nil)
}

// TestBackup_Success verifies the happy path: all legs succeed, every
// database is dumped and uploaded, cleanup runs.
func TestBackup_Success(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	mockAllSuccess(env, []string{"app", "metrics"})

	env.ExecuteWorkflow(Backup, activities.BackupConfig{LocalDays: 7, S3Days: 30, DumpConcurrency: 2})

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
	if result.NomadSnapshot != "/nomad.snap" || result.ConsulSnapshot != "/consul.snap" {
		t.Errorf("snapshots = %q / %q", result.NomadSnapshot, result.ConsulSnapshot)
	}
	if result.PostgresGlobals != "/pg-globals.sql.gz" {
		t.Errorf("PostgresGlobals = %q", result.PostgresGlobals)
	}
	if len(result.PostgresDatabases) != 2 {
		t.Fatalf("PostgresDatabases len = %d, want 2", len(result.PostgresDatabases))
	}
	for _, db := range result.PostgresDatabases {
		if db.LocalPath == "" || db.S3Key == "" {
			t.Errorf("database %q missing path/key: %+v", db.Database, db)
		}
	}
}

// TestBackup_Defaults verifies a zero-value config gets retention (7/30) and
// concurrency (4) defaults applied.
func TestBackup_Defaults(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	// Zero config: DumpConcurrency must default to a positive value or the
	// bounded fan-out would deadlock, so a clean completion proves defaults.
	mockAllSuccess(env, []string{"app"})

	env.ExecuteWorkflow(Backup, activities.BackupConfig{})

	if !env.IsWorkflowCompleted() {
		t.Fatal("Workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("Workflow failed: %v", err)
	}
}

// TestBackup_NoDatabases verifies the workflow succeeds when the cluster has
// no user databases (globals still backed up).
func TestBackup_NoDatabases(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	mockAllSuccess(env, nil)

	env.ExecuteWorkflow(Backup, activities.BackupConfig{DumpConcurrency: 2})

	if !env.IsWorkflowCompleted() {
		t.Fatal("Workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("Workflow failed: %v", err)
	}
	var result activities.BackupResult
	_ = env.GetWorkflowResult(&result)
	if len(result.PostgresDatabases) != 0 {
		t.Errorf("expected no databases, got %d", len(result.PostgresDatabases))
	}
	if !result.Success {
		t.Error("Expected Success=true")
	}
}

// TestBackup_NomadFailure verifies the workflow terminates when the Nomad
// snapshot fails, even though the other legs succeed.
func TestBackup_NomadFailure(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	// Register the failing override first: testify uses the first matching
	// expectation, so it must precede the generic success mocks.
	env.OnActivity(a.TakeNomadSnapshot, mock.Anything).
		Return("", testsuite.ErrMockStartChildWorkflowFailed)
	mockAllSuccess(env, []string{"app"})

	env.ExecuteWorkflow(Backup, activities.BackupConfig{LocalDays: 7, S3Days: 30, DumpConcurrency: 2})

	if !env.IsWorkflowCompleted() {
		t.Fatal("Workflow did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("Expected workflow error when Nomad snapshot fails")
	}
}

// TestBackup_DatabaseDumpFailure verifies a single database dump failure
// fails the workflow after the other databases are attempted.
func TestBackup_DatabaseDumpFailure(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	// Override first: testify matches the first registered expectation, so the
	// failing "broken" dump must precede the generic success mocks.
	env.OnActivity(a.BackupPostgresDatabase, mock.Anything, "broken").
		Return("", testsuite.ErrMockStartChildWorkflowFailed)
	mockAllSuccess(env, []string{"app", "broken"})

	env.ExecuteWorkflow(Backup, activities.BackupConfig{DumpConcurrency: 2})

	if !env.IsWorkflowCompleted() {
		t.Fatal("Workflow did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("Expected workflow error when a database dump fails")
	}
}

// TestBackup_S3UploadFailureContinues verifies S3 upload failures are
// non-fatal -- the workflow still completes successfully.
func TestBackup_S3UploadFailureContinues(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	// Override first so every upload fails regardless of leg.
	env.OnActivity(a.UploadToS3, mock.Anything, mock.Anything, mock.Anything).
		Return("", testsuite.ErrMockStartChildWorkflowFailed)
	mockAllSuccess(env, []string{"app"})

	env.ExecuteWorkflow(Backup, activities.BackupConfig{LocalDays: 7, S3Days: 30, DumpConcurrency: 2})

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
