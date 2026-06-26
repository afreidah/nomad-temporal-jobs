// -------------------------------------------------------------------------------
// GitHub Token Renewer Activities - Mint App Tokens into Repo Secrets
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Activities that keep each managed repository's CI secret continuously valid:
// read the repo list from Consul KV, then for each repo mint a short-lived
// GitHub App installation token and write it to the repo's Actions secret.
// Because the token is re-minted on every scheduled run, the secret never
// expires -- this replaces hand-rotated Personal Access Tokens. All GitHub and
// Consul I/O is reached through narrow consumer interfaces so the activities
// test with fakes.
// -------------------------------------------------------------------------------

package activities

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"munchbox/temporal-workers/shared"
)

// -------------------------------------------------------------------------
// CONSUMER INTERFACES
// -------------------------------------------------------------------------

// githubClient is the GitHub App surface the renewer uses: mint a
// workflow-scoped token and write a repository Actions secret. *shared.GitHub
// satisfies it structurally.
type githubClient interface {
	MintWorkflowToken(ctx context.Context, owner, repo string) (token string, expiry time.Time, err error)
	SetRepoSecret(ctx context.Context, owner, repo, name, value string) error
}

// kvGetter is the Consul KV surface the renewer uses: read the repo-list key.
// *shared.Consul satisfies it structurally.
type kvGetter interface {
	KVGet(ctx context.Context, key string) (value []byte, found bool, err error)
}

// -------------------------------------------------------------------------
// CONFIG AND CONSTRUCTOR
// -------------------------------------------------------------------------

// Config holds the static dependencies and locations for the renewer
// activities.
type Config struct {
	GitHub githubClient
	Repos  kvGetter

	// RepoListKey is the Consul KV key holding the newline-separated owner/repo
	// list. SecretName is the Actions secret each repo's token is written to.
	RepoListKey string
	SecretName  string

	// --- SonarCloud (optional): set only when the worker is configured to
	//     renew SonarCloud tokens. When Sonar is nil the SonarCloud activity is
	//     not registered. ---
	Sonar sonarClient
	// SonarSecretName is the Actions secret the SonarCloud token is written to.
	SonarSecretName string
	// SonarTokenTTL is how long a minted SonarCloud token is valid. Zero mints a
	// non-expiring token.
	SonarTokenTTL time.Duration
}

// Activities implements the token-renewer Temporal activities.
type Activities struct {
	cfg Config
}

// New constructs the activity set, applying defaults for empty locations.
func New(cfg Config) *Activities {
	if cfg.RepoListKey == "" {
		cfg.RepoListKey = "github/token-renewer/repos"
	}
	if cfg.SecretName == "" {
		cfg.SecretName = "RELEASE_PAT"
	}
	return &Activities{cfg: cfg}
}

// RepoRenewResult records the outcome of refreshing one repo's token secret.
type RepoRenewResult struct {
	Repo      string    `json:"repo"`
	ExpiresAt time.Time `json:"expires_at"`
}

// -------------------------------------------------------------------------
// ACTIVITIES
// -------------------------------------------------------------------------

// ListRepos reads the repo list from Consul KV and returns the owner/repo
// entries. Blank lines and # comments are ignored. A missing key is
// non-retryable (the operator must seed it).
func (a *Activities) ListRepos(ctx context.Context) ([]string, error) {
	logger := activity.GetLogger(ctx)

	ctx, span := shared.StartPeerSpan(ctx, "consul", "consul.kv_get")
	defer span.End()

	raw, found, err := a.cfg.Repos.KVGet(ctx, a.cfg.RepoListKey)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("consul kv key %q not found", a.cfg.RepoListKey), "RepoListMissing", nil)
	}

	repos := parseRepoList(string(raw))
	logger.Info("Loaded repo list", "key", a.cfg.RepoListKey, "count", len(repos))
	return repos, nil
}

// RenewRepoToken mints a fresh workflow token for repo ("owner/repo") and writes
// it to the configured Actions secret, returning the token's expiry. An
// unparseable repo is non-retryable; GitHub API failures surface plain so
// Temporal retries them.
func (a *Activities) RenewRepoToken(ctx context.Context, repo string) (RepoRenewResult, error) {
	logger := activity.GetLogger(ctx)

	ctx, span := shared.StartPeerSpan(ctx, "github", "github.renew_repo_token",
		attribute.String("github.repo", repo))
	defer span.End()

	owner, name, ok := splitRepo(repo)
	if !ok {
		return RepoRenewResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("invalid repo %q, want owner/repo", repo), "InvalidRepo", nil)
	}

	token, expiry, err := a.cfg.GitHub.MintWorkflowToken(ctx, owner, name)
	if err != nil {
		return RepoRenewResult{}, fmt.Errorf("mint token for %s: %w", repo, err)
	}
	if err := a.cfg.GitHub.SetRepoSecret(ctx, owner, name, a.cfg.SecretName, token); err != nil {
		return RepoRenewResult{}, fmt.Errorf("set secret on %s: %w", repo, err)
	}

	logger.Info("Renewed repo token secret", "repo", repo, "secret", a.cfg.SecretName, "expires", expiry)
	return RepoRenewResult{Repo: repo, ExpiresAt: expiry}, nil
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// parseRepoList splits a newline-separated owner/repo list, dropping blank lines
// and # comments and trimming surrounding whitespace.
func parseRepoList(s string) []string {
	var repos []string
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		repos = append(repos, line)
	}
	return repos
}

// splitRepo splits "owner/repo" into its two non-empty parts.
func splitRepo(repo string) (owner, name string, ok bool) {
	owner, name, ok = strings.Cut(strings.TrimSpace(repo), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", false
	}
	return owner, name, true
}
