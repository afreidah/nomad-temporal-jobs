// -------------------------------------------------------------------------------
// Runner Scaler Workflows - Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Drives the parent and child workflows in the Temporal test environment with
// mocked activities: the parent tops up runners to cover the queued-job depth
// per (repo, labels), dispatches only the shortfall when runners are already in
// flight, and skips a repo whose listing errors without aborting the tick; a
// vault-mode repo dispatches its profile job with minting disabled; the child
// dispatches a runner and reaps it once the backstop timer fires. matchProfile's
// label->job selection is covered directly.
// -------------------------------------------------------------------------------

package workflows

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"munchbox/temporal-workers/runnerscaler/activities"
	"munchbox/temporal-workers/shared/client/git"
)

// forRepo matches a ListQueuedJobs PollRepo arg by its repo, so per-repo returns
// can be stubbed even though the activity now takes a struct.
func forRepo(repo string) any {
	return mock.MatchedBy(func(r activities.PollRepo) bool { return r.Repo == repo })
}

func appRepos(repos ...string) map[string]activities.RepoConfig {
	m := make(map[string]activities.RepoConfig, len(repos))
	for _, r := range repos {
		m[r] = activities.RepoConfig{} // empty mode => app
	}
	return m
}

func TestPollAndDispatch_DispatchesShortfall(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	env.OnActivity(a.LoadConfig, mock.Anything).Return(appRepos("octo/a", "octo/b"), nil)
	env.OnActivity(a.ListQueuedJobs, mock.Anything, forRepo("octo/a")).Return(
		[]git.QueuedJob{{ID: 1, Labels: []string{"self-hosted"}}}, nil)
	env.OnActivity(a.ListQueuedJobs, mock.Anything, forRepo("octo/b")).Return(
		[]git.QueuedJob{{ID: 2, Labels: []string{"self-hosted"}}, {ID: 3, Labels: []string{"self-hosted"}}}, nil)
	// Nothing in flight -> every queued job is a shortfall.
	env.OnActivity(a.CountActiveRunners, mock.Anything, mock.Anything).Return(map[string]int{}, nil)

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

	env.OnActivity(a.LoadConfig, mock.Anything).Return(appRepos("octo/a"), nil)
	env.OnActivity(a.ListQueuedJobs, mock.Anything, forRepo("octo/a")).Return([]git.QueuedJob{
		{ID: 1, Labels: []string{"self-hosted"}},
		{ID: 2, Labels: []string{"self-hosted"}},
		{ID: 3, Labels: []string{"self-hosted"}},
	}, nil)
	// Two runners already cover this bucket -> only one more is needed.
	env.OnActivity(a.CountActiveRunners, mock.Anything, mock.Anything).Return(
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

	env.OnActivity(a.LoadConfig, mock.Anything).Return(appRepos("octo/a"), nil)
	env.OnActivity(a.ListQueuedJobs, mock.Anything, forRepo("octo/a")).Return(
		[]git.QueuedJob{{ID: 1, Labels: []string{"self-hosted"}}}, nil)
	// More runners in flight than queued jobs -> dispatch nothing (needed < 0).
	env.OnActivity(a.CountActiveRunners, mock.Anything, mock.Anything).Return(
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

	env.OnActivity(a.LoadConfig, mock.Anything).Return(appRepos("octo/good", "octo/bad"), nil)
	env.OnActivity(a.ListQueuedJobs, mock.Anything, forRepo("octo/good")).Return(
		[]git.QueuedJob{{ID: 1, Labels: []string{"self-hosted"}}}, nil)
	env.OnActivity(a.ListQueuedJobs, mock.Anything, forRepo("octo/bad")).Return(
		nil, errors.New("github 500"))
	env.OnActivity(a.CountActiveRunners, mock.Anything, mock.Anything).Return(map[string]int{}, nil)

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

func TestPollAndDispatch_VaultModeDispatchesProfileJobWithoutMint(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	env.OnActivity(a.LoadConfig, mock.Anything).Return(map[string]activities.RepoConfig{
		"octo/private": {
			Mode:              activities.ModeVault,
			VaultPath:         "kv/octo-poll",  // low-priv poll token
			RegisterVaultPath: "kv/octo-admin", // higher-priv registration token
			Profiles: []activities.ProfileRule{
				{Label: "vm", Job: "vm-runner"},
				{Label: "custom", Job: "std-runner"},
			},
		},
	}, nil)
	// A non-vm job: should resolve to the "custom" rule's job.
	env.OnActivity(a.ListQueuedJobs, mock.Anything, forRepo("octo/private")).Return(
		[]git.QueuedJob{{ID: 1, Labels: []string{"self-hosted", "custom"}}}, nil)
	env.OnActivity(a.CountActiveRunners, mock.Anything, mock.Anything).Return(map[string]int{}, nil)

	var gotSpec RunnerSpec
	env.RegisterWorkflow(HandleRunner)
	env.OnWorkflow(HandleRunner, mock.Anything, mock.Anything).Return(
		func(_ workflow.Context, spec RunnerSpec) error { gotSpec = spec; return nil })

	env.ExecuteWorkflow(PollAndDispatch, PollConfig{})

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotSpec.Job != "std-runner" {
		t.Errorf("child job = %q, want std-runner (matched the custom rule)", gotSpec.Job)
	}
	if gotSpec.MintToken {
		t.Error("vault-mode child must not mint a registration token")
	}
	// The job registers with the higher-priv token, not the poll token.
	if gotSpec.VaultSecret != "kv/octo-admin" {
		t.Errorf("child VaultSecret = %q, want kv/octo-admin (registration token, not the poll token)", gotSpec.VaultSecret)
	}
}

func TestHandleRunner_DispatchThenReap(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	var reaped string
	env.OnActivity(a.DispatchRunner, mock.Anything, mock.Anything).Return("ci-runner/dispatch-1-abc", nil)
	env.OnActivity(a.WaitRunnerDone, mock.Anything, mock.Anything).Return(nil) // runner finished cleanly
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
	// Once WaitRunnerDone reports the runner terminal, the reap runs against the
	// dispatched job ID.
	if reaped != "ci-runner/dispatch-1-abc" {
		t.Errorf("reaped %q, want the dispatched job id", reaped)
	}
}

func TestHandleRunner_WaitBackstopStillReaps(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	// The wait ends in error (backstop deadline / wedged runner) -- the workflow
	// must still reap and complete cleanly, never orphaning the Nomad job.
	var reaped string
	env.OnActivity(a.DispatchRunner, mock.Anything, mock.Anything).Return("ci-runner/dispatch-1-abc", nil)
	env.OnActivity(a.WaitRunnerDone, mock.Anything, mock.Anything).Return(errors.New("backstop deadline"))
	env.OnActivity(a.ReapRunner, mock.Anything, mock.Anything).Return(
		func(_ context.Context, id string) error { reaped = id; return nil })

	env.ExecuteWorkflow(HandleRunner, RunnerSpec{Repo: "octo/widget", Labels: []string{"self-hosted"}})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("a wait timeout should still reap, not fail the workflow: %v", err)
	}
	if reaped != "ci-runner/dispatch-1-abc" {
		t.Errorf("reaped %q, want the dispatched job id even after a wait backstop", reaped)
	}
}

func TestPollAndDispatch_CountActiveRunnersError(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	env.OnActivity(a.LoadConfig, mock.Anything).Return(appRepos("octo/a"), nil)
	env.OnActivity(a.ListQueuedJobs, mock.Anything, forRepo("octo/a")).Return(
		[]git.QueuedJob{{ID: 1, Labels: []string{"self-hosted"}}}, nil)
	// Can't reconcile without the active count -> the whole tick fails rather than
	// double-provisioning every queued job.
	env.OnActivity(a.CountActiveRunners, mock.Anything, mock.Anything).Return(nil, errors.New("nomad down"))

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
	env.OnActivity(a.WaitRunnerDone, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(a.ReapRunner, mock.Anything, mock.Anything).Return(errors.New("stop failed"))

	env.ExecuteWorkflow(HandleRunner, RunnerSpec{Repo: "octo/widget", Labels: []string{"self-hosted"}})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if env.GetWorkflowError() == nil {
		t.Fatal("expected HandleRunner to fail when the reap fails")
	}
}

func TestMatchProfile(t *testing.T) {
	profiles := []activities.ProfileRule{
		{Label: "vm", Job: "vm-runner"},
		{Label: "custom", Job: "std-runner"},
	}
	// A job carrying both labels: the ordered match checks "vm" first, so it
	// wins -- the reason profiles are an ordered list, not the old first-non-
	// self-hosted-label heuristic.
	if got := matchProfile(profiles, []string{"self-hosted", "vm", "custom"}); got.Job != "vm-runner" {
		t.Errorf("job = %q, want vm-runner", got.Job)
	}
	// A job without the vm label falls through to the custom rule.
	if got := matchProfile(profiles, []string{"self-hosted", "custom"}); got.Job != "std-runner" {
		t.Errorf("job = %q, want std-runner", got.Job)
	}
	// No matching label -> zero rule -> default job, no image override.
	if got := matchProfile(profiles, []string{"self-hosted"}); got.Job != "" {
		t.Errorf("job = %q, want empty (default job)", got.Job)
	}
	// No profiles configured -> zero rule.
	if got := matchProfile(nil, []string{"self-hosted", "amd64"}); got.Job != "" || got.Image != "" {
		t.Errorf("got = %+v, want zero rule", got)
	}
}

func TestIsVaultMode(t *testing.T) {
	if !isVaultMode(activities.ModeVault) {
		t.Error("ModeVault must be vault-mode")
	}
	if isVaultMode(activities.ModeApp) || isVaultMode("") {
		t.Error("app and empty modes must not be vault-mode")
	}
}
