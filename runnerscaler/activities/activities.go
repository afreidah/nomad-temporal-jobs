// -------------------------------------------------------------------------------
// Runner Scaler Activities - Discover Queued Jobs, Dispatch Ephemeral Runners
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Activities that turn a queued self-hosted Actions job into a running runner:
// read the watched repos and runner profiles from Consul KV, list each repo's
// queued self-hosted jobs on GitHub, and for each one mint a registration token
// and dispatch a single ephemeral Nomad ci-runner with it. The token is minted
// inside DispatchRunner so it is never returned to the workflow (and so never
// lands in Temporal history); only the dispatched job ID comes back, which the
// reaper uses to stop a runner that never picked its job up. All external I/O is
// reached through narrow consumer interfaces so the activities test with fakes.
// -------------------------------------------------------------------------------

package activities

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"munchbox/temporal-workers/shared"
	"munchbox/temporal-workers/shared/client/git"
	"munchbox/temporal-workers/shared/client/nomad"
)

// attrGitHubRepo is the span attribute key for the owner/repo a call targets.
const attrGitHubRepo = "github.repo"

// -------------------------------------------------------------------------
// CONSUMER INTERFACES
// -------------------------------------------------------------------------

// githubRunners is the GitHub App surface the scaler uses: discover queued
// self-hosted jobs and mint a runner registration token. *git.GitHub satisfies
// it structurally.
type githubRunners interface {
	ListQueuedSelfHostedJobs(ctx context.Context, owner, repo string) ([]git.QueuedJob, error)
	CreateRunnerRegistrationToken(ctx context.Context, owner, repo string) (token string, expiry time.Time, err error)
}

// kvGetter is the Consul KV surface the scaler uses: read the repo list and the
// profiles map. *consul.Consul satisfies it structurally.
type kvGetter interface {
	KVGet(ctx context.Context, key string) (value []byte, found bool, err error)
}

// jobDispatcher is the Nomad surface the scaler uses: dispatch a parameterized
// runner job, stop (reap) a dispatched one, and list the active dispatched
// runners so the poller can reconcile by depth. *nomad.Nomad satisfies it
// structurally.
type jobDispatcher interface {
	DispatchJob(ctx context.Context, jobID string, meta map[string]string) (dispatchedID string, err error)
	StopJob(ctx context.Context, jobID string) error
	ActiveRunnerSlots(ctx context.Context, parentJobID string) ([]nomad.RunnerSlot, error)
	RunnerTerminal(ctx context.Context, jobID string) (done bool, err error)
}

// waitPollInterval is how often WaitRunnerDone re-checks the dispatched runner.
// A var so tests can shorten it.
var waitPollInterval = 5 * time.Second

// -------------------------------------------------------------------------
// CONFIG AND CONSTRUCTOR
// -------------------------------------------------------------------------

// Profile maps a runs-on label to the runner image to dispatch for it. Resources
// are fixed in the parameterized Nomad job (the resources stanza can't be driven
// by dispatch meta); per-profile resourcing would be a per-profile job.
type Profile struct {
	Image string `json:"image"`
}

// Config holds the scaler activities' dependencies and Consul/Nomad locations.
type Config struct {
	GitHub githubRunners
	KV     kvGetter
	Nomad  jobDispatcher

	// RepoListKey holds the newline-separated owner/repo list; ProfilesKey holds
	// the JSON label->Profile map; RunnerJobID is the parameterized Nomad job
	// dispatched for each runner.
	RepoListKey string
	ProfilesKey string
	RunnerJobID string
}

// Activities implements the runner-scaler Temporal activities.
type Activities struct {
	cfg Config
}

// New constructs the activity set, applying defaults for empty locations.
func New(cfg Config) *Activities {
	if cfg.RepoListKey == "" {
		cfg.RepoListKey = "runners/repos"
	}
	if cfg.ProfilesKey == "" {
		cfg.ProfilesKey = "runners/profiles"
	}
	if cfg.RunnerJobID == "" {
		cfg.RunnerJobID = "ci-runner"
	}
	return &Activities{cfg: cfg}
}

// DispatchSpec is the input to DispatchRunner: which repo to register the runner
// against, the labels to register it with, and the image its profile selected
// (empty means the Nomad job's default image). No job_id: an ephemeral runner
// is not bound to a specific queued job -- it takes whichever matching job is
// queued -- so the poller dispatches by (repo, labels) depth, not per job.
type DispatchSpec struct {
	Repo   string   `json:"repo"` // "owner/repo"
	Labels []string `json:"labels"`
	Image  string   `json:"image,omitempty"`
}

// -------------------------------------------------------------------------
// ACTIVITIES
// -------------------------------------------------------------------------

// ListWatchedRepos reads the watched repo list from Consul KV. Blank lines and
// # comments are ignored. A missing key is non-retryable (the operator seeds it).
func (a *Activities) ListWatchedRepos(ctx context.Context) ([]string, error) {
	logger := activity.GetLogger(ctx)

	ctx, span := shared.StartPeerSpan(ctx, "consul", "consul.kv_get")
	defer span.End()

	raw, found, err := a.cfg.KV.KVGet(ctx, a.cfg.RepoListKey)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("consul kv key %q not found", a.cfg.RepoListKey), "RepoListMissing", nil)
	}

	repos := git.ParseRepoList(string(raw))
	logger.Info("Loaded watched repos", "key", a.cfg.RepoListKey, "count", len(repos))
	return repos, nil
}

// LoadProfiles reads the JSON label->Profile map from Consul KV. A missing key
// is not an error: with no profiles, runners are dispatched on the job's default
// image. Malformed JSON is non-retryable.
func (a *Activities) LoadProfiles(ctx context.Context) (map[string]Profile, error) {
	logger := activity.GetLogger(ctx)

	ctx, span := shared.StartPeerSpan(ctx, "consul", "consul.kv_get")
	defer span.End()

	raw, found, err := a.cfg.KV.KVGet(ctx, a.cfg.ProfilesKey)
	if err != nil {
		return nil, err
	}
	if !found {
		logger.Info("No runner profiles configured; using job default image", "key", a.cfg.ProfilesKey)
		return map[string]Profile{}, nil
	}

	var profiles map[string]Profile
	if err := json.Unmarshal(raw, &profiles); err != nil {
		return nil, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("parse profiles at %q", a.cfg.ProfilesKey), "InvalidProfiles", err)
	}
	logger.Info("Loaded runner profiles", "key", a.cfg.ProfilesKey, "count", len(profiles))
	return profiles, nil
}

// ListQueuedJobs returns the queued self-hosted Actions jobs for repo
// ("owner/repo"). An unparseable repo is non-retryable.
func (a *Activities) ListQueuedJobs(ctx context.Context, repo string) ([]git.QueuedJob, error) {
	owner, name, ok := git.SplitRepo(repo)
	if !ok {
		return nil, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("invalid repo %q, want owner/repo", repo), "InvalidRepo", nil)
	}

	ctx, span := shared.StartPeerSpan(ctx, "github", "github.list_queued_jobs",
		attribute.String(attrGitHubRepo, repo))
	defer span.End()

	jobs, err := a.cfg.GitHub.ListQueuedSelfHostedJobs(ctx, owner, name)
	if err != nil {
		return nil, fmt.Errorf("list queued jobs for %s: %w", repo, err)
	}
	return jobs, nil
}

// DispatchRunner mints a fresh registration token for spec.Repo and dispatches
// one ephemeral ci-runner carrying it, returning the dispatched Nomad job ID.
// The token is built and consumed here so it never returns to the workflow.
// Because each call creates a new runner, this activity must not be retried
// (the workflow runs it under NoRetry).
func (a *Activities) DispatchRunner(ctx context.Context, spec DispatchSpec) (string, error) {
	logger := activity.GetLogger(ctx)

	owner, name, ok := git.SplitRepo(spec.Repo)
	if !ok {
		return "", temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("invalid repo %q, want owner/repo", spec.Repo), "InvalidRepo", nil)
	}

	tokCtx, span := shared.StartPeerSpan(ctx, "github", "github.create_runner_token",
		attribute.String(attrGitHubRepo, spec.Repo))
	token, _, err := a.cfg.GitHub.CreateRunnerRegistrationToken(tokCtx, owner, name)
	span.End()
	if err != nil {
		return "", fmt.Errorf("mint registration token for %s: %w", spec.Repo, err)
	}

	meta := map[string]string{
		"repo_url":     "https://github.com/" + spec.Repo,
		"runner_token": token,
		"labels":       strings.Join(spec.Labels, ","),
	}
	if spec.Image != "" {
		meta["runner_image"] = spec.Image
	}

	dispCtx, span := shared.StartPeerSpan(ctx, "nomad", "nomad.dispatch_job",
		attribute.String(attrGitHubRepo, spec.Repo))
	defer span.End()

	id, err := a.cfg.Nomad.DispatchJob(dispCtx, a.cfg.RunnerJobID, meta)
	if err != nil {
		return "", fmt.Errorf("dispatch runner for %s: %w", spec.Repo, err)
	}
	logger.Info("Dispatched ephemeral runner",
		"repo", spec.Repo, "dispatched", id, "labels", spec.Labels)
	return id, nil
}

// CountActiveRunners returns the number of active (pending or running) ephemeral
// runners bucketed by (repo, labels), keyed with RunnerBucketKey. The poller
// subtracts these from the queued-job count per bucket and dispatches only the
// shortfall: a runner isn't bound to a specific job_id, so reconciling by depth
// -- not one runner per job -- is what lets a job stranded by a diverted or
// failed runner get a fresh one on the next tick.
func (a *Activities) CountActiveRunners(ctx context.Context) (map[string]int, error) {
	ctx, span := shared.StartPeerSpan(ctx, "nomad", "nomad.active_runners")
	defer span.End()

	slots, err := a.cfg.Nomad.ActiveRunnerSlots(ctx, a.cfg.RunnerJobID)
	if err != nil {
		return nil, fmt.Errorf("list active runners: %w", err)
	}
	counts := make(map[string]int, len(slots))
	for _, s := range slots {
		counts[RunnerBucketKey(repoFromURL(s.RepoURL), splitLabels(s.Labels))]++
	}
	return counts, nil
}

// RunnerBucketKey is the reconciliation key for a (repo, labels) pair: the repo
// plus its labels sorted and joined, so a runner's dispatch meta and a queued
// job's runs-on labels bucket together regardless of label order.
func RunnerBucketKey(repo string, labels []string) string {
	ls := append([]string(nil), labels...)
	sort.Strings(ls)
	return repo + "|" + strings.Join(ls, ",")
}

// repoFromURL turns a runner's repo_url dispatch meta back into owner/repo.
func repoFromURL(url string) string {
	return strings.TrimPrefix(url, "https://github.com/")
}

// splitLabels parses a comma-joined dispatch labels meta into a slice ("" => nil).
func splitLabels(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// WaitRunnerDone blocks until the dispatched runner job reaches a terminal state
// (all allocations finished, or the job is gone), heartbeating while it polls,
// so the caller can reap promptly instead of waiting out the whole backstop.
// Transient poll errors are logged and retried -- only a terminal runner or a
// cancelled/timed-out context ends the wait, so a blip never triggers an early
// reap of a still-running runner. The activity's StartToCloseTimeout is the
// backstop ceiling: a wedged runner times the activity out and the caller reaps
// anyway.
func (a *Activities) WaitRunnerDone(ctx context.Context, dispatchedID string) error {
	logger := activity.GetLogger(ctx)

	ctx, span := shared.StartPeerSpan(ctx, "nomad", "nomad.wait_runner",
		attribute.String("nomad.job", dispatchedID))
	defer span.End()

	for {
		done, err := a.cfg.Nomad.RunnerTerminal(ctx, dispatchedID)
		switch {
		case err != nil:
			logger.Warn("Runner wait poll failed; retrying", "job", dispatchedID, "error", err)
		case done:
			logger.Info("Runner finished", "job", dispatchedID)
			return nil
		}
		activity.RecordHeartbeat(ctx, dispatchedID)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitPollInterval):
		}
	}
}

// ReapRunner stops the dispatched runner job. A job that is already gone (picked
// up its work and self-deregistered, or already reaped) is treated as success.
func (a *Activities) ReapRunner(ctx context.Context, dispatchedID string) error {
	logger := activity.GetLogger(ctx)

	ctx, span := shared.StartPeerSpan(ctx, "nomad", "nomad.stop_job",
		attribute.String("nomad.job", dispatchedID))
	defer span.End()

	if err := a.cfg.Nomad.StopJob(ctx, dispatchedID); err != nil {
		if nomad.IsJobNotFound(err) {
			logger.Info("Runner already gone, nothing to reap", "job", dispatchedID)
			return nil
		}
		return fmt.Errorf("reap runner %s: %w", dispatchedID, err)
	}
	logger.Info("Reaped ephemeral runner", "job", dispatchedID)
	return nil
}
