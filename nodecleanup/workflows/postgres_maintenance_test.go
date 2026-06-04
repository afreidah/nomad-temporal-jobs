// -------------------------------------------------------------------------------
// Postgres Maintenance Workflow - Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Mocks the maintenance activities to verify the bounded-concurrency vacuum
// fan-out, per-database failure handling, config defaults, and the empty-db
// edge case, without a live PostgreSQL.
// -------------------------------------------------------------------------------

package workflows

import (
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	"munchbox/temporal-workers/nodecleanup/activities"
)

// TestPostgresMaintenance_Success verifies every database is vacuumed and the
// result reports success.
func TestPostgresMaintenance_Success(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.OnActivity(a.ListPostgresDatabases, mock.Anything).Return([]string{"app", "metrics"}, nil)
	env.OnActivity(a.VacuumAnalyzeDatabase, mock.Anything, mock.Anything).Return(nil)

	env.ExecuteWorkflow(PostgresMaintenance, activities.PostgresMaintenanceConfig{Concurrency: 2})

	if !env.IsWorkflowCompleted() {
		t.Fatal("Workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("Workflow failed: %v", err)
	}
	var result activities.PostgresMaintenanceResult
	_ = env.GetWorkflowResult(&result)
	if !result.Success {
		t.Error("Expected Success=true")
	}
	if len(result.Databases) != 2 {
		t.Fatalf("Databases len = %d, want 2", len(result.Databases))
	}
}

// TestPostgresMaintenance_Defaults verifies a zero-value config gets a positive
// concurrency default; a zero would size the semaphore at 0 and deadlock.
func TestPostgresMaintenance_Defaults(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.OnActivity(a.ListPostgresDatabases, mock.Anything).Return([]string{"app"}, nil)
	env.OnActivity(a.VacuumAnalyzeDatabase, mock.Anything, mock.Anything).Return(nil)

	env.ExecuteWorkflow(PostgresMaintenance, activities.PostgresMaintenanceConfig{})

	if !env.IsWorkflowCompleted() {
		t.Fatal("Workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("Workflow failed: %v", err)
	}
}

// TestPostgresMaintenance_NoDatabases verifies the workflow succeeds when the
// cluster reports no user databases.
func TestPostgresMaintenance_NoDatabases(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.OnActivity(a.ListPostgresDatabases, mock.Anything).Return([]string{}, nil)

	env.ExecuteWorkflow(PostgresMaintenance, activities.PostgresMaintenanceConfig{Concurrency: 2})

	if !env.IsWorkflowCompleted() {
		t.Fatal("Workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("Workflow failed: %v", err)
	}
}

// TestPostgresMaintenance_VacuumFailure verifies one database's VACUUM failure
// fails the workflow after the others are attempted.
func TestPostgresMaintenance_VacuumFailure(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	// Override first: testify matches the first registered expectation.
	env.OnActivity(a.VacuumAnalyzeDatabase, mock.Anything, "broken").
		Return(testsuite.ErrMockStartChildWorkflowFailed)
	env.OnActivity(a.ListPostgresDatabases, mock.Anything).Return([]string{"app", "broken"}, nil)
	env.OnActivity(a.VacuumAnalyzeDatabase, mock.Anything, mock.Anything).Return(nil)

	env.ExecuteWorkflow(PostgresMaintenance, activities.PostgresMaintenanceConfig{Concurrency: 2})

	if !env.IsWorkflowCompleted() {
		t.Fatal("Workflow did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("Expected workflow error when a VACUUM fails")
	}
}
