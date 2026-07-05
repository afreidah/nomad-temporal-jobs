// -------------------------------------------------------------------------------
// Runner Scaler Parent Workflow - Poll Queued Jobs, Top Up Runners
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// PollAndDispatch is the scheduled entry point. Each tick it loads the per-repo
// config (each repo's provisioning mode + profiles), scans the repos (bounded
// concurrency) for queued self-hosted Actions jobs, and reconciles supply
// against demand: it buckets the queued jobs by (repo, labels), counts the
// active (pending/running) ephemeral runners in each bucket across every
// dispatched job, and starts HandleRunner children only for the shortfall.
// Ephemeral repo-scoped runners are not bound to a specific job_id -- GitHub
// hands any label-matching runner whichever job is queued -- so keying one child
// per job (the old model) stranded a job whenever its runner was diverted to
// another job and never retried it. Reconciling by depth self-heals: a job left
// unserved is still queued next tick and simply tops the count back up. Children
// are abandoned (they outlive this tick); the parent waits only for each to
// *start*. Pure orchestration; all I/O is in activities.
// -------------------------------------------------------------------------------

package workflows

import (
	"fmt"
	"slices"
	"sort"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/workflow"

	"munchbox/temporal-workers/runnerscaler/activities"
	"munchbox/temporal-workers/shared"
	"munchbox/temporal-workers/shared/client/git"
)

// a is a nil-typed activity stub for compile-time method references.
var a *activities.Activities

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

// PollAndDispatch scans every configured repo for queued self-hosted jobs and
// tops up the runner count per (repo, labels) bucket to cover them.
func PollAndDispatch(ctx workflow.Context, config PollConfig) (*PollResult, error) {
	logger := workflow.GetLogger(ctx)
	config.applyDefaults()

	quickCtx := workflow.WithActivityOptions(ctx, shared.QuickActivityOptions())

	var repoCfgs map[string]activities.RepoConfig
	if err := workflow.ExecuteActivity(quickCtx, a.LoadConfig).Get(quickCtx, &repoCfgs); err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	// Iterate repos in sorted order so every derived slice (scan results, the
	// extra-job list handed to CountActiveRunners) is deterministic across replay.
	repos := make([]string, 0, len(repoCfgs))
	for repo := range repoCfgs {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	logger.Info("Polling for queued runners", "repos", len(repos), "concurrency", config.Concurrency)

	// Scan repos with bounded concurrency; results land per-index so the
	// post-barrier reconciliation stays deterministic (no concurrent appends).
	queued := make([][]git.QueuedJob, len(repos))
	sem := workflow.NewBufferedChannel(ctx, config.Concurrency)
	wg := workflow.NewWaitGroup(ctx)
	for i, repo := range repos {
		rc := repoCfgs[repo]
		wg.Add(1)
		workflow.Go(ctx, func(gctx workflow.Context) {
			defer wg.Done()
			sem.Send(gctx, nil)
			defer sem.Receive(gctx, nil)

			rctx := workflow.WithActivityOptions(gctx, shared.QuickActivityOptions())
			pr := activities.PollRepo{Repo: repo, Mode: rc.Mode, VaultPath: rc.VaultPath}
			var jobs []git.QueuedJob
			if err := workflow.ExecuteActivity(rctx, a.ListQueuedJobs, pr).Get(rctx, &jobs); err != nil {
				logger.Warn("List queued jobs failed; skipping repo this tick", "repo", repo, "error", err)
				return
			}
			queued[i] = jobs
		})
	}
	wg.Wait(ctx)

	// Count the runners already in flight per (repo, labels) bucket so we top up
	// only the shortfall, across every dispatched job any profile names (the
	// default job plus each profile's job). A failure here is fatal to the tick:
	// dispatching without the active count would double-provision every queued job.
	var activeCounts map[string]int
	if err := workflow.ExecuteActivity(quickCtx, a.CountActiveRunners, profileJobs(repos, repoCfgs)).Get(quickCtx, &activeCounts); err != nil {
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
			if err := startRunnerChild(ctx, b.repo, b.labels, repoCfgs[b.repo], config.ReapAfter, seq); err != nil {
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

// profileJobs collects the distinct non-empty parameterized job IDs named by any
// repo profile, in deterministic order (sorted repos, then profile order), so
// CountActiveRunners can tally active runners across every dispatch job -- not
// just the default one -- without a bucket served by a non-default job reading
// active = 0 and over-provisioning.
func profileJobs(repos []string, repoCfgs map[string]activities.RepoConfig) []string {
	var jobs []string
	seen := map[string]bool{}
	for _, repo := range repos {
		for _, rule := range repoCfgs[repo].Profiles {
			if rule.Job != "" && !seen[rule.Job] {
				seen[rule.Job] = true
				jobs = append(jobs, rule.Job)
			}
		}
	}
	return jobs
}

// startRunnerChild starts one HandleRunner child for a (repo, labels) bucket.
// The child ID is unique per (parent run, seq) so tops-up never collide, and the
// child is abandoned so it outlives this tick -- but the parent waits for it to
// start so an abandoned child is never dropped. Unlike the old per-job model
// there is no dedup: the parent already reconciled how many to start.
func startRunnerChild(ctx workflow.Context, repo string, labels []string, rc activities.RepoConfig, reapAfter time.Duration, seq int) error {
	runID := workflow.GetInfo(ctx).WorkflowExecution.RunID
	childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowID:            fmt.Sprintf("runner-%s-%d", runID, seq),
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		ParentClosePolicy:     enumspb.PARENT_CLOSE_POLICY_ABANDON,
	})

	rule := matchProfile(rc.Profiles, labels)
	// vault-mode carries the repo's *registration* secret path so the dispatched
	// job self-registers -- distinct from the poll token when registration needs
	// higher privilege (RegisterVaultPath), else the same token (VaultPath).
	// app-mode (both empty) mints a token instead.
	registerSecret := rc.RegisterVaultPath
	if registerSecret == "" {
		registerSecret = rc.VaultPath
	}
	spec := RunnerSpec{
		Repo:        repo,
		Labels:      labels,
		Job:         rule.Job,
		Image:       rule.Image,
		MintToken:   !isVaultMode(rc.Mode),
		VaultSecret: registerSecret,
		ReapAfter:   reapAfter,
	}

	child := workflow.ExecuteChildWorkflow(childCtx, HandleRunner, spec)
	var exec workflow.Execution
	return child.GetChildWorkflowExecution().Get(childCtx, &exec)
}

// matchProfile selects the profile rule for a job from its runs-on labels: the
// first rule (in config order) whose Label the job carries. A repo with no
// profiles -- or a job matching none -- yields the zero rule, i.e. the default
// job and no image override, which is the plain self-hosted case.
func matchProfile(profiles []activities.ProfileRule, labels []string) activities.ProfileRule {
	for _, rule := range profiles {
		if slices.Contains(labels, rule.Label) {
			return rule
		}
	}
	return activities.ProfileRule{}
}

// isVaultMode reports whether mode is the vault (self-registering) strategy; any
// other value -- including the empty default -- is app-mode, which mints a token.
func isVaultMode(mode string) bool {
	return mode == activities.ModeVault
}
