// -------------------------------------------------------------------------------
// Runner Scaler Activities - Discover Queued Jobs, Dispatch Ephemeral Runners
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Activities that turn a queued self-hosted Actions job into a running runner,
// with a per-repo provisioning strategy read from Consul KV. Each repo picks a
// mode: "app" (the default) polls the GitHub App installation and mints a fresh
// registration token per dispatch; "vault" polls with a personal access token
// read from the secret store and mints nothing -- the dispatched job carries its
// own credential and self-registers, for repos the App can't be installed on.
// Either way the poller lists queued jobs, reconciles them against the active
// runners across every dispatched job, and dispatches the shortfall. A minted
// token is built inside DispatchRunner so it never returns to the workflow (and
// never lands in Temporal history); only the dispatched job ID comes back. All
// external I/O is reached through narrow consumer interfaces so the activities
// test with fakes.
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

// Provisioning modes. An empty mode is treated as ModeApp.
const (
	// ModeApp polls the GitHub App installation and mints a registration token
	// per dispatch (App must grant Administration + Actions on the repo).
	ModeApp = "app"
	// ModeVault polls with a PAT from the secret store and mints nothing -- the
	// dispatched job self-registers from its own stored credential.
	ModeVault = "vault"
)

// -------------------------------------------------------------------------
// CONSUMER INTERFACES
// -------------------------------------------------------------------------

// githubApp is the App surface the scaler uses for app-mode repos: discover
// queued self-hosted jobs and mint a runner registration token. *git.GitHub
// satisfies it structurally, and it is also a githubLister (it has the list
// method), so listerFor can return it directly for app-mode.
type githubApp interface {
	ListQueuedSelfHostedJobs(ctx context.Context, owner, repo string) ([]git.QueuedJob, error)
	CreateRunnerRegistrationToken(ctx context.Context, owner, repo string) (token string, expiry time.Time, err error)
}

// githubLister is the job-discovery surface shared by both modes: the App client
// and a PAT client both satisfy it. app-mode reuses the App client; vault-mode
// builds a PAT lister per call.
type githubLister interface {
	ListQueuedSelfHostedJobs(ctx context.Context, owner, repo string) ([]git.QueuedJob, error)
}

// kvGetter is the Consul KV surface the scaler uses: read the per-repo config.
// *consul.Consul satisfies it structurally.
type kvGetter interface {
	KVGet(ctx context.Context, key string) (value []byte, found bool, err error)
}

// secretReader is the secret-store surface vault-mode uses: read a runner PAT by
// path. *vault.VaultClient satisfies it structurally.
type secretReader interface {
	ReadKV(ctx context.Context, path string) (map[string]any, error)
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

// RepoConfig is one repo's provisioning strategy, read from the config JSON.
type RepoConfig struct {
	// Mode is "app" (default) or "vault"; see the mode constants.
	Mode string `json:"mode,omitempty"`
	// VaultPath is the secret-store KV path (field "token") the scaler *polls*
	// with in vault-mode -- only Actions:read is needed, so a write-collaborator
	// PAT suffices.
	VaultPath string `json:"vaultPath,omitempty"`
	// RegisterVaultPath is the KV path (field "token") the dispatched job
	// *registers* with. Registration needs admin on the repo, which the poll
	// token may lack, so it can be a separate, higher-privilege PAT. Empty means
	// reuse VaultPath (one token does both).
	RegisterVaultPath string `json:"registerVaultPath,omitempty"`
	// Profiles maps a distinguishing runs-on label to the parameterized Nomad
	// job (and optional image) dispatched for jobs carrying it. Rules are
	// evaluated in order; the first whose Label is among a queued job's labels
	// wins. Empty means the default job (RunnerJobID) with the job's own image.
	Profiles []ProfileRule `json:"profiles,omitempty"`
}

// ProfileRule maps a runs-on label to the runner job dispatched for it.
type ProfileRule struct {
	Label string `json:"label"`
	Job   string `json:"job,omitempty"`
	Image string `json:"image,omitempty"`
	// Cap on concurrent runners for this pool; 0 = unlimited.
	MaxConcurrent int `json:"maxConcurrent,omitempty"`
}

// Config holds the scaler activities' dependencies and Consul locations.
type Config struct {
	GitHub githubApp     // app-mode poll + mint (the App client)
	KV     kvGetter      // per-repo config
	Nomad  jobDispatcher // dispatch + reap + active-runner listing
	Vault  secretReader  // vault-mode PAT reads

	// NewPATLister builds a token-authenticated job lister for vault-mode repos.
	// Injected so the git package stays out of the activity's test surface.
	NewPATLister func(token string) (githubLister, error)

	// ConfigKey holds the JSON repo->RepoConfig map; RunnerJobID is the
	// parameterized Nomad job dispatched when a repo/profile names none.
	ConfigKey   string
	RunnerJobID string
}

// Activities implements the runner-scaler Temporal activities.
type Activities struct {
	cfg Config
}

// New constructs the activity set, applying defaults for empty locations.
func New(cfg Config) *Activities {
	if cfg.ConfigKey == "" {
		cfg.ConfigKey = "runners/config"
	}
	if cfg.RunnerJobID == "" {
		cfg.RunnerJobID = "ci-runner"
	}
	if cfg.NewPATLister == nil {
		cfg.NewPATLister = func(token string) (githubLister, error) {
			return git.NewGitHubPAT(token, "")
		}
	}
	return &Activities{cfg: cfg}
}

// PollRepo is the ListQueuedJobs input: the repo to poll and the strategy fields
// that decide which GitHub client discovers its queued jobs.
type PollRepo struct {
	Repo      string `json:"repo"` // "owner/repo"
	Mode      string `json:"mode,omitempty"`
	VaultPath string `json:"vault_path,omitempty"`
}

// DispatchSpec is the input to DispatchRunner: which repo to register the runner
// against, the labels to register it with, the parameterized job to dispatch
// (empty => RunnerJobID), the image its profile selected (empty => the job's
// default), and whether to mint a registration token. No job_id: an ephemeral
// runner is not bound to a specific queued job -- it takes whichever matching
// job is queued -- so the poller dispatches by (repo, labels) depth, not per job.
type DispatchSpec struct {
	Repo   string   `json:"repo"` // "owner/repo"
	Labels []string `json:"labels"`
	Job    string   `json:"job,omitempty"`
	Image  string   `json:"image,omitempty"`
	// MintToken (app-mode) mints a registration token and passes it as
	// runner_token. VaultSecret (vault-mode) is the secret-store path the
	// dispatched job reads its own PAT from, passed as runner_secret so a
	// self-registering job needs no minted token. They are mutually exclusive.
	MintToken   bool   `json:"mint_token"`
	VaultSecret string `json:"vault_secret,omitempty"`
}

// -------------------------------------------------------------------------
// ACTIVITIES
// -------------------------------------------------------------------------

// LoadConfig reads the JSON repo->RepoConfig map from Consul KV. A missing key
// is non-retryable (the operator seeds it); malformed JSON is non-retryable.
func (a *Activities) LoadConfig(ctx context.Context) (map[string]RepoConfig, error) {
	logger := activity.GetLogger(ctx)

	ctx, span := shared.StartPeerSpan(ctx, "consul", "consul.kv_get")
	defer span.End()

	raw, found, err := a.cfg.KV.KVGet(ctx, a.cfg.ConfigKey)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("consul kv key %q not found", a.cfg.ConfigKey), "ConfigMissing", nil)
	}

	var cfg map[string]RepoConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("parse config at %q", a.cfg.ConfigKey), "InvalidConfig", err)
	}
	logger.Info("Loaded runner config", "key", a.cfg.ConfigKey, "repos", len(cfg))
	return cfg, nil
}

// ListQueuedJobs returns the queued self-hosted Actions jobs for r.Repo, polling
// through the GitHub client its mode selects: the App installation for app-mode,
// or a PAT read from the secret store for vault-mode. An unparseable repo, a
// vault-mode repo with no vaultPath, or a missing token are non-retryable.
func (a *Activities) ListQueuedJobs(ctx context.Context, r PollRepo) ([]git.QueuedJob, error) {
	owner, name, ok := git.SplitRepo(r.Repo)
	if !ok {
		return nil, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("invalid repo %q, want owner/repo", r.Repo), "InvalidRepo", nil)
	}

	lister, err := a.listerFor(ctx, r)
	if err != nil {
		return nil, err
	}

	ctx, span := shared.StartPeerSpan(ctx, "github", "github.list_queued_jobs",
		attribute.String(attrGitHubRepo, r.Repo))
	defer span.End()

	jobs, err := lister.ListQueuedSelfHostedJobs(ctx, owner, name)
	if err != nil {
		return nil, fmt.Errorf("list queued jobs for %s: %w", r.Repo, err)
	}
	return jobs, nil
}

// listerFor picks the job-discovery client for r's mode: the shared App client
// for app-mode, or a PAT lister built from the repo's secret-store token for
// vault-mode. The PAT is read fresh each call so a rotated token is picked up.
func (a *Activities) listerFor(ctx context.Context, r PollRepo) (githubLister, error) {
	if !strings.EqualFold(r.Mode, ModeVault) {
		return a.cfg.GitHub, nil // app-mode (default): the App client is a githubLister
	}

	if r.VaultPath == "" {
		return nil, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("repo %q is vault-mode but sets no vaultPath", r.Repo), "MissingVaultPath", nil)
	}
	rctx, span := shared.StartPeerSpan(ctx, "vault", "vault.read_kv")
	data, err := a.cfg.Vault.ReadKV(rctx, r.VaultPath)
	span.End()
	if err != nil {
		return nil, fmt.Errorf("read runner token for %s at %s: %w", r.Repo, r.VaultPath, err)
	}
	token, _ := data["token"].(string)
	if token == "" {
		return nil, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("no token field at %s for %s", r.VaultPath, r.Repo), "MissingRunnerToken", nil)
	}
	return a.cfg.NewPATLister(token)
}

// DispatchRunner dispatches one ephemeral runner for spec's (repo, labels) and
// returns the dispatched Nomad job ID. It always carries repo_url + labels meta
// so CountActiveRunners can bucket the runner it creates. In app-mode
// (spec.MintToken) it also mints a fresh registration token and passes it as
// runner_token; in vault-mode it mints nothing -- the parameterized job reads
// its own credential from the secret store and self-registers. The token is
// built and consumed here so it never returns to the workflow. Because each call
// creates a new runner, this activity must not be retried (the workflow runs it
// under NoRetry).
func (a *Activities) DispatchRunner(ctx context.Context, spec DispatchSpec) (string, error) {
	logger := activity.GetLogger(ctx)

	owner, name, ok := git.SplitRepo(spec.Repo)
	if !ok {
		return "", temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("invalid repo %q, want owner/repo", spec.Repo), "InvalidRepo", nil)
	}

	meta := map[string]string{
		"repo_url": "https://github.com/" + spec.Repo,
		"labels":   strings.Join(spec.Labels, ","),
	}

	if spec.MintToken {
		tokCtx, span := shared.StartPeerSpan(ctx, "github", "github.create_runner_token",
			attribute.String(attrGitHubRepo, spec.Repo))
		token, _, err := a.cfg.GitHub.CreateRunnerRegistrationToken(tokCtx, owner, name)
		span.End()
		if err != nil {
			return "", fmt.Errorf("mint registration token for %s: %w", spec.Repo, err)
		}
		meta["runner_token"] = token
	}
	// vault-mode: hand the job the secret-store path so it self-registers from
	// its own PAT -- the token itself never transits the scaler or Nomad meta.
	if spec.VaultSecret != "" {
		meta["runner_secret"] = spec.VaultSecret
	}
	if spec.Image != "" {
		meta["runner_image"] = spec.Image
	}

	jobID := spec.Job
	if jobID == "" {
		jobID = a.cfg.RunnerJobID
	}

	dispCtx, span := shared.StartPeerSpan(ctx, "nomad", "nomad.dispatch_job",
		attribute.String(attrGitHubRepo, spec.Repo))
	defer span.End()

	id, err := a.cfg.Nomad.DispatchJob(dispCtx, jobID, meta)
	if err != nil {
		return "", fmt.Errorf("dispatch runner for %s: %w", spec.Repo, err)
	}
	logger.Info("Dispatched ephemeral runner",
		"repo", spec.Repo, "job", jobID, "dispatched", id, "labels", spec.Labels, "minted", spec.MintToken)
	return id, nil
}

// CountActiveRunners returns the number of active (pending or running) ephemeral
// runners bucketed by (repo, labels), keyed with RunnerBucketKey, across the
// default runner job and every extra dispatched job named by a repo profile.
// The poller subtracts these from the queued-job count per bucket and dispatches
// only the shortfall: a runner isn't bound to a specific job_id, so reconciling
// by depth -- not one runner per job -- is what lets a job stranded by a diverted
// or failed runner get a fresh one on the next tick. Counting must span every
// dispatch job, or a bucket served by a non-default job would always read active
// = 0 and over-provision each tick.
func (a *Activities) CountActiveRunners(ctx context.Context, extraJobIDs []string) (map[string]int, error) {
	ctx, span := shared.StartPeerSpan(ctx, "nomad", "nomad.active_runners")
	defer span.End()

	jobs := []string{a.cfg.RunnerJobID}
	seen := map[string]bool{a.cfg.RunnerJobID: true}
	for _, j := range extraJobIDs {
		if j != "" && !seen[j] {
			seen[j] = true
			jobs = append(jobs, j)
		}
	}

	counts := make(map[string]int)
	for _, jobID := range jobs {
		slots, err := a.cfg.Nomad.ActiveRunnerSlots(ctx, jobID)
		if err != nil {
			return nil, fmt.Errorf("list active runners for %s: %w", jobID, err)
		}
		for _, s := range slots {
			counts[RunnerBucketKey(repoFromURL(s.RepoURL), splitLabels(s.Labels))]++
		}
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
