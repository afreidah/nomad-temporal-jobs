// -------------------------------------------------------------------------------
// GitHub Token Renewer Workflow - Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Drives RenewTokens in the Temporal test environment with mocked activities:
// the happy path renews every repo, and a per-repo failure is tolerated (the run
// continues) but surfaces as a workflow error.
// -------------------------------------------------------------------------------

package workflows

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	"munchbox/temporal-workers/ghtokenrenewer/activities"
)

func TestRenewTokens_HappyPath(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.OnActivity(a.ListRepos, mock.Anything).Return([]string{"o/a", "o/b"}, nil)
	env.OnActivity(a.RenewRepoToken, mock.Anything, mock.Anything).Return(
		func(_ context.Context, repo string) (activities.RepoRenewResult, error) {
			return activities.RepoRenewResult{Repo: repo}, nil
		})

	env.ExecuteWorkflow(RenewTokens, RenewConfig{})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result RenewResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !result.Success || len(result.Renewed) != 2 || len(result.Failed) != 0 {
		t.Errorf("result = %+v, want success with 2 renewed and none failed", result)
	}
}

func TestRenewTokens_PartialFailureStillContinues(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.OnActivity(a.ListRepos, mock.Anything).Return([]string{"o/good", "o/bad"}, nil)

	var renewed int
	env.OnActivity(a.RenewRepoToken, mock.Anything, mock.Anything).Return(
		func(_ context.Context, repo string) (activities.RepoRenewResult, error) {
			if repo == "o/bad" {
				return activities.RepoRenewResult{}, errors.New("forbidden")
			}
			renewed++
			return activities.RepoRenewResult{Repo: repo}, nil
		})

	env.ExecuteWorkflow(RenewTokens, RenewConfig{})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	// One repo failed -> the workflow reports an error, but the good repo was
	// still renewed (the run didn't abort on the first failure).
	if env.GetWorkflowError() == nil {
		t.Fatal("expected a workflow error when a repo fails")
	}
	if renewed != 1 {
		t.Errorf("renewed %d repos, want 1 (the healthy one still ran)", renewed)
	}
}
