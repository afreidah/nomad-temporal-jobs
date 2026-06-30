// -------------------------------------------------------------------------------
// Shared GitHub App Client - Self-Hosted Runner Registration and Job Discovery
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// The runner-scaler worker drives on-demand ephemeral runners from the same
// GitHub App used for token renewal. It needs two things the App can do that the
// token-renewer never used: mint a runner *registration* token (so a freshly
// dispatched container can join the repo as a runner) and discover which Actions
// jobs are currently queued for a self-hosted runner. Both mint a narrowly
// scoped installation token first -- registration needs administration:write,
// job discovery needs actions:read -- so the App's blast radius is the only
// thing that changes, not the auth model.
// -------------------------------------------------------------------------------

package git

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/go-github/v88/github"

	"munchbox/temporal-workers/shared"
)

// selfHostedLabel is the runs-on label every self-hosted job carries; the scaler
// only handles jobs that ask for it.
const selfHostedLabel = "self-hosted"

// QueuedJob is a queued Actions job awaiting a self-hosted runner. Labels are
// the job's runs-on labels (always including "self-hosted"); the scaler maps the
// remaining label to a runner profile.
type QueuedJob struct {
	ID     int64
	RunID  int64
	Name   string
	Labels []string
}

// installationClient mints an installation token scoped to repo with perms and
// returns a token-authenticated client. This is the one place the per-call token
// dance lives (SetRepoSecret and the runner methods all share it); the App
// client itself only ever holds the JWT.
func (g *GitHub) installationClient(ctx context.Context, repo string, perms *github.InstallationPermissions) (*github.Client, error) {
	tok, _, err := g.app.Apps.CreateInstallationToken(ctx, g.instID, &github.InstallationTokenOptions{
		Repositories: []string{repo},
		Permissions:  perms,
	})
	if err != nil {
		return nil, fmt.Errorf("mint installation token for %s: %w", repo, err)
	}
	opts := []github.ClientOptionsFunc{
		github.WithTransport(shared.OTelTransport("github", nil)),
		github.WithAuthToken(tok.GetToken()),
	}
	if g.baseURL != "" {
		opts = append(opts, github.WithEnterpriseURLs(g.baseURL, g.baseURL))
	}
	cli, err := github.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("github token client: %w", err)
	}
	return cli, nil
}

// CreateRunnerRegistrationToken mints a short-lived registration token a runner
// uses to join owner/repo, returning the token and its expiry. Requires the App
// installation to grant administration:write.
func (g *GitHub) CreateRunnerRegistrationToken(ctx context.Context, owner, repo string) (string, time.Time, error) {
	cli, err := g.installationClient(ctx, repo, &github.InstallationPermissions{Administration: new("write")})
	if err != nil {
		return "", time.Time{}, err
	}
	tok, _, err := cli.Actions.CreateRegistrationToken(ctx, owner, repo)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("create runner registration token for %s/%s: %w", owner, repo, err)
	}
	return tok.GetToken(), tok.GetExpiresAt().Time, nil
}

// ListQueuedSelfHostedJobs returns the Actions jobs in owner/repo that are
// queued for a self-hosted runner. GitHub has no "list queued jobs" endpoint, so
// it enumerates runs that are queued or in_progress (a multi-job run can be
// in_progress with one leg still waiting) and keeps the queued, self-hosted jobs
// under them, de-duplicating by job ID across the two run states. Requires
// actions:read.
func (g *GitHub) ListQueuedSelfHostedJobs(ctx context.Context, owner, repo string) ([]QueuedJob, error) {
	cli, err := g.installationClient(ctx, repo, &github.InstallationPermissions{Actions: new("read")})
	if err != nil {
		return nil, err
	}

	var all []QueuedJob
	for _, status := range []string{"queued", "in_progress"} {
		runIDs, err := listWorkflowRunIDs(ctx, cli, owner, repo, status)
		if err != nil {
			return nil, err
		}
		for _, runID := range runIDs {
			jobs, err := queuedSelfHostedJobsForRun(ctx, cli, owner, repo, runID)
			if err != nil {
				return nil, err
			}
			all = append(all, jobs...)
		}
	}
	return dedupByID(all), nil
}

// listWorkflowRunIDs returns the IDs of owner/repo's workflow runs in the given
// status, following pagination.
func listWorkflowRunIDs(ctx context.Context, cli *github.Client, owner, repo, status string) ([]int64, error) {
	opts := &github.ListWorkflowRunsOptions{Status: status, ListOptions: github.ListOptions{PerPage: 100}}
	var ids []int64
	for run, err := range cli.Actions.ListRepositoryWorkflowRunsIter(ctx, owner, repo, opts) {
		if err != nil {
			return nil, fmt.Errorf("list %s runs for %s/%s: %w", status, owner, repo, err)
		}
		ids = append(ids, run.GetID())
	}
	return ids, nil
}

// queuedSelfHostedJobsForRun returns runID's jobs that are still queued and ask
// for a self-hosted runner.
func queuedSelfHostedJobsForRun(ctx context.Context, cli *github.Client, owner, repo string, runID int64) ([]QueuedJob, error) {
	opts := &github.ListWorkflowJobsOptions{Filter: "latest", ListOptions: github.ListOptions{PerPage: 100}}
	var jobs []QueuedJob
	for job, err := range cli.Actions.ListWorkflowJobsIter(ctx, owner, repo, runID, opts) {
		if err != nil {
			return nil, fmt.Errorf("list jobs for run %d in %s/%s: %w", runID, owner, repo, err)
		}
		if job.GetStatus() != "queued" || !slices.Contains(job.Labels, selfHostedLabel) {
			continue
		}
		jobs = append(jobs, QueuedJob{
			ID:     job.GetID(),
			RunID:  job.GetRunID(),
			Name:   job.GetName(),
			Labels: job.Labels,
		})
	}
	return jobs, nil
}

// dedupByID drops jobs with a repeated ID, keeping first-seen order (the same
// job can surface under both the queued and in_progress run sweeps).
func dedupByID(jobs []QueuedJob) []QueuedJob {
	seen := make(map[int64]struct{}, len(jobs))
	out := jobs[:0]
	for _, j := range jobs {
		if _, dup := seen[j.ID]; dup {
			continue
		}
		seen[j.ID] = struct{}{}
		out = append(out, j)
	}
	return out
}
