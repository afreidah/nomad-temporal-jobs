// -------------------------------------------------------------------------------
// Shared GitHub App Client - Installation Tokens and Repo Secrets
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// One GitHub App, installed across many repositories, is the only way to mint
// PR-capable tokens programmatically -- user PATs can't be API-minted and they
// expire on a calendar. This client authenticates as the App (a JWT signed with
// the App private key) to mint short-lived, repo-scoped installation tokens, and
// to write repository Actions secrets (encrypted with the repo's public key via
// a NaCl sealed box, the libsodium format GitHub requires). The token-sync
// worker uses it to keep each repo's CI secret freshly minted so it never
// expires.
// -------------------------------------------------------------------------------

package shared

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v88/github"
	"golang.org/x/crypto/nacl/box"
)

// GitHubConfig configures a GitHub App-authenticated client. PrivateKeyPEM is
// the App's PEM private key and AppID identifies the App. InstallationID is
// optional: when zero it is discovered (a personal account has one installation).
type GitHubConfig struct {
	AppID          int64
	InstallationID int64
	PrivateKeyPEM  []byte
}

// GitHub is a GitHub App client. It mints short-lived installation access tokens
// and writes repository Actions secrets. Construct it with NewGitHub; workers
// consume it through their own narrow interfaces.
type GitHub struct {
	app    *github.Client // authenticated as the App (JWT); mints installation tokens
	instID int64
}

// NewGitHub builds an App-authenticated client from cfg, discovering the
// installation ID when it isn't provided. The HTTP transport is
// OTel-instrumented so GitHub calls appear as edges in the service graph.
func NewGitHub(ctx context.Context, cfg GitHubConfig) (*GitHub, error) {
	appTransport, err := ghinstallation.NewAppsTransport(otelTransport("github", nil), cfg.AppID, cfg.PrivateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("github app transport: %w", err)
	}
	app, err := github.NewClient(github.WithTransport(appTransport))
	if err != nil {
		return nil, fmt.Errorf("github app client: %w", err)
	}

	instID := cfg.InstallationID
	if instID == 0 {
		insts, _, err := app.Apps.ListInstallations(ctx, &github.ListOptions{PerPage: 1})
		if err != nil {
			return nil, fmt.Errorf("list app installations: %w", err)
		}
		if len(insts) == 0 {
			return nil, fmt.Errorf("github app %d has no installations", cfg.AppID)
		}
		instID = insts[0].GetID()
	}
	return &GitHub{app: app, instID: instID}, nil
}

// workflowTokenPermissions are what the *stored* token grants a release/CI
// workflow: open and merge PRs, push commits. It deliberately excludes secrets,
// so a leaked stored token can't rewrite secrets (the job mints a separate
// secrets-scoped token for the write itself).
func workflowTokenPermissions() *github.InstallationPermissions {
	return &github.InstallationPermissions{
		Contents:     new("write"),
		PullRequests: new("write"),
	}
}

// MintWorkflowToken mints a short-lived installation token scoped to a single
// repo with the permissions a release/CI workflow needs, returning the token and
// its expiry.
func (g *GitHub) MintWorkflowToken(ctx context.Context, owner, repo string) (string, time.Time, error) {
	tok, _, err := g.app.Apps.CreateInstallationToken(ctx, g.instID, &github.InstallationTokenOptions{
		Repositories: []string{repo},
		Permissions:  workflowTokenPermissions(),
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("mint workflow token for %s/%s: %w", owner, repo, err)
	}
	return tok.GetToken(), tok.GetExpiresAt().Time, nil
}

// SetRepoSecret writes value as the Actions secret `name` on owner/repo. It
// mints a secrets-scoped token for the repo, fetches the repo's public key,
// seals value (NaCl anonymous sealed box), and stores the ciphertext.
func (g *GitHub) SetRepoSecret(ctx context.Context, owner, repo, name, value string) error {
	tok, _, err := g.app.Apps.CreateInstallationToken(ctx, g.instID, &github.InstallationTokenOptions{
		Repositories: []string{repo},
		Permissions:  &github.InstallationPermissions{Secrets: new("write")},
	})
	if err != nil {
		return fmt.Errorf("mint secrets token for %s/%s: %w", owner, repo, err)
	}
	cli, err := github.NewClient(
		github.WithTransport(otelTransport("github", nil)),
		github.WithAuthToken(tok.GetToken()),
	)
	if err != nil {
		return fmt.Errorf("github client for %s/%s: %w", owner, repo, err)
	}

	pubKey, _, err := cli.Actions.GetRepoPublicKey(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("get public key for %s/%s: %w", owner, repo, err)
	}
	sealed, err := sealSecret(pubKey.GetKey(), value)
	if err != nil {
		return fmt.Errorf("seal secret for %s/%s: %w", owner, repo, err)
	}
	if _, err := cli.Actions.CreateOrUpdateRepoSecret(ctx, owner, repo, &github.EncryptedSecret{
		Name:           name,
		KeyID:          pubKey.GetKeyID(),
		EncryptedValue: sealed,
	}); err != nil {
		return fmt.Errorf("set secret %s on %s/%s: %w", name, owner, repo, err)
	}
	return nil
}

// sealSecret encrypts plaintext for a repo's base64 NaCl public key using an
// anonymous sealed box, returning the base64 ciphertext GitHub stores.
func sealSecret(b64PublicKey, plaintext string) (string, error) {
	pub, err := base64.StdEncoding.DecodeString(b64PublicKey)
	if err != nil {
		return "", fmt.Errorf("decode public key: %w", err)
	}
	if len(pub) != 32 {
		return "", fmt.Errorf("public key is %d bytes, want 32", len(pub))
	}
	var pk [32]byte
	copy(pk[:], pub)
	sealed, err := box.SealAnonymous(nil, []byte(plaintext), &pk, rand.Reader)
	if err != nil {
		return "", fmt.Errorf("seal: %w", err)
	}
	return base64.StdEncoding.EncodeToString(sealed), nil
}
