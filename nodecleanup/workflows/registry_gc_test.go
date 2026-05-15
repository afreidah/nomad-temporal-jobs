// -------------------------------------------------------------------------------
// Registry GC Workflow - Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Tests workflow orchestration using the Temporal test suite. The
// RegistryGarbageCollect activity is mocked.
// -------------------------------------------------------------------------------

package workflows

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	"munchbox/temporal-workers/nodecleanup/activities"
)

// TestRegistryGC_Success verifies the workflow returns the activity's
// result on the happy path.
func TestRegistryGC_Success(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	config := activities.RegistryGCConfig{
		JobName:         "registry",
		RegistryDataDir: "/mnt/gdrive/munchbox-data/registry",
		RegistryImage:   "registry:3",
		DryRun:          false,
		DeleteUntagged:  true,
	}

	expected := activities.RegistryGCResult{
		NodeName:       "stabler",
		NodeAddr:       "192.168.68.61",
		BlobsDeleted:   42,
		BytesReclaimed: "3.0GiB",
		BeforeBytes:    "10G",
		AfterBytes:     "7.0G",
		DryRun:         false,
	}

	env.OnActivity(a.RegistryGarbageCollect, mock.Anything, config).Return(expected, nil)

	env.ExecuteWorkflow(RegistryGC, config)

	if !env.IsWorkflowCompleted() {
		t.Fatal("Workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("Workflow failed: %v", err)
	}

	var got activities.RegistryGCResult
	if err := env.GetWorkflowResult(&got); err != nil {
		t.Fatalf("Failed to get result: %v", err)
	}
	if got.BlobsDeleted != expected.BlobsDeleted {
		t.Errorf("BlobsDeleted = %d, want %d", got.BlobsDeleted, expected.BlobsDeleted)
	}
	if got.BytesReclaimed != expected.BytesReclaimed {
		t.Errorf("BytesReclaimed = %q, want %q", got.BytesReclaimed, expected.BytesReclaimed)
	}
}

// TestRegistryGC_ActivityFailure verifies the workflow surfaces the
// underlying activity error.
func TestRegistryGC_ActivityFailure(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	config := activities.RegistryGCConfig{JobName: "registry"}

	env.OnActivity(a.RegistryGarbageCollect, mock.Anything, mock.Anything).
		Return(activities.RegistryGCResult{}, errors.New("registry didn't come back up"))

	env.ExecuteWorkflow(RegistryGC, config)

	if !env.IsWorkflowCompleted() {
		t.Fatal("Workflow did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("Expected workflow error, got nil")
	}
}
