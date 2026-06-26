// -------------------------------------------------------------------------------
// GitHub Token Renewer Activities - SonarCloud Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Runs RenewSonarCloudToken in a TestActivityEnvironment with a fake sonarClient
// (and the shared fakeGitHub): the mint-write-revoke path, project-key
// derivation, the unconfigured/invalid guards, and that revoking prior tokens is
// best-effort. No real SonarCloud or GitHub.
// -------------------------------------------------------------------------------

package activities

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeSonar struct {
	token     string
	mintErr   error
	listNames []string
	listErr   error
	revokeErr error

	mintCalls []string // token name
	revoked   []string
}

func (f *fakeSonar) MintToken(_ context.Context, name string, _ time.Time) (string, error) {
	f.mintCalls = append(f.mintCalls, name)
	return f.token, f.mintErr
}

func (f *fakeSonar) ListTokenNames(_ context.Context) ([]string, error) {
	return f.listNames, f.listErr
}

func (f *fakeSonar) RevokeToken(_ context.Context, name string) error {
	f.revoked = append(f.revoked, name)
	return f.revokeErr
}

func sonarConfig(gh *fakeGitHub, sc *fakeSonar) Config {
	return Config{
		GitHub:          gh,
		Sonar:           sc,
		SonarSecretName: "SONAR_TOKEN",
		SonarTokenTTL:   90 * 24 * time.Hour,
	}
}

func TestRenewSonarCloudToken(t *testing.T) {
	gh := &fakeGitHub{}
	sc := &fakeSonar{
		token:     "sq_minted",
		listNames: []string{"munchbox-ci/afreidah/myrepo/1", "munchbox-ci/afreidah/other/9", "user-token"},
	}
	a := New(sonarConfig(gh, sc))
	env := actEnv()
	env.RegisterActivity(a.RenewSonarCloudToken)

	val, err := env.ExecuteActivity(a.RenewSonarCloudToken, "afreidah/myrepo")
	if err != nil {
		t.Fatalf("RenewSonarCloudToken: %v", err)
	}
	var res SonarRenewResult
	if err := val.Get(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if res.Repo != "afreidah/myrepo" {
		t.Errorf("Repo = %q, want afreidah/myrepo", res.Repo)
	}
	// Minted one token under the repo's name prefix.
	if len(sc.mintCalls) != 1 || !strings.HasPrefix(sc.mintCalls[0], "munchbox-ci/afreidah/myrepo/") {
		t.Errorf("mint calls = %v, want one for afreidah/myrepo", sc.mintCalls)
	}
	// Wrote the minted token to the repo's SONAR_TOKEN secret.
	want := "afreidah/myrepo:SONAR_TOKEN=sq_minted"
	if len(gh.setCalls) != 1 || gh.setCalls[0] != want {
		t.Errorf("SetRepoSecret calls = %v, want [%s]", gh.setCalls, want)
	}
	// Revoked only this repo's prior token -- not other repos' or unrelated tokens.
	if len(sc.revoked) != 1 || sc.revoked[0] != "munchbox-ci/afreidah/myrepo/1" {
		t.Errorf("revoked = %v, want [munchbox-ci/afreidah/myrepo/1]", sc.revoked)
	}
}

func TestRenewSonarCloudToken_NotConfigured(t *testing.T) {
	a := New(Config{GitHub: &fakeGitHub{}}) // no Sonar client / org
	env := actEnv()
	env.RegisterActivity(a.RenewSonarCloudToken)
	if _, err := env.ExecuteActivity(a.RenewSonarCloudToken, "afreidah/myrepo"); err == nil {
		t.Fatal("expected an error when SonarCloud is not configured")
	}
}

func TestRenewSonarCloudToken_InvalidRepo(t *testing.T) {
	a := New(sonarConfig(&fakeGitHub{}, &fakeSonar{token: "t"}))
	env := actEnv()
	env.RegisterActivity(a.RenewSonarCloudToken)
	if _, err := env.ExecuteActivity(a.RenewSonarCloudToken, "no-slash"); err == nil {
		t.Fatal("expected an error for a repo without owner/name")
	}
}

func TestRenewSonarCloudToken_MintFails(t *testing.T) {
	a := New(sonarConfig(&fakeGitHub{}, &fakeSonar{mintErr: errors.New("sonar down")}))
	env := actEnv()
	env.RegisterActivity(a.RenewSonarCloudToken)
	if _, err := env.ExecuteActivity(a.RenewSonarCloudToken, "o/r"); err == nil {
		t.Fatal("expected an error when minting fails")
	}
}

func TestRenewSonarCloudToken_SetFails(t *testing.T) {
	gh := &fakeGitHub{setErr: errors.New("forbidden")}
	a := New(sonarConfig(gh, &fakeSonar{token: "t"}))
	env := actEnv()
	env.RegisterActivity(a.RenewSonarCloudToken)
	if _, err := env.ExecuteActivity(a.RenewSonarCloudToken, "o/r"); err == nil {
		t.Fatal("expected an error when setting the secret fails")
	}
}

// Revoking prior tokens is cleanup -- the new token is already live, so a revoke
// failure must not fail the renewal.
func TestRenewSonarCloudToken_RevokeIsBestEffort(t *testing.T) {
	gh := &fakeGitHub{}
	sc := &fakeSonar{token: "sq_x", listNames: []string{"munchbox-ci/o/r/1"}, revokeErr: errors.New("revoke boom")}
	a := New(sonarConfig(gh, sc))
	env := actEnv()
	env.RegisterActivity(a.RenewSonarCloudToken)

	if _, err := env.ExecuteActivity(a.RenewSonarCloudToken, "o/r"); err != nil {
		t.Fatalf("revoke failure should not fail renewal, got: %v", err)
	}
	if len(gh.setCalls) != 1 {
		t.Errorf("expected the secret to still be written, setCalls = %v", gh.setCalls)
	}
}

func TestSonarTokenNamePrefix(t *testing.T) {
	name := sonarTokenName("afreidah", "myrepo", time.Unix(1700000000, 0))
	if !strings.HasPrefix(name, sonarTokenPrefix("afreidah", "myrepo")) {
		t.Errorf("name %q lacks repo prefix %q", name, sonarTokenPrefix("afreidah", "myrepo"))
	}
	// The slash delimiter keeps one repo's prefix from matching a longer repo
	// name (slashes can't appear within an owner or repo segment).
	if strings.HasPrefix(sonarTokenName("afreidah", "my", time.Unix(1, 0)), sonarTokenPrefix("afreidah", "myrepo")) {
		t.Error("prefix of a shorter repo name should not match a longer repo's prefix")
	}
}
