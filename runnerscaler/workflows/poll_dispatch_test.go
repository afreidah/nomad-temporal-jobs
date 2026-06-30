// -------------------------------------------------------------------------------
// Runner Scaler Workflows - Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Drives the parent and child workflows in the Temporal test environment with
// mocked activities: the parent starts one runner child per queued job and a
// repo that errors is skipped without aborting the tick; the child dispatches a
// runner and reaps it once the backstop timer fires. profileLabel's
// label->profile mapping is covered directly.
// -------------------------------------------------------------------------------

package workflows

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	"munchbox/temporal-workers/runnerscaler/activities"
	"munchbox/temporal-workers/shared/client/git"
)

func TestPollAndDispatch_StartsChildPerJob(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	env.OnActivity(a.ListWatchedRepos, mock.Anything).Return([]string{"octo/a", "octo/b"}, nil)
	env.OnActivity(a.LoadProfiles, mock.Anything).Return(map[string]activities.Profile{
		"default": {Image: "reg/ci:latest"},
	}, nil)
	env.OnActivity(a.ListQueuedJobs, mock.Anything, "octo/a").Return(
		[]git.QueuedJob{{ID: 1, Labels: []string{"self-hosted"}}}, nil)
	env.OnActivity(a.ListQueuedJobs, mock.Anything, "octo/b").Return(
		[]git.QueuedJob{{ID: 2, Labels: []string{"self-hosted"}}, {ID: 3, Labels: []string{"self-hosted"}}}, nil)

	// Stub the child so the parent only exercises its start path.
	env.RegisterWorkflow(HandleQueuedJob)
	env.OnWorkflow(HandleQueuedJob, mock.Anything, mock.Anything).Return(nil)

	env.ExecuteWorkflow(PollAndDispatch, PollConfig{})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result PollResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.ReposScanned != 2 || result.RunnersStarted != 3 {
		t.Errorf("result = %+v, want 2 repos scanned / 3 runners started", result)
	}
}

func TestPollAndDispatch_RepoErrorIsSkipped(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	env.OnActivity(a.ListWatchedRepos, mock.Anything).Return([]string{"octo/good", "octo/bad"}, nil)
	env.OnActivity(a.LoadProfiles, mock.Anything).Return(map[string]activities.Profile{}, nil)
	env.OnActivity(a.ListQueuedJobs, mock.Anything, "octo/good").Return(
		[]git.QueuedJob{{ID: 1, Labels: []string{"self-hosted"}}}, nil)
	env.OnActivity(a.ListQueuedJobs, mock.Anything, "octo/bad").Return(
		nil, errors.New("github 500"))

	env.RegisterWorkflow(HandleQueuedJob)
	env.OnWorkflow(HandleQueuedJob, mock.Anything, mock.Anything).Return(nil)

	env.ExecuteWorkflow(PollAndDispatch, PollConfig{})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	// A repo that fails its listing is skipped; the healthy repo still dispatches.
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("a failing repo should not abort the tick: %v", err)
	}
	var result PollResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.RunnersStarted != 1 {
		t.Errorf("started = %d, want 1 (only the healthy repo)", result.RunnersStarted)
	}
}

func TestHandleQueuedJob_DispatchThenReap(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	var reaped string
	env.OnActivity(a.DispatchRunner, mock.Anything, mock.Anything).Return("ci-runner/dispatch-1-abc", nil)
	env.OnActivity(a.ReapRunner, mock.Anything, mock.Anything).Return(
		func(_ context.Context, id string) error { reaped = id; return nil })

	env.ExecuteWorkflow(HandleQueuedJob, JobSpec{
		Repo:   "octo/widget",
		JobID:  7,
		Labels: []string{"self-hosted"},
	})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The test env auto-fires the backstop timer, so the reap runs against the
	// dispatched job ID.
	if reaped != "ci-runner/dispatch-1-abc" {
		t.Errorf("reaped %q, want the dispatched job id", reaped)
	}
}

func TestProfileLabel(t *testing.T) {
	cases := []struct {
		labels []string
		want   string
	}{
		{[]string{"self-hosted"}, "default"},
		{[]string{"self-hosted", "amd64"}, "amd64"},
		{[]string{"arm64", "self-hosted"}, "arm64"},
		{nil, "default"},
	}
	for _, c := range cases {
		if got := profileLabel(c.labels); got != c.want {
			t.Errorf("profileLabel(%v) = %q, want %q", c.labels, got, c.want)
		}
	}
}
