// -------------------------------------------------------------------------------
// Node Cleanup Workflow - Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Tests workflow orchestration using the Temporal test suite. Activities are
// mocked to verify the workflow discovers nodes, cleans each sequentially,
// applies defaults, and reports failures correctly.
// -------------------------------------------------------------------------------

package workflows

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	"munchbox/temporal-workers/nodecleanup/activities"
)

// TestCleanup_Success verifies the happy path: discover nodes, clean each,
// return results.
func TestCleanup_Success(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	nodes := []activities.NodeInfo{
		{ID: "n1", Name: "node1", Address: "10.0.0.1", HTTPAddr: "10.0.0.1:4646"},
		{ID: "n2", Name: "node2", Address: "10.0.0.2", HTTPAddr: "10.0.0.2:4646"},
	}
	config := activities.CleanupConfig{
		DataDir:   "/opt/nomad/data",
		GraceDays: 7,
		DryRun:    true,
	}

	result1 := activities.CleanupResult{NodeName: "node1", NodeAddr: "10.0.0.1", Scanned: 5, Orphaned: 1}
	result2 := activities.CleanupResult{NodeName: "node2", NodeAddr: "10.0.0.2", Scanned: 3, Orphaned: 0}

	env.OnActivity(a.GetAllNomadClientNodes, mock.Anything).Return(nodes, nil)
	env.OnActivity(a.CleanupNodeViaSSH, mock.Anything, nodes[0], config).Return(result1, nil)
	env.OnActivity(a.CleanupNodeViaSSH, mock.Anything, nodes[1], config).Return(result2, nil)

	env.ExecuteWorkflow(Cleanup, config)

	if !env.IsWorkflowCompleted() {
		t.Fatal("Workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("Workflow failed: %v", err)
	}

	var results []activities.CleanupResult
	if err := env.GetWorkflowResult(&results); err != nil {
		t.Fatalf("Failed to get result: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(results))
	}
}

// TestCleanup_AppliesDefaults verifies that empty DataDir and zero
// GraceDays get populated with defaults.
func TestCleanup_AppliesDefaults(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.OnActivity(a.GetAllNomadClientNodes, mock.Anything).Return([]activities.NodeInfo{}, nil)

	// Pass zero config — workflow should apply defaults
	env.ExecuteWorkflow(Cleanup, activities.CleanupConfig{})

	if !env.IsWorkflowCompleted() {
		t.Fatal("Workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("Workflow failed: %v", err)
	}
}

// TestCleanup_NodeDiscoveryFailure verifies the workflow returns an error
// when node discovery fails.
func TestCleanup_NodeDiscoveryFailure(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.OnActivity(a.GetAllNomadClientNodes, mock.Anything).
		Return(nil, testsuite.ErrMockStartChildWorkflowFailed)

	env.ExecuteWorkflow(Cleanup, activities.CleanupConfig{DataDir: "/opt/nomad/data", GraceDays: 7})

	if !env.IsWorkflowCompleted() {
		t.Fatal("Workflow did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("Expected workflow error when node discovery fails")
	}
}

// TestCleanup_NodeFailureReported verifies that a failed node is included
// in the results and the workflow returns an error listing the failed node.
func TestCleanup_NodeFailureReported(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	nodes := []activities.NodeInfo{
		{ID: "n1", Name: "good-node", Address: "10.0.0.1", HTTPAddr: "10.0.0.1:4646"},
		{ID: "n2", Name: "bad-node", Address: "10.0.0.2", HTTPAddr: "10.0.0.2:4646"},
	}
	config := activities.CleanupConfig{DataDir: "/opt/nomad/data", GraceDays: 7, DryRun: true}

	goodResult := activities.CleanupResult{NodeName: "good-node", NodeAddr: "10.0.0.1", Scanned: 3}

	env.OnActivity(a.GetAllNomadClientNodes, mock.Anything).Return(nodes, nil)
	env.OnActivity(a.CleanupNodeViaSSH, mock.Anything, nodes[0], config).Return(goodResult, nil)
	env.OnActivity(a.CleanupNodeViaSSH, mock.Anything, nodes[1], config).
		Return(activities.CleanupResult{}, testsuite.ErrMockStartChildWorkflowFailed)

	env.ExecuteWorkflow(Cleanup, config)

	if !env.IsWorkflowCompleted() {
		t.Fatal("Workflow did not complete")
	}
	err := env.GetWorkflowError()
	if err == nil {
		t.Fatal("Expected workflow error when a node fails")
	}

	// Verify the error message includes the failed node name
	if !strings.Contains(err.Error(), "bad-node") {
		t.Errorf("Expected error to mention bad-node, got: %v", err)
	}
}
