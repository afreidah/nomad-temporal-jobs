// -------------------------------------------------------------------------------
// Shared SonarCloud Client - Project Analysis Tokens
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// SonarCloud (SonarQube Cloud) has no official Go SDK, so this is a thin native
// HTTP client over the handful of user-token endpoints we need: mint a
// project-scoped analysis token (sqp_...), revoke one, and list the
// authenticated user's token names. One master user token -- held in Vault, with
// Execute Analysis permission on the projects -- authenticates every call; the
// token-renewer worker uses this to mint a fresh per-project token for each
// managed repo and write it to that repo's SONAR_TOKEN secret, mirroring how the
// GitHub App client renews CI tokens.
// -------------------------------------------------------------------------------

package shared

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultSonarCloudBaseURL is the public SonarCloud API host. A self-hosted
// SonarQube server can be targeted by overriding BaseURL.
const defaultSonarCloudBaseURL = "https://sonarcloud.io"

// projectAnalysisTokenType is the SonarCloud token type scoped to a single
// project: a leak only grants analysis of that one project.
const projectAnalysisTokenType = "PROJECT_ANALYSIS_TOKEN"

// SonarCloudConfig configures a SonarCloud client. Token is the master user
// token (Execute Analysis permission) used to authenticate every request.
// BaseURL is optional and defaults to https://sonarcloud.io.
type SonarCloudConfig struct {
	Token   string
	BaseURL string
}

// SonarCloud is a SonarCloud API client. It mints and revokes project analysis
// tokens. Construct it with NewSonarCloud; workers consume it through their own
// narrow interfaces.
type SonarCloud struct {
	base  string
	token string
	http  *http.Client
}

// NewSonarCloud builds a client from cfg. The HTTP transport is
// OTel-instrumented so SonarCloud calls appear as edges in the service graph.
func NewSonarCloud(cfg SonarCloudConfig) *SonarCloud {
	base := cfg.BaseURL
	if base == "" {
		base = defaultSonarCloudBaseURL
	}
	return &SonarCloud{
		base:  strings.TrimRight(base, "/"),
		token: cfg.Token,
		http:  &http.Client{Transport: otelTransport("sonarcloud", nil)},
	}
}

// generateResponse is the subset of api/user_tokens/generate we read. The token
// value is only ever returned here, at creation.
type generateResponse struct {
	Token string `json:"token"`
}

// searchResponse is the subset of api/user_tokens/search we read.
type searchResponse struct {
	UserTokens []struct {
		Name string `json:"name"`
	} `json:"userTokens"`
}

// MintProjectToken creates a PROJECT_ANALYSIS_TOKEN named name, scoped to
// projectKey, and returns its value (returned only at creation). A non-zero
// expiry sets an expiration date (day granularity, the API's resolution); a zero
// expiry mints a non-expiring token. The name must be unique for the
// authenticated user -- minting a name that already exists fails.
func (s *SonarCloud) MintProjectToken(ctx context.Context, projectKey, name string, expiry time.Time) (string, error) {
	form := url.Values{
		"name":       {name},
		"type":       {projectAnalysisTokenType},
		"projectKey": {projectKey},
	}
	if !expiry.IsZero() {
		form.Set("expirationDate", expiry.UTC().Format("2006-01-02"))
	}

	var out generateResponse
	if err := s.post(ctx, "/api/user_tokens/generate", form, &out); err != nil {
		return "", fmt.Errorf("mint project token %q for %s: %w", name, projectKey, err)
	}
	if out.Token == "" {
		return "", fmt.Errorf("mint project token %q for %s: empty token in response", name, projectKey)
	}
	return out.Token, nil
}

// RevokeToken revokes the named token. Revoking a name that does not exist is a
// no-op on SonarCloud's side and returns no error.
func (s *SonarCloud) RevokeToken(ctx context.Context, name string) error {
	if err := s.post(ctx, "/api/user_tokens/revoke", url.Values{"name": {name}}, nil); err != nil {
		return fmt.Errorf("revoke token %q: %w", name, err)
	}
	return nil
}

// ListTokenNames returns the names of every token owned by the authenticated
// user. The renewer uses it to find and revoke a project's prior tokens after
// minting the replacement.
func (s *SonarCloud) ListTokenNames(ctx context.Context) ([]string, error) {
	var out searchResponse
	if err := s.get(ctx, "/api/user_tokens/search", &out); err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	names := make([]string, len(out.UserTokens))
	for i, t := range out.UserTokens {
		names[i] = t.Name
	}
	return names, nil
}

// post sends a form-encoded POST authenticated with the master token and decodes
// a JSON body into out (out may be nil to discard the body).
func (s *SonarCloud) post(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return s.do(req, out)
}

// get sends a GET authenticated with the master token and decodes a JSON body
// into out.
func (s *SonarCloud) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+path, nil)
	if err != nil {
		return err
	}
	return s.do(req, out)
}

// do authenticates req with the master token (HTTP Basic, token as username and
// an empty password, per the SonarCloud convention), sends it, and decodes a
// non-empty 2xx body into out. Non-2xx responses surface the status and body.
func (s *SonarCloud) do(req *http.Request, out any) error {
	req.SetBasicAuth(s.token, "")

	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("sonarcloud %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
