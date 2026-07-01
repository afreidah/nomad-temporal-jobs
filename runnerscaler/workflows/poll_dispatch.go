// -------------------------------------------------------------------------------
// Runner Scaler Parent Workflow - Poll Queued Jobs, Top Up Runners
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// PollAndDispatch is the scheduled entry point. Each tick it loads the watched
// repos and runner profiles, scans the repos (bounded concurrency) for queued
// self-hosted Actions jobs, and reconciles supply against demand: it buckets the
// queued jobs by (repo, labels), counts the active (pending/running) ephemeral
// runners in each bucket, and starts HandleRunner children only for the
// shortfall. Ephemeral repo-scoped runners are not bound to a specific job_id --
// GitHub hands any label-matching runner whichever job is queued -- so keying one
// child per job (the old model) stranded a job whenever its runner was diverted
// to another job and never retried it. Reconciling by depth self-heals: a job
// left unserved is still queued next tick and simply tops the count back up.
// Children are abandoned (they outlive this tick); the parent waits only for each
// to *start*. Pure orchestration; all I/O is in activities.
// -------------------------------------------------------------------------------

package workflows

import (
	"fmt"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
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
	QueuedJobs     int `json:"queued_jobs"`     // queued self-hosted jobs seen across repos
	ActiveRunners  int `json:"active_runners"`  // pending/running runners already covering queued buckets
	RunnersStarted int `json:"runners_started"` // new runners dispatched this tick (the shortfall)
}

// runnerBucket is the demand side of one (repo, labels) reconciliation: the repo
// and labels a runner would be dispatched with, and how many jobs are queued.
type runnerBucket struct {
	repo   string
	labels []string
	queued int
}

// PollAndDispatch scans every watched repo for queued self-hosted jobs and tops
// up the runner count per (repo, labels) bucket to cover them.
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
	// post-barrier reconciliation stays deterministic (no concurrent appends).
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

	// Count the runners already in flight per (repo, labels) bucket so we top up
	// only the shortfall. A failure here is fatal to the tick: dispatching without
	// the active count would double-provision every queued job.
	var activeCounts map[string]int
	if err := workflow.ExecuteActivity(quickCtx, a.CountActiveRunners).Get(quickCtx, &activeCounts); err != nil {
		return nil, fmt.Errorf("count active runners: %w", err)
	}

	// Bucket queued jobs by (repo, labels) in deterministic scan order.
	buckets := make(map[string]*runnerBucket)
	var order []string
	for i, repo := range repos {
		for _, job := range queued[i] {
			key := activities.RunnerBucketKey(repo, job.Labels)
			b := buckets[key]
			if b == nil {
				b = &runnerBucket{repo: repo, labels: job.Labels}
				buckets[key] = b
				order = append(order, key)
			}
			b.queued++
		}
	}

	// Top up each bucket to cover its queued jobs.
	result := &PollResult{ReposScanned: len(repos)}
	seq := 0
	for _, key := range order {
		b := buckets[key]
		active := activeCounts[key]
		result.QueuedJobs += b.queued
		result.ActiveRunners += active

		// range over a negative shortfall iterates zero times -- an over-covered
		// bucket dispatches nothing.
		needed := b.queued - active
		for range needed {
			if err := startRunnerChild(ctx, b.repo, b.labels, profiles, config.ReapAfter, seq); err != nil {
				logger.Warn("Failed to start runner child", "repo", b.repo, "labels", b.labels, "error", err)
			} else {
				result.RunnersStarted++
			}
			seq++
		}
	}

	logger.Info("Poll complete",
		"repos", result.ReposScanned, "queued", result.QueuedJobs,
		"active", result.ActiveRunners, "started", result.RunnersStarted)
	return result, nil
}

// startRunnerChild starts one HandleRunner child for a (repo, labels) bucket.
// The child ID is unique per (parent run, seq) so tops-up never collide, and the
// child is abandoned so it outlives this tick -- but the parent waits for it to
// start so an abandoned child is never dropped. Unlike the old per-job model
// there is no dedup: the parent already reconciled how many to start.
func startRunnerChild(ctx workflow.Context, repo string, labels []string, profiles map[string]activities.Profile, reapAfter time.Duration, seq int) error {
	runID := workflow.GetInfo(ctx).WorkflowExecution.RunID
	childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowID:            fmt.Sprintf("runner-%s-%d", runID, seq),
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		ParentClosePolicy:     enumspb.PARENT_CLOSE_POLICY_ABANDON,
	})

	spec := RunnerSpec{
		Repo:      repo,
		Labels:    labels,
		Image:     profiles[profileLabel(labels)].Image,
		ReapAfter: reapAfter,
	}

	child := workflow.ExecuteChildWorkflow(childCtx, HandleRunner, spec)
	var exec workflow.Execution
	return child.GetChildWorkflowExecution().Get(childCtx, &exec)
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
