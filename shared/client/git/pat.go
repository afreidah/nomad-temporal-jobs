// -------------------------------------------------------------------------------
// Shared GitHub Client - Personal Access Token Job Discovery
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Some repos can't be reached through the GitHub App (the App isn't -- and can't
// be -- installed, e.g. a repo the operator doesn't own) but already have a
// pre-provisioned personal access token in the secret store that a statically
// deployed runner self-registers with. For those, the scaler doesn't mint
// anything: it only needs to read the Actions queue, which a PAT with
// actions:read can do directly. GitHubPAT is that read-only surface -- a plain
// token-authenticated client reusing the same queued-job discovery core as the
// App path, with none of the installation-token machinery.
// -------------------------------------------------------------------------------

package git

import (
	"context"
	"fmt"

	"github.com/google/go-github/v88/github"

	"munchbox/temporal-workers/shared"
)

// GitHubPAT lists queued self-hosted jobs using a personal access token
// directly, with no App installation. Construct it with NewGitHubPAT.
type GitHubPAT struct {
	cli *github.Client
}

// NewGitHubPAT builds a token-authenticated client. baseURL overrides the API
// endpoint (GHE or an httptest server); empty means the public api.github.com.
// The transport is OTel-instrumented so its calls appear in the service graph,
// matching the App client.
func NewGitHubPAT(token, baseURL string) (*GitHubPAT, error) {
	opts := []github.ClientOptionsFunc{
		github.WithTransport(shared.OTelTransport("github", nil)),
		github.WithAuthToken(token),
	}
	if baseURL != "" {
		opts = append(opts, github.WithEnterpriseURLs(baseURL, baseURL))
	}
	cli, err := github.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("github PAT client: %w", err)
	}
	return &GitHubPAT{cli: cli}, nil
}

// ListQueuedSelfHostedJobs returns the queued self-hosted Actions jobs in
// owner/repo, using the PAT directly. Requires actions:read on the token.
func (g *GitHubPAT) ListQueuedSelfHostedJobs(ctx context.Context, owner, repo string) ([]QueuedJob, error) {
	return listQueuedSelfHostedJobs(ctx, g.cli, owner, repo)
}
