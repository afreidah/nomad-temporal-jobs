// -------------------------------------------------------------------------------
// GitHub Token Renewer Activities - SonarCloud Tokens
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Renews each managed repo's SonarCloud analysis secret. SonarCloud tokens are
// minted out-of-band only as the one master token; a per-repo token is minted
// here from it. SonarCloud removed project scoping, so each minted token is a
// full-scope standard token -- the per-repo split buys independent rotation and
// revocation, not project isolation. Rotation is zero-downtime: a fresh
// uniquely-named token is minted and written to the repo secret before the
// repo's previous tokens are revoked, so a scan that runs mid-rotation always
// has a valid token. SonarCloud is reached through a narrow consumer interface
// so the activity tests with a fake.
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
	"munchbox/temporal-workers/shared/client/git"
)

// sonarClient is the SonarCloud surface the renewer uses: mint a user token,
// list the user's token names, and revoke a token. *sonarcloud.SonarCloud satisfies
// it structurally.
type sonarClient interface {
	MintToken(ctx context.Context, name string, expiry time.Time) (string, error)
	ListTokenNames(ctx context.Context) ([]string, error)
	RevokeToken(ctx context.Context, name string) error
}

// SonarRenewResult records the outcome of refreshing one repo's SonarCloud token
// secret.
type SonarRenewResult struct {
	Repo      string    `json:"repo"`
	TokenName string    `json:"token_name"`
	ExpiresAt time.Time `json:"expires_at"`
}

// RenewSonarCloudToken mints a fresh SonarCloud token for repo ("owner/repo"),
// writes it to the configured Actions secret, then revokes the repo's prior
// tokens. An unparseable repo or unconfigured SonarCloud is non-retryable;
// SonarCloud and GitHub API failures surface plain so Temporal retries them.
// Revoking the old tokens is best-effort: the new token is already live, so a
// revoke failure is logged, not surfaced as an error.
func (a *Activities) RenewSonarCloudToken(ctx context.Context, repo string) (SonarRenewResult, error) {
	logger := activity.GetLogger(ctx)

	if a.cfg.Sonar == nil {
		return SonarRenewResult{}, temporal.NewNonRetryableApplicationError(
			"SonarCloud renewal not configured (no client)", "SonarNotConfigured", nil)
	}

	owner, name, ok := git.SplitRepo(repo)
	if !ok {
		return SonarRenewResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("invalid repo %q, want owner/repo", repo), "InvalidRepo", nil)
	}

	ctx, span := shared.StartPeerSpan(ctx, "sonarcloud", "sonarcloud.renew_token",
		attribute.String("github.repo", repo))
	defer span.End()

	// Unique per-run name so the new token coexists with the old until the secret
	// is written -- zero-downtime rotation.
	tokenName := sonarTokenName(owner, name, activity.GetInfo(ctx).ScheduledTime)
	var expiry time.Time
	if a.cfg.SonarTokenTTL > 0 {
		expiry = activity.GetInfo(ctx).ScheduledTime.Add(a.cfg.SonarTokenTTL)
	}

	token, err := a.cfg.Sonar.MintToken(ctx, tokenName, expiry)
	if err != nil {
		return SonarRenewResult{}, fmt.Errorf("mint sonar token for %s: %w", repo, err)
	}
	if err := a.cfg.GitHub.SetRepoSecret(ctx, owner, name, a.cfg.SonarSecretName, token); err != nil {
		return SonarRenewResult{}, fmt.Errorf("set secret on %s: %w", repo, err)
	}

	// The new token is live in the repo secret; revoking the repo's prior tokens
	// is cleanup, so a failure here doesn't fail the renewal.
	if err := a.revokeOldSonarTokens(ctx, owner, name, tokenName); err != nil {
		logger.Warn("Failed to revoke prior SonarCloud tokens", "repo", repo, "error", err)
	}

	logger.Info("Renewed SonarCloud token secret",
		"repo", repo, "secret", a.cfg.SonarSecretName, "token_name", tokenName, "expires", expiry)
	return SonarRenewResult{Repo: repo, TokenName: tokenName, ExpiresAt: expiry}, nil
}

// revokeOldSonarTokens revokes every token this worker previously minted for
// owner/repo except keep (the just-minted one), identified by the renewer's
// per-repo name prefix.
func (a *Activities) revokeOldSonarTokens(ctx context.Context, owner, repo, keep string) error {
	names, err := a.cfg.Sonar.ListTokenNames(ctx)
	if err != nil {
		return err
	}
	prefix := sonarTokenPrefix(owner, repo)
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

// sonarTokenPrefix is the stable per-repo name prefix every token this worker
// mints for owner/repo shares, so prior tokens can be found for revocation
// without touching tokens minted elsewhere. Slashes delimit the segments;
// neither owner nor repo can contain a slash, so one repo's prefix can never
// match another's token.
func sonarTokenPrefix(owner, repo string) string {
	return "munchbox-ci/" + owner + "/" + repo + "/"
}

// sonarTokenName is the unique name for a token minted at t for owner/repo.
func sonarTokenName(owner, repo string, t time.Time) string {
	return fmt.Sprintf("%s%d", sonarTokenPrefix(owner, repo), t.UTC().Unix())
}
