// -------------------------------------------------------------------------------
// Cert Acquirer Workflow - Unit Tests
//
// Author: Alex Freidah
//
// Verifies the issue-then-publish ordering with mocked activities, and the
// load-bearing property of the split design: a publish failure surfaces as a
// workflow error but never re-runs ACME issuance.
// -------------------------------------------------------------------------------

package workflows

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	"munchbox/temporal-workers/certacquirer/activities"
)

func TestCertAcquirer_IssueThenPublish(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	req := activities.IssueRequest{Domains: []string{"*.munchbox.cc"}, Email: "ops@munchbox.cc"}
	env.OnActivity(a.IssueWildcardCert, mock.Anything, req).Return(nil).Once()
	env.OnActivity(a.PublishWildcardCert, mock.Anything).Return(nil).Once()

	env.ExecuteWorkflow(CertAcquirer, req)

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	env.AssertExpectations(t)
}

func TestCertAcquirer_PublishFailureDoesNotReRunIssue(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	req := activities.IssueRequest{Domains: []string{"*.munchbox.cc"}, Email: "ops@munchbox.cc"}
	// Issue runs exactly once; publish keeps failing through its retries.
	env.OnActivity(a.IssueWildcardCert, mock.Anything, req).Return(nil).Once()
	env.OnActivity(a.PublishWildcardCert, mock.Anything).Return(errors.New("vault write failed"))

	env.ExecuteWorkflow(CertAcquirer, req)

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if env.GetWorkflowError() == nil {
		t.Fatal("expected the workflow to fail when publish fails")
	}
	// The .Once() expectation on IssueWildcardCert proves it was not re-run.
	env.AssertExpectations(t)
}
