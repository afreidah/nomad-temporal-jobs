// -------------------------------------------------------------------------------
// GitHub Token Renewer Activities - Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Runs the activities in a TestActivityEnvironment with fakes for the githubClient
// and kvGetter consumer interfaces -- repo-list parsing, the mint-and-store
// path, and error handling -- with no real GitHub or Consul.
// -------------------------------------------------------------------------------

package activities

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"
)

type fakeGitHub struct {
	token    string
	expiry   time.Time
	mintErr  error
	setErr   error
	setCalls []string // "owner/repo:name=value"
}

func (f *fakeGitHub) MintWorkflowToken(_ context.Context, _, _ string) (string, time.Time, error) {
	return f.token, f.expiry, f.mintErr
}

func (f *fakeGitHub) SetRepoSecret(_ context.Context, owner, repo, name, value string) error {
	f.setCalls = append(f.setCalls, owner+"/"+repo+":"+name+"="+value)
	return f.setErr
}

type fakeRepos struct {
	val   []byte
	found bool
	err   error
}

func (f *fakeRepos) KVGet(_ context.Context, _ string) ([]byte, bool, error) {
	return f.val, f.found, f.err
}

func actEnv() *testsuite.TestActivityEnvironment {
	return (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
}

// --- ListRepos ---------------------------------------------------------------

func TestListRepos(t *testing.T) {
	a := New(Config{Repos: &fakeRepos{val: []byte("# repos\nafreidah/a\n\n  afreidah/b  \n"), found: true}})
	env := actEnv()
	env.RegisterActivity(a.ListRepos)

	val, err := env.ExecuteActivity(a.ListRepos)
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	var repos []string
	if err := val.Get(&repos); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(repos) != 2 || repos[0] != "afreidah/a" || repos[1] != "afreidah/b" {
		t.Errorf("repos = %v, want [afreidah/a afreidah/b]", repos)
	}
}

func TestListRepos_MissingKey(t *testing.T) {
	a := New(Config{Repos: &fakeRepos{found: false}})
	env := actEnv()
	env.RegisterActivity(a.ListRepos)
	if _, err := env.ExecuteActivity(a.ListRepos); err == nil {
		t.Fatal("expected an error when the repo-list key is absent")
	}
}

// --- RenewRepoToken ----------------------------------------------------------

func TestRenewRepoToken(t *testing.T) {
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	gh := &fakeGitHub{token: "ghs_minted", expiry: exp}
	a := New(Config{GitHub: gh, SecretName: "RELEASE_PAT"})
	env := actEnv()
	env.RegisterActivity(a.RenewRepoToken)

	val, err := env.ExecuteActivity(a.RenewRepoToken, "afreidah/myrepo")
	if err != nil {
		t.Fatalf("RenewRepoToken: %v", err)
	}
	var res RepoRenewResult
	if err := val.Get(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Repo != "afreidah/myrepo" {
		t.Errorf("Repo = %q, want afreidah/myrepo", res.Repo)
	}
	want := "afreidah/myrepo:RELEASE_PAT=ghs_minted"
	if len(gh.setCalls) != 1 || gh.setCalls[0] != want {
		t.Errorf("SetRepoSecret calls = %v, want [%s]", gh.setCalls, want)
	}
}

func TestRenewRepoToken_InvalidRepo(t *testing.T) {
	a := New(Config{GitHub: &fakeGitHub{}})
	env := actEnv()
	env.RegisterActivity(a.RenewRepoToken)
	if _, err := env.ExecuteActivity(a.RenewRepoToken, "no-slash"); err == nil {
		t.Fatal("expected an error for a repo without owner/name")
	}
}

func TestRenewRepoToken_MintFails(t *testing.T) {
	a := New(Config{GitHub: &fakeGitHub{mintErr: errors.New("github down")}})
	env := actEnv()
	env.RegisterActivity(a.RenewRepoToken)
	if _, err := env.ExecuteActivity(a.RenewRepoToken, "o/r"); err == nil {
		t.Fatal("expected an error when minting fails")
	}
}

func TestRenewRepoToken_SetFails(t *testing.T) {
	a := New(Config{GitHub: &fakeGitHub{token: "t", setErr: errors.New("forbidden")}})
	env := actEnv()
	env.RegisterActivity(a.RenewRepoToken)
	if _, err := env.ExecuteActivity(a.RenewRepoToken, "o/r"); err == nil {
		t.Fatal("expected an error when setting the secret fails")
	}
}
