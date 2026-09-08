// -------------------------------------------------------------------------------
// Runner Scaler Activities Tests - Forgejo Mode
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// forgejo-mode differs from the GitHub modes in two places that matter: the
// client is built per repo from the instance the config names, and a dispatched
// runner registers against that instance rather than github.com. These pin both,
// plus the errors raised when a forgejo-mode repo is configured incompletely.
// -------------------------------------------------------------------------------

package activities

import (
	"errors"
	"testing"

	"munchbox/temporal-workers/shared/client/git"
)

// fakeForgejo stands in for a Forgejo instance client. It records what it was
// constructed with so the tests can assert the config reached the constructor.
type fakeForgejo struct {
	fakeGitHub
	baseURL string
	token   string
}

// newForgejoActs builds an Activities whose NewForgejo records its arguments and
// returns fg.
func newForgejoActs(t *testing.T, vault fakeVault, nm *fakeNomad, fg *fakeForgejo) *Activities {
	t.Helper()
	return New(Config{
		KV:    fakeKV{},
		Vault: vault,
		Nomad: nm,
		NewForgejo: func(baseURL, token string) (githubApp, error) {
			fg.baseURL, fg.token = baseURL, token
			return fg, nil
		},
	})
}

// --- polling ------------------------------------------------------------------

func TestListQueuedJobs_ForgejoMode(t *testing.T) {
	fg := &fakeForgejo{
		jobs: []git.QueuedJob{{ID: 12728, Labels: []string{"ubuntu-latest"}}}}
	a := newForgejoActs(t,
		fakeVault{"kv/forgejo": {"token": "forge-api-token"}},
		&fakeNomad{}, fg)

	env := actEnv()
	env.RegisterActivity(a.ListQueuedJobs)

	val, err := env.ExecuteActivity(a.ListQueuedJobs, PollRepo{
		Repo:       "alex/munchbox",
		Mode:       ModeForgejo,
		VaultPath:  "kv/forgejo",
		ForgejoURL: "http://forgejo.service.consul:30028",
	})
	if err != nil {
		t.Fatalf("ListQueuedJobs forgejo: %v", err)
	}
	var jobs []git.QueuedJob
	if err := val.Get(&jobs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != 12728 {
		t.Errorf("jobs = %+v, want one job id 12728", jobs)
	}
	if fg.baseURL != "http://forgejo.service.consul:30028" {
		t.Errorf("client built for %q, want the configured instance", fg.baseURL)
	}
	if fg.token != "forge-api-token" {
		t.Errorf("client built with token %q, want forge-api-token", fg.token)
	}
}

func TestListQueuedJobs_ForgejoMode_MissingURL(t *testing.T) {
	a := newForgejoActs(t, fakeVault{"kv/forgejo": {"token": "t"}}, &fakeNomad{}, &fakeForgejo{})
	env := actEnv()
	env.RegisterActivity(a.ListQueuedJobs)

	_, err := env.ExecuteActivity(a.ListQueuedJobs, PollRepo{
		Repo: "alex/munchbox", Mode: ModeForgejo, VaultPath: "kv/forgejo",
	})
	if err == nil {
		t.Fatal("want an error for forgejo-mode with no forgejoUrl, got nil")
	}
}

func TestListQueuedJobs_ForgejoMode_MissingVaultPath(t *testing.T) {
	a := newForgejoActs(t, fakeVault{}, &fakeNomad{}, &fakeForgejo{})
	env := actEnv()
	env.RegisterActivity(a.ListQueuedJobs)

	_, err := env.ExecuteActivity(a.ListQueuedJobs, PollRepo{
		Repo: "alex/munchbox", Mode: ModeForgejo, ForgejoURL: "http://forge:3000",
	})
	if err == nil {
		t.Fatal("want an error for forgejo-mode with no vaultPath, got nil")
	}
}

func TestListQueuedJobs_ForgejoMode_UnreadableSecret(t *testing.T) {
	a := newForgejoActs(t, fakeVault{}, &fakeNomad{}, &fakeForgejo{})
	env := actEnv()
	env.RegisterActivity(a.ListQueuedJobs)

	_, err := env.ExecuteActivity(a.ListQueuedJobs, PollRepo{
		Repo: "alex/munchbox", Mode: ModeForgejo,
		VaultPath: "kv/absent", ForgejoURL: "http://forge:3000",
	})
	if err == nil {
		t.Fatal("want an error when the secret store has no such path, got nil")
	}
}

// --- dispatch -----------------------------------------------------------------

func TestDispatchRunner_ForgejoMode_RegistersAgainstTheInstance(t *testing.T) {
	nm := &fakeNomad{}
	fg := &fakeForgejo{fakeGitHub: fakeGitHub{token: "reg-token-xyz"}}
	a := newForgejoActs(t, fakeVault{"kv/forgejo": {"token": "forge-api-token"}}, nm, fg)

	env := actEnv()
	env.RegisterActivity(a.DispatchRunner)

	_, err := env.ExecuteActivity(a.DispatchRunner, DispatchSpec{
		Repo:       "alex/munchbox",
		Labels:     []string{"ops"},
		MintToken:  true,
		Mode:       ModeForgejo,
		ForgejoURL: "http://forgejo.service.consul:30028/",
		VaultPath:  "kv/forgejo",
	})
	if err != nil {
		t.Fatalf("DispatchRunner forgejo: %v", err)
	}

	// The runner registers against the forge that queued the work, not github.com.
	if got, want := nm.dispatchedMeta["repo_url"], "http://forgejo.service.consul:30028/alex/munchbox"; got != want {
		t.Errorf("repo_url = %q, want %q", got, want)
	}
	if got, want := nm.dispatchedMeta["runner_token"], "reg-token-xyz"; got != want {
		t.Errorf("runner_token = %q, want the token minted by the Forgejo client (%q)", got, want)
	}
}

func TestDispatchRunner_AppMode_StillUsesGitHub(t *testing.T) {
	// The forgejo branch must not divert app-mode: same config, no Mode set.
	nm := &fakeNomad{}
	a := New(Config{
		KV:     fakeKV{},
		Nomad:  nm,
		GitHub: &fakeGitHub{token: "gh-reg-token"},
	})
	env := actEnv()
	env.RegisterActivity(a.DispatchRunner)

	if _, err := env.ExecuteActivity(a.DispatchRunner, DispatchSpec{
		Repo: "octo/a", Labels: []string{"self-hosted"}, MintToken: true,
	}); err != nil {
		t.Fatalf("DispatchRunner app: %v", err)
	}
	if got, want := nm.dispatchedMeta["repo_url"], "https://github.com/octo/a"; got != want {
		t.Errorf("repo_url = %q, want %q", got, want)
	}
	if got, want := nm.dispatchedMeta["runner_token"], "gh-reg-token"; got != want {
		t.Errorf("runner_token = %q, want %q", got, want)
	}
}

func TestDispatchRunner_ForgejoMode_MintFailurePropagates(t *testing.T) {
	fg := &fakeForgejo{tokenErr: errors.New("forge refused")}
	a := newForgejoActs(t, fakeVault{"kv/forgejo": {"token": "t"}}, &fakeNomad{}, fg)

	env := actEnv()
	env.RegisterActivity(a.DispatchRunner)

	_, err := env.ExecuteActivity(a.DispatchRunner, DispatchSpec{
		Repo: "alex/munchbox", Labels: []string{"ops"}, MintToken: true,
		Mode: ModeForgejo, ForgejoURL: "http://forge:3000", VaultPath: "kv/forgejo",
	})
	if err == nil {
		t.Fatal("want the mint failure to surface, got nil")
	}
}

func TestDispatchRunner_ForgejoMode_ClientBuildFailurePropagates(t *testing.T) {
	a := New(Config{
		KV:    fakeKV{},
		Vault: fakeVault{"kv/forgejo": {"token": "t"}},
		Nomad: &fakeNomad{},
		NewForgejo: func(_, _ string) (githubApp, error) {
			return nil, errors.New("bad instance URL")
		},
	})
	env := actEnv()
	env.RegisterActivity(a.DispatchRunner)

	_, err := env.ExecuteActivity(a.DispatchRunner, DispatchSpec{
		Repo: "alex/munchbox", Labels: []string{"ops"}, MintToken: true,
		Mode: ModeForgejo, ForgejoURL: "http://forge:3000", VaultPath: "kv/forgejo",
	})
	if err == nil {
		t.Fatal("want the client build failure to surface, got nil")
	}
}

// --- constructor default ------------------------------------------------------

func TestNew_DefaultsNewForgejo(t *testing.T) {
	a := New(Config{KV: fakeKV{}, Nomad: &fakeNomad{}})
	if a.cfg.NewForgejo == nil {
		t.Fatal("NewForgejo left nil; New must supply the real constructor")
	}
	if _, err := a.cfg.NewForgejo("http://forge:3000", "tok"); err != nil {
		t.Errorf("default NewForgejo: %v", err)
	}
	if _, err := a.cfg.NewForgejo("", ""); err == nil {
		t.Error("default NewForgejo accepted empty arguments, want an error")
	}
}
