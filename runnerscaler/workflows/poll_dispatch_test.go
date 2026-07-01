// -------------------------------------------------------------------------------
// Runner Scaler Workflows - Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Drives the parent and child workflows in the Temporal test environment with
// mocked activities: the parent tops up runners to cover the queued-job depth
// per (repo, labels), dispatches only the shortfall when runners are already in
// flight, and skips a repo whose listing errors without aborting the tick; the
// child dispatches a runner and reaps it once the backstop timer fires.
// profileLabel's label->profile mapping is covered directly.
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

func TestPollAndDispatch_DispatchesShortfall(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	env.OnActivity(a.ListWatchedRepos, mock.Anything).Return([]string{"octo/a", "octo/b"}, nil)
	env.OnActivity(a.LoadProfiles, mock.Anything).Return(map[string]activities.Profile{
		"default": {Image: "reg/ci:latest"},
	}, nil)
	env.OnActivity(a.ListQueuedJobs, mock.Anything, "octo/a").Return(
		[]git.QueuedJob{{ID: 1, Labels: []string{"self-hosted"}}}, nil)
	env.OnActivity(a.ListQueuedJobs, mock.Anything, "octo/b").Return(
		[]git.QueuedJob{{ID: 2, Labels: []string{"self-hosted"}}, {ID: 3, Labels: []string{"self-hosted"}}}, nil)
	// Nothing in flight -> every queued job is a shortfall.
	env.OnActivity(a.CountActiveRunners, mock.Anything).Return(map[string]int{}, nil)

	// Stub the child so the parent only exercises its start path.
	env.RegisterWorkflow(HandleRunner)
	env.OnWorkflow(HandleRunner, mock.Anything, mock.Anything).Return(nil)

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
	if result.ReposScanned != 2 || result.QueuedJobs != 3 || result.RunnersStarted != 3 {
		t.Errorf("result = %+v, want 2 scanned / 3 queued / 3 started", result)
	}
}

func TestPollAndDispatch_TopsUpOnlyShortfall(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	env.OnActivity(a.ListWatchedRepos, mock.Anything).Return([]string{"octo/a"}, nil)
	env.OnActivity(a.LoadProfiles, mock.Anything).Return(map[string]activities.Profile{}, nil)
	env.OnActivity(a.ListQueuedJobs, mock.Anything, "octo/a").Return([]git.QueuedJob{
		{ID: 1, Labels: []string{"self-hosted"}},
		{ID: 2, Labels: []string{"self-hosted"}},
		{ID: 3, Labels: []string{"self-hosted"}},
	}, nil)
	// Two runners already cover this bucket -> only one more is needed.
	env.OnActivity(a.CountActiveRunners, mock.Anything).Return(
		map[string]int{"octo/a|self-hosted": 2}, nil)

	env.RegisterWorkflow(HandleRunner)
	env.OnWorkflow(HandleRunner, mock.Anything, mock.Anything).Return(nil)

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
	if result.QueuedJobs != 3 || result.ActiveRunners != 2 || result.RunnersStarted != 1 {
		t.Errorf("result = %+v, want 3 queued / 2 active / 1 started", result)
	}
}

func TestPollAndDispatch_NoShortfallStartsNothing(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	env.OnActivity(a.ListWatchedRepos, mock.Anything).Return([]string{"octo/a"}, nil)
	env.OnActivity(a.LoadProfiles, mock.Anything).Return(map[string]activities.Profile{}, nil)
	env.OnActivity(a.ListQueuedJobs, mock.Anything, "octo/a").Return(
		[]git.QueuedJob{{ID: 1, Labels: []string{"self-hosted"}}}, nil)
	// More runners in flight than queued jobs -> dispatch nothing (needed < 0).
	env.OnActivity(a.CountActiveRunners, mock.Anything).Return(
		map[string]int{"octo/a|self-hosted": 3}, nil)

	env.RegisterWorkflow(HandleRunner)
	env.OnWorkflow(HandleRunner, mock.Anything, mock.Anything).Return(nil)

	env.ExecuteWorkflow(PollAndDispatch, PollConfig{})

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result PollResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.RunnersStarted != 0 {
		t.Errorf("started = %d, want 0 (already over-covered)", result.RunnersStarted)
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
	env.OnActivity(a.CountActiveRunners, mock.Anything).Return(map[string]int{}, nil)

	env.RegisterWorkflow(HandleRunner)
	env.OnWorkflow(HandleRunner, mock.Anything, mock.Anything).Return(nil)

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

func TestHandleRunner_DispatchThenReap(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	var reaped string
	env.OnActivity(a.DispatchRunner, mock.Anything, mock.Anything).Return("ci-runner/dispatch-1-abc", nil)
	env.OnActivity(a.ReapRunner, mock.Anything, mock.Anything).Return(
		func(_ context.Context, id string) error { reaped = id; return nil })

	env.ExecuteWorkflow(HandleRunner, RunnerSpec{
		Repo:   "octo/widget",
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

func TestPollAndDispatch_CountActiveRunnersError(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	env.OnActivity(a.ListWatchedRepos, mock.Anything).Return([]string{"octo/a"}, nil)
	env.OnActivity(a.LoadProfiles, mock.Anything).Return(map[string]activities.Profile{}, nil)
	env.OnActivity(a.ListQueuedJobs, mock.Anything, "octo/a").Return(
		[]git.QueuedJob{{ID: 1, Labels: []string{"self-hosted"}}}, nil)
	// Can't reconcile without the active count -> the whole tick fails rather than
	// double-provisioning every queued job.
	env.OnActivity(a.CountActiveRunners, mock.Anything).Return(nil, errors.New("nomad down"))

	env.ExecuteWorkflow(PollAndDispatch, PollConfig{})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if env.GetWorkflowError() == nil {
		t.Fatal("expected PollAndDispatch to fail when the active-runner count errors")
	}
}

func TestHandleRunner_DispatchError(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	env.OnActivity(a.DispatchRunner, mock.Anything, mock.Anything).Return("", errors.New("403 permission denied"))

	env.ExecuteWorkflow(HandleRunner, RunnerSpec{Repo: "octo/widget", Labels: []string{"self-hosted"}})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if env.GetWorkflowError() == nil {
		t.Fatal("expected HandleRunner to fail when the dispatch fails")
	}
}

func TestHandleRunner_ReapError(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	env.OnActivity(a.DispatchRunner, mock.Anything, mock.Anything).Return("ci-runner/dispatch-1-abc", nil)
	env.OnActivity(a.ReapRunner, mock.Anything, mock.Anything).Return(errors.New("stop failed"))

	env.ExecuteWorkflow(HandleRunner, RunnerSpec{Repo: "octo/widget", Labels: []string{"self-hosted"}})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if env.GetWorkflowError() == nil {
		t.Fatal("expected HandleRunner to fail when the reap fails")
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
