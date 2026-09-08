// -------------------------------------------------------------------------------
// Shared Forgejo Client - Queued Job Discovery and Runner Registration
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Forgejo runs its own Actions implementation with a Gitea-compatible API, so
// the GitHub client cannot serve it even though the scaler asks both the same
// two questions: which jobs are waiting on a runner, and what token lets a
// runner register. This client answers those against a Forgejo instance and is
// shaped to satisfy the scaler's existing consumer interfaces unchanged.
//
// Forgejo mints a registration token on demand per repository, so a Forgejo
// repo behaves like an app-mode GitHub repo: the scaler mints per dispatch and
// the dispatched runner carries a credential that is useless once consumed.
// Nothing long-lived is handed to a runner.
// -------------------------------------------------------------------------------

package forgejo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"munchbox/temporal-workers/shared"
	"munchbox/temporal-workers/shared/client/git"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

const (
	// statusWaiting is the status Forgejo reports for a job that has been
	// created but not yet claimed by a runner.
	statusWaiting = "waiting"

	// defaultTimeout bounds a single API call. The scaler polls on a tick, so a
	// hung request must not outlive the tick that started it.
	defaultTimeout = 30 * time.Second
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Forgejo talks to one Forgejo instance with a fixed API token. Construct it
// with New.
type Forgejo struct {
	baseURL string
	token   string
	cli     *http.Client
}

// actionJob is the subset of Forgejo's action-job representation the scaler
// reads.
//
// TaskID is the discriminator that matters alongside Status: Forgejo reports a
// job as waiting from creation until a runner claims it, and sets TaskID when
// one does. Treating waiting alone as queued double-counts a job in the window
// between the claim and the status catching up, which would dispatch a second
// runner for work already being done.
type actionJob struct {
	ID     int64    `json:"id"`
	Name   string   `json:"name"`
	RepoID int64    `json:"repo_id"`
	RunsOn []string `json:"runs_on"`
	Status string   `json:"status"`
	TaskID int64    `json:"task_id"`
}

// registrationToken is Forgejo's reply when minting a runner token.
type registrationToken struct {
	Token string `json:"token"`
}

// -------------------------------------------------------------------------
// CONSTRUCTOR
// -------------------------------------------------------------------------

// New builds a client for the Forgejo instance at baseURL authenticating with
// token. baseURL is the instance root, with or without a trailing slash; the
// api/v1 prefix is added per call.
//
// The transport is OTel-instrumented so these calls appear in the service graph
// alongside the GitHub client's.
func New(baseURL, token string) (*Forgejo, error) {
	if baseURL == "" {
		return nil, errors.New("forgejo client: empty base URL")
	}
	if token == "" {
		return nil, errors.New("forgejo client: empty token")
	}
	return &Forgejo{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		cli: &http.Client{
			Transport: shared.OTelTransport("forgejo", nil),
			Timeout:   defaultTimeout,
		},
	}, nil
}

// -------------------------------------------------------------------------
// QUEUED JOB DISCOVERY
// -------------------------------------------------------------------------

// ListQueuedSelfHostedJobs returns the jobs in owner/repo that are waiting on a
// runner, in the shared QueuedJob shape the scaler reconciles against.
//
// Forgejo's endpoint filters by label only, so the queued test is applied here.
// RunID is left zero: Forgejo does not report the parent run on this endpoint,
// and the scaler reconciles on queue depth and labels rather than on run
// identity.
func (f *Forgejo) ListQueuedSelfHostedJobs(ctx context.Context, owner, repo string) ([]git.QueuedJob, error) {
	path := fmt.Sprintf("/api/v1/repos/%s/%s/actions/runners/jobs",
		url.PathEscape(owner), url.PathEscape(repo))

	var jobs []actionJob
	if err := f.get(ctx, path, &jobs); err != nil {
		return nil, fmt.Errorf("list queued jobs for %s/%s: %w", owner, repo, err)
	}

	out := make([]git.QueuedJob, 0, len(jobs))
	for _, j := range jobs {
		if !strings.EqualFold(j.Status, statusWaiting) || j.TaskID != 0 {
			continue
		}
		out = append(out, git.QueuedJob{
			ID:     j.ID,
			Name:   j.Name,
			Labels: j.RunsOn,
		})
	}
	return out, nil
}

// -------------------------------------------------------------------------
// RUNNER REGISTRATION
// -------------------------------------------------------------------------

// CreateRunnerRegistrationToken mints a registration token for owner/repo.
//
// The expiry return exists to satisfy the scaler's interface, which the GitHub
// App client populates from its response. Forgejo does not report one, so the
// zero time is returned; callers hand the token straight to a dispatch rather
// than caching it, so nothing reads the value.
func (f *Forgejo) CreateRunnerRegistrationToken(ctx context.Context, owner, repo string) (string, time.Time, error) {
	path := fmt.Sprintf("/api/v1/repos/%s/%s/actions/runners/registration-token",
		url.PathEscape(owner), url.PathEscape(repo))

	var tok registrationToken
	if err := f.get(ctx, path, &tok); err != nil {
		return "", time.Time{}, fmt.Errorf("mint registration token for %s/%s: %w", owner, repo, err)
	}
	if tok.Token == "" {
		return "", time.Time{}, fmt.Errorf("forgejo returned an empty registration token for %s/%s", owner, repo)
	}
	return tok.Token, time.Time{}, nil
}

// -------------------------------------------------------------------------
// INTERNALS
// -------------------------------------------------------------------------

// get issues an authenticated GET and decodes the body into out.
func (f *Forgejo) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "token "+f.token)
	req.Header.Set("Accept", "application/json")

	resp, err := f.cli.Do(req)
	if err != nil {
		return fmt.Errorf("request %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s: unexpected status %s", path, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}
