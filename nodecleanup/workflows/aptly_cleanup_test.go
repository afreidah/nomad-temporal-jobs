// -------------------------------------------------------------------------------
// Aptly Cleanup Workflow - Saga Pattern Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Tests the saga shape with mocked activities: the happy path (scale down ->
// cleanup -> scale back, with before/after sizing) and a cleanup failure that
// must still scale aptly back to 1 so it isn't stranded at count=0.
// -------------------------------------------------------------------------------

package workflows

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	"munchbox/temporal-workers/nodecleanup/activities"
)

// TestAptlyCleanup_HappyPath verifies the full saga runs and reports reclaimed
// space.
func TestAptlyCleanup_HappyPath(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	exp := activities.AptlyCleanupConfig{}
	exp.ApplyDefaults()
	node := activities.NodeInfo{ID: "n1", Name: "goren", Address: "192.168.68.60"}

	env.OnActivity(a.FindJobNode, mock.Anything, exp.JobName).Return(node, nil)
	env.OnActivity(a.MeasureDataDir, mock.Anything, node, exp.DataDir).
		Return(int64(2*1024*1024*1024), nil).Once() // 2 GiB before
	env.OnActivity(a.ScaleJob, mock.Anything, exp.JobName, exp.GroupName, 0).Return(nil)
	env.OnActivity(a.WaitJobDrained, mock.Anything, exp.JobName).Return(nil)
	env.OnActivity(a.RunAptlyDBCleanup, mock.Anything, node, exp.Image, exp.DataDir).Return("cleaned 12 files", nil)
	env.OnActivity(a.MeasureDataDir, mock.Anything, node, exp.DataDir).
		Return(int64(1*1024*1024*1024), nil).Once() // 1 GiB after
	// Deferred scale-back always fires.
	env.OnActivity(a.ScaleJob, mock.Anything, exp.JobName, exp.GroupName, 1).Return(nil)
	env.OnActivity(a.WaitJobRunning, mock.Anything, exp.JobName).Return(nil)

	env.ExecuteWorkflow(AptlyCleanup, activities.AptlyCleanupConfig{})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	var got activities.AptlyCleanupResult
	if err := env.GetWorkflowResult(&got); err != nil {
		t.Fatalf("get result: %v", err)
	}
	if got.Node != "goren" {
		t.Errorf("Node = %q, want goren", got.Node)
	}
	if got.BytesReclaimed != "1.0GiB" {
		t.Errorf("BytesReclaimed = %q, want 1.0GiB", got.BytesReclaimed)
	}
}

// TestAptlyCleanup_CleanupFailureStillScalesBack verifies the deferred
// scale-back runs even when the cleanup activity fails.
func TestAptlyCleanup_CleanupFailureStillScalesBack(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	exp := activities.AptlyCleanupConfig{}
	exp.ApplyDefaults()
	node := activities.NodeInfo{ID: "n1", Name: "goren", Address: "192.168.68.60"}

	env.OnActivity(a.FindJobNode, mock.Anything, exp.JobName).Return(node, nil)
	env.OnActivity(a.MeasureDataDir, mock.Anything, node, exp.DataDir).
		Return(int64(2*1024*1024*1024), nil).Once()
	env.OnActivity(a.ScaleJob, mock.Anything, exp.JobName, exp.GroupName, 0).Return(nil)
	env.OnActivity(a.WaitJobDrained, mock.Anything, exp.JobName).Return(nil)
	env.OnActivity(a.RunAptlyDBCleanup, mock.Anything, node, exp.Image, exp.DataDir).
		Return("", errors.New("docker run failed: exit 1"))

	scaleBackCalled := false
	env.OnActivity(a.ScaleJob, mock.Anything, exp.JobName, exp.GroupName, 1).
		Run(func(mock.Arguments) { scaleBackCalled = true }).
		Return(nil)
	env.OnActivity(a.WaitJobRunning, mock.Anything, exp.JobName).Return(nil)

	env.ExecuteWorkflow(AptlyCleanup, activities.AptlyCleanupConfig{})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if env.GetWorkflowError() == nil {
		t.Fatal("expected workflow error from cleanup failure, got nil")
	}
	if !scaleBackCalled {
		t.Fatal("compensation scale-back to 1 was NOT called — aptly would be stranded at count=0")
	}
}
