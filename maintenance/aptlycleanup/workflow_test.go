// -------------------------------------------------------------------------------
// Aptly Cleanup Workflow - Saga Pattern Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Tests the saga shape with mocked activities: the happy path (scale down ->
// cleanup -> scale back, with before/after sizing) and a cleanup failure that
// must still scale aptly back to 1 so it isn't stranded at count=0.
// -------------------------------------------------------------------------------

package aptlycleanup

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	"munchbox/temporal-workers/maintenance/internal/nodes"
)

// TestAptlyCleanup_HappyPath verifies the full saga runs and reports reclaimed
// space.
func TestAptlyCleanup_HappyPath(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	exp := AptlyCleanupConfig{}
	exp.ApplyDefaults()
	node := nodes.NodeInfo{ID: "n1", Name: "goren", Address: "192.168.68.60"}

	env.OnActivity(saga.FindJobNode, mock.Anything, exp.JobName).Return(node, nil)
	// Image is resolved from the deployed aptly job (config.Image left empty).
	env.OnActivity(saga.ResolveJobImage, mock.Anything, exp.JobName).Return("urpylka/aptly:1.6.3", nil)
	env.OnActivity(saga.MeasureDataDir, mock.Anything, node, exp.DataDir).
		Return(int64(2*1024*1024*1024), nil).Once() // 2 GiB before
	env.OnActivity(saga.ScaleJob, mock.Anything, exp.JobName, exp.GroupName, 0).Return(nil)
	env.OnActivity(saga.WaitJobDrained, mock.Anything, exp.JobName).Return(nil)
	env.OnActivity(acts.RunAptlyDBCleanup, mock.Anything, node, "urpylka/aptly:1.6.3", exp.DataDir).Return("cleaned 12 files", nil)
	env.OnActivity(saga.MeasureDataDir, mock.Anything, node, exp.DataDir).
		Return(int64(1*1024*1024*1024), nil).Once() // 1 GiB after
	// Deferred scale-back always fires.
	env.OnActivity(saga.ScaleJob, mock.Anything, exp.JobName, exp.GroupName, 1).Return(nil)
	env.OnActivity(saga.WaitJobRunning, mock.Anything, exp.JobName).Return(nil)

	env.ExecuteWorkflow(AptlyCleanup, AptlyCleanupConfig{})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	var got AptlyCleanupResult
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

// TestAptlyCleanup_ExplicitImageOverride verifies that when config.Image is set
// the workflow uses it verbatim and never calls ResolveJobImage.
func TestAptlyCleanup_ExplicitImageOverride(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	const override = "octo/aptly:pinned"
	exp := AptlyCleanupConfig{Image: override}
	exp.ApplyDefaults()
	node := nodes.NodeInfo{ID: "n1", Name: "goren", Address: "192.168.68.60"}

	env.OnActivity(saga.FindJobNode, mock.Anything, exp.JobName).Return(node, nil)
	env.OnActivity(saga.MeasureDataDir, mock.Anything, node, exp.DataDir).
		Return(int64(2*1024*1024*1024), nil).Once()
	env.OnActivity(saga.ScaleJob, mock.Anything, exp.JobName, exp.GroupName, 0).Return(nil)
	env.OnActivity(saga.WaitJobDrained, mock.Anything, exp.JobName).Return(nil)
	// The pinned override is what reaches the cleanup, not a resolved tag.
	env.OnActivity(acts.RunAptlyDBCleanup, mock.Anything, node, override, exp.DataDir).Return("cleaned", nil)
	env.OnActivity(saga.MeasureDataDir, mock.Anything, node, exp.DataDir).
		Return(int64(1*1024*1024*1024), nil).Once()
	env.OnActivity(saga.ScaleJob, mock.Anything, exp.JobName, exp.GroupName, 1).Return(nil)
	env.OnActivity(saga.WaitJobRunning, mock.Anything, exp.JobName).Return(nil)

	env.ExecuteWorkflow(AptlyCleanup, AptlyCleanupConfig{Image: override})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	// The override must short-circuit resolution entirely.
	env.AssertNotCalled(t, "ResolveJobImage", mock.Anything, mock.Anything)
}

// TestAptlyCleanup_ResolveImageFailure verifies that a failure resolving the
// deployed image aborts before the scale-down, so aptly is never touched (no
// compensation scale-back is needed because it was never scaled to 0).
func TestAptlyCleanup_ResolveImageFailure(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	exp := AptlyCleanupConfig{}
	exp.ApplyDefaults()
	node := nodes.NodeInfo{ID: "n1", Name: "goren", Address: "192.168.68.60"}

	env.OnActivity(saga.FindJobNode, mock.Anything, exp.JobName).Return(node, nil)
	env.OnActivity(saga.ResolveJobImage, mock.Anything, exp.JobName).
		Return("", errors.New("job not found"))

	scaleCalled := false
	env.OnActivity(saga.ScaleJob, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { scaleCalled = true }).Return(nil)

	env.ExecuteWorkflow(AptlyCleanup, AptlyCleanupConfig{})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if env.GetWorkflowError() == nil {
		t.Fatal("expected workflow error when image resolution fails, got nil")
	}
	if scaleCalled {
		t.Fatal("aptly was scaled before the image resolved — cleanup must abort first")
	}
}

// TestAptlyCleanup_CleanupFailureStillScalesBack verifies the deferred
// scale-back runs even when the cleanup activity fails.
func TestAptlyCleanup_CleanupFailureStillScalesBack(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	exp := AptlyCleanupConfig{}
	exp.ApplyDefaults()
	node := nodes.NodeInfo{ID: "n1", Name: "goren", Address: "192.168.68.60"}

	env.OnActivity(saga.FindJobNode, mock.Anything, exp.JobName).Return(node, nil)
	env.OnActivity(saga.ResolveJobImage, mock.Anything, exp.JobName).Return("urpylka/aptly:1.6.3", nil)
	env.OnActivity(saga.MeasureDataDir, mock.Anything, node, exp.DataDir).
		Return(int64(2*1024*1024*1024), nil).Once()
	env.OnActivity(saga.ScaleJob, mock.Anything, exp.JobName, exp.GroupName, 0).Return(nil)
	env.OnActivity(saga.WaitJobDrained, mock.Anything, exp.JobName).Return(nil)
	env.OnActivity(acts.RunAptlyDBCleanup, mock.Anything, node, "urpylka/aptly:1.6.3", exp.DataDir).
		Return("", errors.New("docker run failed: exit 1"))

	scaleBackCalled := false
	env.OnActivity(saga.ScaleJob, mock.Anything, exp.JobName, exp.GroupName, 1).
		Run(func(mock.Arguments) { scaleBackCalled = true }).
		Return(nil)
	env.OnActivity(saga.WaitJobRunning, mock.Anything, exp.JobName).Return(nil)

	env.ExecuteWorkflow(AptlyCleanup, AptlyCleanupConfig{})

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
