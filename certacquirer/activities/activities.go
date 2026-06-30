// -------------------------------------------------------------------------------
// Cert Acquirer Activities - Wildcard Issuance and Vault Publish
//
// Author: Alex Freidah
//
// Temporal activities that issue the *.munchbox.cc wildcard certificate via
// ACME DNS-01 (Cloudflare) using the go-acme/lego library, and publish the
// result to Vault. The ACME account is persisted to Vault so registration
// happens once rather than on every run. Issuance and publish are separate
// activities: the issued cert+key are written to a staging path so a publish
// failure never re-runs ACME issuance (Let's Encrypt rate limits), and the
// private key never transits Temporal workflow history.
// -------------------------------------------------------------------------------

package activities

import (
	"context"
	"crypto"
	"encoding/json"
	"errors"
	"fmt"
	"munchbox/temporal-workers/shared"
	"strings"

	"github.com/go-acme/lego/v4/acme"
	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

// -------------------------------------------------------------------------
// ERRORS
// -------------------------------------------------------------------------

// errRateLimited classifies a Let's Encrypt rate-limit response as permanent
// for a single run so Temporal does not hammer the ACME endpoint.
const errRateLimited = "ACMERateLimited"

// -------------------------------------------------------------------------
// CONFIG AND CONSTRUCTOR
// -------------------------------------------------------------------------

// vaultKV is the narrow Vault surface the cert activities use, declared here
// so tests can substitute a fake. *vault.VaultClient satisfies it.
type vaultKV interface {
	ReadKV(ctx context.Context, path string) (map[string]any, error)
	ReadKVMaybe(ctx context.Context, path string) (map[string]any, bool, error)
	ReadKVField(ctx context.Context, path, field string) (string, error)
	WriteKV(ctx context.Context, path string, data map[string]any) error
}

// Config holds the static dependencies and Vault locations for the cert
// activities. Per-run values (domains, email) arrive as activity arguments.
type Config struct {
	Vault vaultKV

	// CADirURL is the ACME directory endpoint (Let's Encrypt production by
	// default; point at staging for testing to avoid rate limits).
	CADirURL string

	// Vault KV paths (under the client's mount).
	CFTokenPath  string // holds the Cloudflare DNS API token
	CFTokenField string
	AccountPath  string // persisted ACME account (key + registration + email)
	StagingPath  string // freshly issued cert+key, pre-publish
	PublishPath  string // the path Traefik reads (secret/traefik/wildcard)
}

// Activities implements the cert-acquirer Temporal activities.
type Activities struct {
	cfg Config
	// newIssuer builds the ACME issuer for an account. Defaults to the lego-backed
	// newLegoIssuer (acme.go); tests substitute a fake to cover IssueWildcardCert.
	newIssuer func(ctx context.Context, user *acmeUser) (certIssuer, error)
}

// New constructs the activity set. The Vault client and CA URL are required;
// empty Vault paths fall back to the cluster defaults.
func New(cfg Config) *Activities {
	if cfg.CFTokenPath == "" {
		cfg.CFTokenPath = "cloudflare-wandns"
	}
	if cfg.CFTokenField == "" {
		cfg.CFTokenField = "api_token"
	}
	if cfg.AccountPath == "" {
		cfg.AccountPath = "traefik/acme-account"
	}
	if cfg.StagingPath == "" {
		cfg.StagingPath = "traefik/wildcard-staging"
	}
	if cfg.PublishPath == "" {
		cfg.PublishPath = "traefik/wildcard"
	}
	if cfg.CADirURL == "" {
		cfg.CADirURL = lego.LEDirectoryProduction
	}
	a := &Activities{cfg: cfg}
	a.newIssuer = a.newLegoIssuer
	return a
}

// -------------------------------------------------------------------------
// ACME ACCOUNT
// -------------------------------------------------------------------------

// acmeUser carries the ACME account identity lego requires.
type acmeUser struct {
	email        string
	key          crypto.PrivateKey
	registration *registration.Resource
}

func (u *acmeUser) GetEmail() string                        { return u.email }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.key }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.registration }

// persistedAccount is the JSON shape stored in Vault for the ACME account.
type persistedAccount struct {
	Email        string                 `json:"email"`
	KeyPEM       string                 `json:"key_pem"`
	Registration *registration.Resource `json:"registration"`
}

// loadAccount reads the persisted ACME account from Vault, returning a nil
// user (not an error) when no account exists yet.
func (a *Activities) loadAccount(ctx context.Context) (*acmeUser, error) {
	data, found, err := a.cfg.Vault.ReadKVMaybe(ctx, a.cfg.AccountPath)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}

	raw, ok := data["account"].(string)
	if !ok || raw == "" {
		return nil, nil
	}

	var acc persistedAccount
	if err := json.Unmarshal([]byte(raw), &acc); err != nil {
		return nil, fmt.Errorf("decode persisted acme account: %w", err)
	}
	key, err := certcrypto.ParsePEMPrivateKey([]byte(acc.KeyPEM))
	if err != nil {
		return nil, fmt.Errorf("parse acme account key: %w", err)
	}
	return &acmeUser{email: acc.Email, key: key, registration: acc.Registration}, nil
}

// saveAccount persists the ACME account (key + registration) to Vault.
func (a *Activities) saveAccount(ctx context.Context, user *acmeUser) error {
	pem := certcrypto.PEMEncode(user.key)
	acc := persistedAccount{Email: user.email, KeyPEM: string(pem), Registration: user.registration}
	body, err := json.Marshal(acc)
	if err != nil {
		return fmt.Errorf("encode acme account: %w", err)
	}
	return a.cfg.Vault.WriteKV(ctx, a.cfg.AccountPath, map[string]any{"account": string(body)})
}

// -------------------------------------------------------------------------
// ISSUE
// -------------------------------------------------------------------------

// IssueRequest is the per-run input to IssueWildcardCert.
type IssueRequest struct {
	Domains []string `json:"domains"`
	Email   string   `json:"email"`
}

// certIssuer is the narrow ACME surface IssueWildcardCert drives: register the
// account and obtain the certificate. The lego-backed implementation is in
// acme.go (the untestable I/O); tests substitute a fake via Activities.newIssuer.
type certIssuer interface {
	Register(ctx context.Context) (*registration.Resource, error)
	Obtain(ctx context.Context, domains []string) (certPEM, keyPEM []byte, err error)
}

// IssueWildcardCert ensures the ACME account, obtains the wildcard via the
// configured issuer, and writes the cert+key to the staging Vault path. A
// rate-limit response is returned non-retryable so a single run stops hammering
// Let's Encrypt.
func (a *Activities) IssueWildcardCert(ctx context.Context, req IssueRequest) error {
	logger := activity.GetLogger(ctx)

	ctx, span := shared.StartPeerSpan(ctx, "acme", "acme.issue")
	defer span.End()

	user, isNew, err := a.ensureUser(ctx, req.Email)
	if err != nil {
		return err
	}

	issuer, err := a.newIssuer(ctx, user)
	if err != nil {
		return err
	}

	if isNew {
		reg, rerr := issuer.Register(ctx)
		if rerr != nil {
			return classifyACME(fmt.Errorf("register acme account: %w", rerr))
		}
		user.registration = reg
		if serr := a.saveAccount(ctx, user); serr != nil {
			return serr
		}
		logger.Info("Registered new ACME account", "email", req.Email)
	}

	certPEM, keyPEM, err := issuer.Obtain(ctx, req.Domains)
	if err != nil {
		return classifyACME(fmt.Errorf("obtain certificate: %w", err))
	}

	if werr := a.cfg.Vault.WriteKV(ctx, a.cfg.StagingPath, map[string]any{
		"cert": string(certPEM),
		"key":  string(keyPEM),
	}); werr != nil {
		return werr
	}
	logger.Info("Issued wildcard certificate to staging", "domains", req.Domains, "staging_path", a.cfg.StagingPath)
	return nil
}

// ensureUser loads the persisted ACME account or, on first run, generates a
// fresh EC256 key for a new one. isNew reports whether the account still needs
// registration.
func (a *Activities) ensureUser(ctx context.Context, email string) (user *acmeUser, isNew bool, err error) {
	user, err = a.loadAccount(ctx)
	if err != nil {
		return nil, false, err
	}
	if user != nil {
		return user, false, nil
	}
	key, err := certcrypto.GeneratePrivateKey(certcrypto.EC256)
	if err != nil {
		return nil, false, fmt.Errorf("generate acme account key: %w", err)
	}
	return &acmeUser{email: email, key: key}, true, nil
}

// -------------------------------------------------------------------------
// PUBLISH
// -------------------------------------------------------------------------

// PublishWildcardCert promotes the staged cert+key to the published Vault path
// that Traefik reads. Kept separate from issuance so a publish retry never
// re-runs ACME.
func (a *Activities) PublishWildcardCert(ctx context.Context) error {
	logger := activity.GetLogger(ctx)

	staged, err := a.cfg.Vault.ReadKV(ctx, a.cfg.StagingPath)
	if err != nil {
		return fmt.Errorf("read staged cert: %w", err)
	}
	cert, _ := staged["cert"].(string)
	key, _ := staged["key"].(string)
	if cert == "" || key == "" {
		return temporal.NewNonRetryableApplicationError("staged cert or key missing", "StagedCertMissing", nil)
	}

	if err := a.cfg.Vault.WriteKV(ctx, a.cfg.PublishPath, map[string]any{"cert": cert, "key": key}); err != nil {
		return fmt.Errorf("publish cert: %w", err)
	}
	logger.Info("Published wildcard certificate", "publish_path", a.cfg.PublishPath)
	return nil
}

// classifyACME marks Let's Encrypt rate-limit errors non-retryable so a run
// does not keep retrying against the limit; other errors stay retryable.
func classifyACME(err error) error {
	if err == nil {
		return nil
	}
	if isRateLimit(err) {
		return temporal.NewNonRetryableApplicationError(err.Error(), errRateLimited, err)
	}
	return err
}

// isRateLimit reports whether err is an ACME rate-limit response.
func isRateLimit(err error) bool {
	if prob, ok := errors.AsType[*acme.ProblemDetails](err); ok {
		return prob.Type == "urn:ietf:params:acme:error:rateLimited"
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "ratelimited") || strings.Contains(msg, "rate limit") || strings.Contains(msg, "too many")
}
