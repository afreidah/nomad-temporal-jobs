// -------------------------------------------------------------------------------
// Trivy Scan Workflow - Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Tests workflow orchestration using the Temporal test suite. Activities are
// mocked to verify the workflow calls them in the correct order with the
// correct arguments, without requiring Trivy, Nomad, or PostgreSQL.
// -------------------------------------------------------------------------------

package workflows

import (
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"
	"github.com/stretchr/testify/mock"

	"munchbox/temporal-workers/trivyscan/activities"
)

// TestScan_Success verifies the happy path: discover images, scan in batch,
// save results.
func TestScan_Success(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	images := []string{"nginx:latest", "redis:7"}
	scanResult := activities.ScanResult{
		Image:         "nginx:latest",
		Status:        "success",
		CriticalCount: 1,
		HighCount:     2,
		ScannedAt:     time.Now(),
	}
	scanResult2 := activities.ScanResult{
		Image:     "redis:7",
		Status:    "success",
		ScannedAt: time.Now(),
	}

	env.OnActivity(a.GetRunningImages, mock.Anything).Return(images, nil)
	env.OnActivity(a.ScanImage, mock.Anything, "nginx:latest").Return(scanResult, nil)
	env.OnActivity(a.ScanImage, mock.Anything, "redis:7").Return(scanResult2, nil)
	env.OnActivity(a.SaveScanResult, mock.Anything, scanResult).Return(nil)
	env.OnActivity(a.SaveScanResult, mock.Anything, scanResult2).Return(nil)

	env.ExecuteWorkflow(Scan)

	if !env.IsWorkflowCompleted() {
		t.Fatal("Workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("Workflow failed: %v", err)
	}
}

// TestScan_NoImages verifies the workflow completes successfully when no
// running images are found.
func TestScan_NoImages(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.OnActivity(a.GetRunningImages, mock.Anything).Return([]string{}, nil)

	env.ExecuteWorkflow(Scan)

	if !env.IsWorkflowCompleted() {
		t.Fatal("Workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("Workflow failed: %v", err)
	}
}

// TestScan_GetImagesFailure verifies the workflow returns an error when
// image discovery fails.
func TestScan_GetImagesFailure(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.OnActivity(a.GetRunningImages, mock.Anything).
		Return(nil, testsuite.ErrMockStartChildWorkflowFailed)

	env.ExecuteWorkflow(Scan)

	if !env.IsWorkflowCompleted() {
		t.Fatal("Workflow did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("Expected workflow error when GetRunningImages fails")
	}
}

// TestScan_ScanFailureContinues verifies that a scan failure for one image
// does not prevent other images from being processed and saved.
func TestScan_ScanFailureContinues(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	images := []string{"good:latest", "bad:latest"}
	goodResult := activities.ScanResult{
		Image:     "good:latest",
		Status:    "success",
		ScannedAt: time.Now(),
	}

	env.OnActivity(a.GetRunningImages, mock.Anything).Return(images, nil)
	env.OnActivity(a.ScanImage, mock.Anything, "good:latest").Return(goodResult, nil)
	env.OnActivity(a.ScanImage, mock.Anything, "bad:latest").
		Return(activities.ScanResult{}, testsuite.ErrMockStartChildWorkflowFailed)

	// Both results should be saved — the failed one with error status
	env.OnActivity(a.SaveScanResult, mock.Anything, goodResult).Return(nil)
	env.OnActivity(a.SaveScanResult, mock.Anything, mock.Anything).Return(nil)

	env.ExecuteWorkflow(Scan)

	if !env.IsWorkflowCompleted() {
		t.Fatal("Workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("Workflow should succeed even with scan failures: %v", err)
	}
}
