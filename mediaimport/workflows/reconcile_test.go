// -------------------------------------------------------------------------------
// Media Import Workflow - Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
// -------------------------------------------------------------------------------

package workflows

import (
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	"munchbox/temporal-workers/mediaimport/activities"
)

func isFolder(name string) any {
	return mock.MatchedBy(func(r activities.ImportRequest) bool { return r.Folder == name })
}

func TestReconcile_SonarrAndRadarrFallback(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.OnActivity(a.ListCompleted, mock.Anything).Return([]string{"Show S01", "Some Movie"}, nil)
	env.OnActivity(a.SonarrImport, mock.Anything, isFolder("Show S01")).
		Return(activities.ImportResult{Folder: "Show S01", App: "sonarr", Imported: 2}, nil)
	env.OnActivity(a.SonarrImport, mock.Anything, isFolder("Some Movie")).
		Return(activities.ImportResult{Folder: "Some Movie", App: "sonarr", NoMatch: true}, nil)
	env.OnActivity(a.RadarrImport, mock.Anything, isFolder("Some Movie")).
		Return(activities.ImportResult{Folder: "Some Movie", App: "radarr", Imported: 1}, nil)
	env.OnActivity(a.JellyfinRefresh, mock.Anything).Return(nil)

	env.ExecuteWorkflow(Reconcile, activities.ReconcileConfig{Concurrency: 2})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	env.AssertExpectations(t)
}

func TestReconcile_NothingImportedSkipsRefresh(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.OnActivity(a.ListCompleted, mock.Anything).Return([]string{}, nil)
	env.ExecuteWorkflow(Reconcile, activities.ReconcileConfig{})

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	// JellyfinRefresh must not be registered/called when nothing imported.
	env.AssertNotCalled(t, "JellyfinRefresh", mock.Anything)
}

func TestReconcile_DryRunSkipsRefresh(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.OnActivity(a.ListCompleted, mock.Anything).Return([]string{"Show S01"}, nil)
	env.OnActivity(a.SonarrImport, mock.Anything, mock.Anything).
		Return(activities.ImportResult{Folder: "Show S01", App: "sonarr", Imported: 3}, nil)

	env.ExecuteWorkflow(Reconcile, activities.ReconcileConfig{DryRun: true})

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	env.AssertNotCalled(t, "JellyfinRefresh", mock.Anything)
}
