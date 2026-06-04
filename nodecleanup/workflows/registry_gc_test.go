// -------------------------------------------------------------------------------
// Registry GC Workflow - Saga Pattern Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Tests the saga shape of the workflow with mocked activities. Three cases:
//   - Happy path: all 6 activities + deferred scale-back run in order.
//   - GC failure: GC activity errors; the deferred scale-back STILL runs so
//     the registry isn't stranded at count=0.
//   - Scale-down failure: nothing else runs (no compensation needed because
//     the registry was never actually scaled down).
// -------------------------------------------------------------------------------

package workflows

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	"munchbox/temporal-workers/nodecleanup/activities"
)

// -------------------------------------------------------------------------
// HAPPY PATH
// -------------------------------------------------------------------------

func TestRegistryGC_HappyPath(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	cfgIn := activities.RegistryGCConfig{} // workflow applies defaults
	expandedCfg := activities.RegistryGCConfig{}
	expandedCfg.ApplyDefaults()

	node := activities.NodeInfo{
		ID: "node-1", Name: "stabler", Address: "192.168.68.61",
	}

	env.OnActivity(a.FindJobNode, mock.Anything, expandedCfg.JobName).Return(node, nil)
	env.OnActivity(a.MeasureDataDir, mock.Anything, node, expandedCfg.RegistryDataDir).
		Return(int64(10*1024*1024*1024), nil).Once() // 10 GiB before
	env.OnActivity(a.ScaleJob, mock.Anything, expandedCfg.JobName, expandedCfg.GroupName, 0).Return(nil)
	env.OnActivity(a.WaitJobDrained, mock.Anything, expandedCfg.JobName).Return(nil)
	env.OnActivity(a.RunRegistryGarbageCollect, mock.Anything, node, expandedCfg).
		Return(activities.RegistryGCRunResult{BlobsDeleted: 7}, nil)
	env.OnActivity(a.MeasureDataDir, mock.Anything, node, expandedCfg.RegistryDataDir).
		Return(int64(7*1024*1024*1024), nil).Once() // 7 GiB after
	// Deferred scale-back: ALWAYS fires (even on happy path).
	env.OnActivity(a.ScaleJob, mock.Anything, expandedCfg.JobName, expandedCfg.GroupName, 1).Return(nil)
	env.OnActivity(a.WaitJobRunning, mock.Anything, expandedCfg.JobName).Return(nil)

	env.ExecuteWorkflow(RegistryGC, cfgIn)

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}

	var got activities.RegistryGCResult
	if err := env.GetWorkflowResult(&got); err != nil {
		t.Fatalf("get result: %v", err)
	}
	if got.NodeName != "stabler" {
		t.Errorf("NodeName = %q, want stabler", got.NodeName)
	}
	if got.BlobsDeleted != 7 {
		t.Errorf("BlobsDeleted = %d, want 7", got.BlobsDeleted)
	}
	if got.BeforeBytes != "10.0GiB" {
		t.Errorf("BeforeBytes = %q, want 10.0GiB", got.BeforeBytes)
	}
	if got.AfterBytes != "7.0GiB" {
		t.Errorf("AfterBytes = %q, want 7.0GiB", got.AfterBytes)
	}
	if got.BytesReclaimed != "3.0GiB" {
		t.Errorf("BytesReclaimed = %q, want 3.0GiB", got.BytesReclaimed)
	}
}

// -------------------------------------------------------------------------
// GC FAILURE — DEFERRED SCALE-BACK MUST STILL RUN
// -------------------------------------------------------------------------

func TestRegistryGC_GCFailureStillScalesBack(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	cfgIn := activities.RegistryGCConfig{}
	expandedCfg := activities.RegistryGCConfig{}
	expandedCfg.ApplyDefaults()
	node := activities.NodeInfo{ID: "node-1", Name: "stabler", Address: "192.168.68.61"}

	env.OnActivity(a.FindJobNode, mock.Anything, expandedCfg.JobName).Return(node, nil)
	env.OnActivity(a.MeasureDataDir, mock.Anything, node, expandedCfg.RegistryDataDir).
		Return(int64(10*1024*1024*1024), nil).Once()
	env.OnActivity(a.ScaleJob, mock.Anything, expandedCfg.JobName, expandedCfg.GroupName, 0).Return(nil)
	env.OnActivity(a.WaitJobDrained, mock.Anything, expandedCfg.JobName).Return(nil)
	// GC fails — workflow should error out, BUT compensation still runs.
	env.OnActivity(a.RunRegistryGarbageCollect, mock.Anything, node, expandedCfg).
		Return(activities.RegistryGCRunResult{}, errors.New("docker run failed: exit 1"))
	// Compensation MUST fire:
	scaleBackCalled := false
	env.OnActivity(a.ScaleJob, mock.Anything, expandedCfg.JobName, expandedCfg.GroupName, 1).
		Run(func(args mock.Arguments) { scaleBackCalled = true }).
		Return(nil)
	env.OnActivity(a.WaitJobRunning, mock.Anything, expandedCfg.JobName).Return(nil)

	env.ExecuteWorkflow(RegistryGC, cfgIn)

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if env.GetWorkflowError() == nil {
		t.Fatal("expected workflow error from GC failure, got nil")
	}
	if !scaleBackCalled {
		t.Fatal("compensation scale-back to 1 was NOT called — registry would be stranded at count=0")
	}
}

// -------------------------------------------------------------------------
// SCALE-DOWN FAILURE — NO COMPENSATION NEEDED
// -------------------------------------------------------------------------

func TestRegistryGC_ScaleDownFailureSkipsCompensation(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	cfgIn := activities.RegistryGCConfig{}
	expandedCfg := activities.RegistryGCConfig{}
	expandedCfg.ApplyDefaults()
	node := activities.NodeInfo{ID: "node-1", Name: "stabler", Address: "192.168.68.61"}

	env.OnActivity(a.FindJobNode, mock.Anything, expandedCfg.JobName).Return(node, nil)
	env.OnActivity(a.MeasureDataDir, mock.Anything, node, expandedCfg.RegistryDataDir).
		Return(int64(10*1024*1024*1024), nil).Once()
	// Scale-down fails before compensation can be registered.
	env.OnActivity(a.ScaleJob, mock.Anything, expandedCfg.JobName, expandedCfg.GroupName, 0).
		Return(errors.New("nomad api 500"))

	scaleBackCalled := false
	env.OnActivity(a.ScaleJob, mock.Anything, expandedCfg.JobName, expandedCfg.GroupName, 1).
		Run(func(args mock.Arguments) { scaleBackCalled = true }).
		Return(nil).Maybe() // Maybe — assert it does NOT run below.

	env.ExecuteWorkflow(RegistryGC, cfgIn)

	if env.GetWorkflowError() == nil {
		t.Fatal("expected workflow error from scale-down failure")
	}
	if scaleBackCalled {
		t.Fatal("scale-back to 1 should NOT be called when scale-down itself failed (registry was never scaled down)")
	}
}
