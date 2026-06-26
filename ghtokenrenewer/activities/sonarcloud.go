// -------------------------------------------------------------------------------
// GitHub Token Renewer Activities - SonarCloud Project Tokens
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Renews each managed repo's SonarCloud analysis secret. SonarCloud tokens are
// minted out-of-band only as the one master token; per-project tokens are minted
// here from it, so each repo gets a token scoped to just its project. Rotation is
// zero-downtime: a fresh uniquely-named token is minted and written to the repo
// secret before the project's previous tokens are revoked, so a scan that runs
// mid-rotation always has a valid token. SonarCloud is reached through a narrow
// consumer interface so the activity tests with a fake.
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

// sonarClient is the SonarCloud surface the renewer uses: mint a project-scoped
// analysis token, list the user's token names, and revoke a token.
// *shared.SonarCloud satisfies it structurally.
type sonarClient interface {
	MintProjectToken(ctx context.Context, projectKey, name string, expiry time.Time) (string, error)
	ListTokenNames(ctx context.Context) ([]string, error)
	RevokeToken(ctx context.Context, name string) error
}

// SonarRenewResult records the outcome of refreshing one repo's SonarCloud token
// secret.
type SonarRenewResult struct {
	Repo       string    `json:"repo"`
	ProjectKey string    `json:"project_key"`
	TokenName  string    `json:"token_name"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// RenewSonarCloudToken mints a fresh project analysis token for repo
// ("owner/repo"), writes it to the configured Actions secret, then revokes the
// project's prior tokens. An unparseable repo or unconfigured SonarCloud is
// non-retryable; SonarCloud and GitHub API failures surface plain so Temporal
// retries them. Revoking the old tokens is best-effort: the new token is already
// live, so a revoke failure is logged, not surfaced as an error.
func (a *Activities) RenewSonarCloudToken(ctx context.Context, repo string) (SonarRenewResult, error) {
	logger := activity.GetLogger(ctx)

	if a.cfg.Sonar == nil || a.cfg.SonarOrg == "" {
		return SonarRenewResult{}, temporal.NewNonRetryableApplicationError(
			"SonarCloud renewal not configured (missing client or org)", "SonarNotConfigured", nil)
	}

	owner, name, ok := splitRepo(repo)
	if !ok {
		return SonarRenewResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("invalid repo %q, want owner/repo", repo), "InvalidRepo", nil)
	}
	projectKey := sonarProjectKey(a.cfg.SonarOrg, name)

	ctx, span := shared.StartPeerSpan(ctx, "sonarcloud", "sonarcloud.renew_project_token",
		attribute.String("github.repo", repo),
		attribute.String("sonar.project", projectKey))
	defer span.End()

	// Unique per-run name so the new token coexists with the old until the secret
	// is written -- zero-downtime rotation.
	tokenName := sonarTokenName(projectKey, activity.GetInfo(ctx).ScheduledTime)
	var expiry time.Time
	if a.cfg.SonarTokenTTL > 0 {
		expiry = activity.GetInfo(ctx).ScheduledTime.Add(a.cfg.SonarTokenTTL)
	}

	token, err := a.cfg.Sonar.MintProjectToken(ctx, projectKey, tokenName, expiry)
	if err != nil {
		return SonarRenewResult{}, fmt.Errorf("mint sonar token for %s: %w", projectKey, err)
	}
	if err := a.cfg.GitHub.SetRepoSecret(ctx, owner, name, a.cfg.SonarSecretName, token); err != nil {
		return SonarRenewResult{}, fmt.Errorf("set secret on %s: %w", repo, err)
	}

	// The new token is live in the repo secret; revoking the project's prior
	// tokens is cleanup, so a failure here doesn't fail the renewal.
	if err := a.revokeOldSonarTokens(ctx, projectKey, tokenName); err != nil {
		logger.Warn("Failed to revoke prior SonarCloud tokens", "project", projectKey, "error", err)
	}

	logger.Info("Renewed SonarCloud token secret",
		"repo", repo, "project", projectKey, "secret", a.cfg.SonarSecretName, "token_name", tokenName, "expires", expiry)
	return SonarRenewResult{Repo: repo, ProjectKey: projectKey, TokenName: tokenName, ExpiresAt: expiry}, nil
}

// revokeOldSonarTokens revokes every token this worker previously minted for
// projectKey except keep (the just-minted one), identified by the renewer's
// per-project name prefix.
func (a *Activities) revokeOldSonarTokens(ctx context.Context, projectKey, keep string) error {
	names, err := a.cfg.Sonar.ListTokenNames(ctx)
	if err != nil {
		return err
	}
	prefix := sonarTokenPrefix(projectKey)
	var errs []error
	for _, n := range names {
		if n == keep || !strings.HasPrefix(n, prefix) {
			continue
		}
		if err := a.cfg.Sonar.RevokeToken(ctx, n); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("revoke %d prior token(s): %v", len(errs), errs)
	}
	return nil
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// sonarProjectKey derives a repo's SonarCloud project key from the org and repo
// name, matching the "<org>_<repo>" key GitHub-imported projects get by default.
func sonarProjectKey(org, repo string) string {
	return org + "_" + repo
}

// sonarTokenPrefix is the stable per-project name prefix every token this worker
// mints for projectKey shares, so prior tokens can be found for revocation
// without touching tokens minted elsewhere. The trailing "-" keeps one project's
// prefix from matching another whose key extends it.
func sonarTokenPrefix(projectKey string) string {
	return "munchbox-ci-" + projectKey + "-"
}

// sonarTokenName is the unique name for a token minted at t for projectKey.
func sonarTokenName(projectKey string, t time.Time) string {
	return fmt.Sprintf("%s%d", sonarTokenPrefix(projectKey), t.UTC().Unix())
}
