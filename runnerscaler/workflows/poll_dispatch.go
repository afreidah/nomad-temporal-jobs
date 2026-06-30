// -------------------------------------------------------------------------------
// Runner Scaler Parent Workflow - Poll Queued Jobs, Dispatch Runners
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// PollAndDispatch is the scheduled entry point. Each tick it loads the watched
// repos and runner profiles, scans the repos (bounded concurrency) for queued
// self-hosted Actions jobs, and starts one HandleQueuedJob child per job. Each
// child is keyed runner-<repo>-<job_id> with a reject-duplicate ID policy, so a
// job still queued on the next tick can't spawn a second runner -- Temporal's
// workflow-ID dedup is the whole state store. Children are abandoned (they
// outlive this tick) but the parent waits for each to *start* before returning,
// so an abandoned child can't be lost. Pure orchestration; all I/O is in
// activities.
// -------------------------------------------------------------------------------

package workflows

import (
	"fmt"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"munchbox/temporal-workers/runnerscaler/activities"
	"munchbox/temporal-workers/shared"
	"munchbox/temporal-workers/shared/client/git"
)

// a is a nil-typed activity stub for compile-time method references.
var a *activities.Activities

// defaultProfile is the profile label used for a bare `runs-on: [self-hosted]`
// job that names no profile.
const defaultProfile = "default"

// PollConfig is the workflow input.
type PollConfig struct {
	// Concurrency bounds how many repos are scanned in parallel so a large fleet
	// doesn't burst the GitHub API. Default 4.
	Concurrency int `json:"concurrency"`
	// ReapAfter overrides the child runner backstop lifetime (0 => child default).
	ReapAfter time.Duration `json:"reap_after,omitempty"`
}

func (c *PollConfig) applyDefaults() {
	if c.Concurrency <= 0 {
		c.Concurrency = 4
	}
}

// PollResult summarizes one poll tick.
type PollResult struct {
	ReposScanned   int `json:"repos_scanned"`
	RunnersStarted int `json:"runners_started"`
	Skipped        int `json:"skipped"` // jobs already handled by a live/closed child
}

// PollAndDispatch scans every watched repo for queued self-hosted jobs and
// starts a runner child for each new one.
func PollAndDispatch(ctx workflow.Context, config PollConfig) (*PollResult, error) {
	logger := workflow.GetLogger(ctx)
	config.applyDefaults()

	quickCtx := workflow.WithActivityOptions(ctx, shared.QuickActivityOptions())

	var repos []string
	if err := workflow.ExecuteActivity(quickCtx, a.ListWatchedRepos).Get(quickCtx, &repos); err != nil {
		return nil, fmt.Errorf("list watched repos: %w", err)
	}
	var profiles map[string]activities.Profile
	if err := workflow.ExecuteActivity(quickCtx, a.LoadProfiles).Get(quickCtx, &profiles); err != nil {
		return nil, fmt.Errorf("load profiles: %w", err)
	}
	logger.Info("Polling for queued runners", "repos", len(repos), "profiles", len(profiles), "concurrency", config.Concurrency)

	// Scan repos with bounded concurrency; results land per-index so the
	// post-barrier dispatch stays deterministic (no concurrent appends).
	queued := make([][]git.QueuedJob, len(repos))
	sem := workflow.NewBufferedChannel(ctx, config.Concurrency)
	wg := workflow.NewWaitGroup(ctx)
	for i, repo := range repos {
		wg.Add(1)
		workflow.Go(ctx, func(gctx workflow.Context) {
			defer wg.Done()
			sem.Send(gctx, nil)
			defer sem.Receive(gctx, nil)

			rctx := workflow.WithActivityOptions(gctx, shared.QuickActivityOptions())
			var jobs []git.QueuedJob
			if err := workflow.ExecuteActivity(rctx, a.ListQueuedJobs, repo).Get(rctx, &jobs); err != nil {
				logger.Warn("List queued jobs failed; skipping repo this tick", "repo", repo, "error", err)
				return
			}
			queued[i] = jobs
		})
	}
	wg.Wait(ctx)

	// Start a child per queued job after the barrier (deterministic order).
	result := &PollResult{ReposScanned: len(repos)}
	for i, repo := range repos {
		for _, job := range queued[i] {
			started, err := startRunnerChild(ctx, repo, job, profiles, config.ReapAfter)
			switch {
			case err != nil:
				logger.Warn("Failed to start runner child", "repo", repo, "job_id", job.ID, "error", err)
			case started:
				result.RunnersStarted++
			default:
				result.Skipped++
			}
		}
	}

	logger.Info("Poll complete",
		"repos", result.ReposScanned, "started", result.RunnersStarted, "skipped", result.Skipped)
	return result, nil
}

// startRunnerChild starts one HandleQueuedJob child for job. It returns
// (false, nil) when the child ID already exists -- the expected dedup signal
// that a runner for this job was already handled -- and (true, nil) when a new
// child started. The parent waits only for the child to start (not complete):
// the child is abandoned so it outlives this tick, but waiting for start ensures
// an abandoned child is never dropped.
func startRunnerChild(ctx workflow.Context, repo string, job git.QueuedJob, profiles map[string]activities.Profile, reapAfter time.Duration) (bool, error) {
	childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowID:            fmt.Sprintf("runner-%s-%d", repo, job.ID),
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		ParentClosePolicy:     enumspb.PARENT_CLOSE_POLICY_ABANDON,
	})

	spec := JobSpec{
		Repo:      repo,
		JobID:     job.ID,
		Labels:    job.Labels,
		Image:     profiles[profileLabel(job.Labels)].Image,
		ReapAfter: reapAfter,
	}

	child := workflow.ExecuteChildWorkflow(childCtx, HandleQueuedJob, spec)
	var exec workflow.Execution
	if err := child.GetChildWorkflowExecution().Get(childCtx, &exec); err != nil {
		if temporal.IsWorkflowExecutionAlreadyStartedError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// profileLabel picks the runner profile for a job from its runs-on labels: the
// first label that isn't "self-hosted", or "default" for a bare self-hosted job.
func profileLabel(labels []string) string {
	for _, l := range labels {
		if l != "self-hosted" {
			return l
		}
	}
	return defaultProfile
}
