// -------------------------------------------------------------------------------
// Runner Scaler Activities - Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Runs the activities in a TestActivityEnvironment with in-memory fakes for the
// githubApp, githubLister, kvGetter, secretReader, and jobDispatcher consumer
// interfaces: per-repo config parsing from Consul KV (including missing/malformed
// keys), queued-job discovery in both app-mode (App client) and vault-mode (PAT
// read from the secret store), the dispatch meta the runner job receives (token
// minted only when the mode asks, image carried only when a profile sets one),
// active-runner counting across multiple dispatch jobs, and the reaper tolerating
// an already-gone job.
// -------------------------------------------------------------------------------

package activities

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"

	"munchbox/temporal-workers/shared/client/git"
	"munchbox/temporal-workers/shared/client/nomad"
)

// --- fakes ---

type fakeKV map[string][]byte

func (f fakeKV) KVGet(_ context.Context, key string) ([]byte, bool, error) {
	v, ok := f[key]
	return v, ok, nil
}

type fakeGitHub struct {
	jobs     []git.QueuedJob
	token    string
	tokenErr error
}

func (f *fakeGitHub) ListQueuedSelfHostedJobs(_ context.Context, _, _ string) ([]git.QueuedJob, error) {
	return f.jobs, nil
}

func (f *fakeGitHub) CreateRunnerRegistrationToken(_ context.Context, _, _ string) (string, time.Time, error) {
	return f.token, time.Now().Add(time.Hour), f.tokenErr
}

// fakeVault is the secret-store fake for vault-mode: path -> fields.
type fakeVault map[string]map[string]any

func (f fakeVault) ReadKV(_ context.Context, path string) (map[string]any, error) {
	v, ok := f[path]
	if !ok {
		return nil, errors.New("no secret at " + path)
	}
	return v, nil
}

// fakeLister is the PAT-built job lister returned by a fake NewPATLister.
type fakeLister struct {
	jobs  []git.QueuedJob
	token string // the token it was constructed with, for assertions
}

func (f *fakeLister) ListQueuedSelfHostedJobs(_ context.Context, _, _ string) ([]git.QueuedJob, error) {
	return f.jobs, nil
}

type fakeNomad struct {
	dispatchedMeta map[string]string
	dispatchedJob  string
	stopped        []string
	stopErr        error
	slots          []nomad.RunnerSlot
	slotsByJob     map[string][]nomad.RunnerSlot // per-parent-job slots; falls back to slots
	slotsErr       error
	dispatchErr    error
	runnerTerminal func() (bool, error)
}

func (f *fakeNomad) RunnerTerminal(_ context.Context, _ string) (bool, error) {
	if f.runnerTerminal != nil {
		return f.runnerTerminal()
	}
	return true, nil
}

func (f *fakeNomad) DispatchJob(_ context.Context, jobID string, meta map[string]string) (string, error) {
	f.dispatchedJob = jobID
	f.dispatchedMeta = meta
	if f.dispatchErr != nil {
		return "", f.dispatchErr
	}
	return jobID + "/dispatch-1-abc", nil
}

func (f *fakeNomad) StopJob(_ context.Context, jobID string) error {
	f.stopped = append(f.stopped, jobID)
	return f.stopErr
}

func (f *fakeNomad) ActiveRunnerSlots(_ context.Context, parentJobID string) ([]nomad.RunnerSlot, error) {
	if f.slotsErr != nil {
		return nil, f.slotsErr
	}
	if f.slotsByJob != nil {
		return f.slotsByJob[parentJobID], nil
	}
	return f.slots, nil
}

func actEnv() *testsuite.TestActivityEnvironment {
	return (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
}

func newActs(kv fakeKV, gh *fakeGitHub, nm *fakeNomad) *Activities {
	return New(Config{GitHub: gh, KV: kv, Nomad: nm})
}

// --- LoadConfig --------------------------------------------------------------

func TestLoadConfig(t *testing.T) {
	raw := []byte(`{
		"octo/a": {"mode": "app"},
		"octo/b": {"mode": "vault", "vaultPath": "kv/octo-pat", "profiles": [{"label":"vm","job":"vm-runner"},{"label":"custom","job":"std-runner"}]}
	}`)
	a := newActs(fakeKV{"runners/config": raw}, nil, nil)
	env := actEnv()
	env.RegisterActivity(a.LoadConfig)

	val, err := env.ExecuteActivity(a.LoadConfig)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	var cfg map[string]RepoConfig
	if err := val.Get(&cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg["octo/a"].Mode != "app" {
		t.Errorf("octo/a mode = %q, want app", cfg["octo/a"].Mode)
	}
	vault := cfg["octo/b"]
	if vault.Mode != ModeVault || vault.VaultPath != "kv/octo-pat" {
		t.Errorf("octo/b = %+v, want vault/kv/octo-pat", vault)
	}
	if len(vault.Profiles) != 2 || vault.Profiles[0].Label != "vm" || vault.Profiles[0].Job != "vm-runner" {
		t.Errorf("octo/b profiles = %+v, want ordered vm-first", vault.Profiles)
	}
}

func TestLoadConfig_MissingKeyIsNonRetryable(t *testing.T) {
	a := newActs(fakeKV{}, nil, nil)
	env := actEnv()
	env.RegisterActivity(a.LoadConfig)
	if _, err := env.ExecuteActivity(a.LoadConfig); err == nil {
		t.Fatal("expected an error for a missing config key")
	}
}

func TestLoadConfig_MalformedIsNonRetryable(t *testing.T) {
	a := newActs(fakeKV{"runners/config": []byte("not json")}, nil, nil)
	env := actEnv()
	env.RegisterActivity(a.LoadConfig)
	if _, err := env.ExecuteActivity(a.LoadConfig); err == nil {
		t.Fatal("expected an error for malformed config JSON")
	}
}

// --- ListQueuedJobs ----------------------------------------------------------

func TestListQueuedJobs_AppMode(t *testing.T) {
	gh := &fakeGitHub{jobs: []git.QueuedJob{{ID: 7, RunID: 1, Name: "build", Labels: []string{"self-hosted"}}}}
	a := newActs(fakeKV{}, gh, nil)
	env := actEnv()
	env.RegisterActivity(a.ListQueuedJobs)

	val, err := env.ExecuteActivity(a.ListQueuedJobs, PollRepo{Repo: "octo/widget"})
	if err != nil {
		t.Fatalf("ListQueuedJobs: %v", err)
	}
	var jobs []git.QueuedJob
	if err := val.Get(&jobs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != 7 {
		t.Errorf("jobs = %+v, want one job id 7", jobs)
	}
}

func TestListQueuedJobs_VaultMode(t *testing.T) {
	// vault-mode reads the token from the secret store and polls with a PAT
	// lister, never touching the App client (gh is nil here).
	var gotToken string
	a := New(Config{
		KV:    fakeKV{},
		Vault: fakeVault{"kv/octo-pat": {"token": "pat-secret", "repo_url": "https://github.com/octo/private"}},
		Nomad: &fakeNomad{},
		NewPATLister: func(token string) (githubLister, error) {
			gotToken = token
			return &fakeLister{jobs: []git.QueuedJob{{ID: 9, Labels: []string{"self-hosted", "custom"}}}, token: token}, nil
		},
	})
	env := actEnv()
	env.RegisterActivity(a.ListQueuedJobs)

	val, err := env.ExecuteActivity(a.ListQueuedJobs, PollRepo{Repo: "octo/private", Mode: ModeVault, VaultPath: "kv/octo-pat"})
	if err != nil {
		t.Fatalf("ListQueuedJobs vault: %v", err)
	}
	var jobs []git.QueuedJob
	if err := val.Get(&jobs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != 9 {
		t.Errorf("jobs = %+v, want one job id 9", jobs)
	}
	if gotToken != "pat-secret" {
		t.Errorf("PAT lister built with token %q, want pat-secret", gotToken)
	}
}

func TestListQueuedJobs_VaultModeMissingPath(t *testing.T) {
	a := New(Config{KV: fakeKV{}, Vault: fakeVault{}, Nomad: &fakeNomad{}})
	env := actEnv()
	env.RegisterActivity(a.ListQueuedJobs)
	if _, err := env.ExecuteActivity(a.ListQueuedJobs, PollRepo{Repo: "octo/private", Mode: ModeVault}); err == nil {
		t.Fatal("expected an error for vault-mode with no vaultPath")
	}
}

func TestListQueuedJobs_VaultModeMissingToken(t *testing.T) {
	a := New(Config{
		KV:    fakeKV{},
		Vault: fakeVault{"kv/octo-pat": {"repo_url": "https://github.com/octo/private"}}, // no token field
		Nomad: &fakeNomad{},
	})
	env := actEnv()
	env.RegisterActivity(a.ListQueuedJobs)
	if _, err := env.ExecuteActivity(a.ListQueuedJobs, PollRepo{Repo: "octo/private", Mode: ModeVault, VaultPath: "kv/octo-pat"}); err == nil {
		t.Fatal("expected an error when the secret has no token field")
	}
}

func TestListQueuedJobs_InvalidRepo(t *testing.T) {
	a := newActs(fakeKV{}, &fakeGitHub{}, nil)
	env := actEnv()
	env.RegisterActivity(a.ListQueuedJobs)
	if _, err := env.ExecuteActivity(a.ListQueuedJobs, PollRepo{Repo: "no-slash"}); err == nil {
		t.Fatal("expected an error for an unparseable repo")
	}
}

// --- DispatchRunner ----------------------------------------------------------

func TestDispatchRunner_AppModeMintsAndBuildsMeta(t *testing.T) {
	gh := &fakeGitHub{token: "ARRT_reg"}
	nm := &fakeNomad{}
	a := newActs(fakeKV{}, gh, nm)
	env := actEnv()
	env.RegisterActivity(a.DispatchRunner)

	val, err := env.ExecuteActivity(a.DispatchRunner, DispatchSpec{
		Repo:      "octo/widget",
		Labels:    []string{"self-hosted", "amd64"},
		Image:     "reg/ci-amd64:latest",
		MintToken: true,
	})
	if err != nil {
		t.Fatalf("DispatchRunner: %v", err)
	}
	var id string
	if err := val.Get(&id); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if id != "ci-runner/dispatch-1-abc" {
		t.Errorf("dispatched id = %q", id)
	}
	if nm.dispatchedJob != "ci-runner" {
		t.Errorf("dispatched job = %q, want ci-runner (default)", nm.dispatchedJob)
	}
	want := map[string]string{
		"repo_url":     "https://github.com/octo/widget",
		"runner_token": "ARRT_reg",
		"labels":       "self-hosted,amd64",
		"runner_image": "reg/ci-amd64:latest",
	}
	for k, v := range want {
		if nm.dispatchedMeta[k] != v {
			t.Errorf("meta[%q] = %q, want %q", k, nm.dispatchedMeta[k], v)
		}
	}
}

func TestDispatchRunner_VaultModeSkipsMintAndUsesProfileJob(t *testing.T) {
	// vault-mode: no token is minted (gh is nil, so any mint attempt would panic),
	// the profile job is dispatched, and repo_url + labels meta are still carried
	// so the runner buckets in CountActiveRunners.
	nm := &fakeNomad{}
	a := newActs(fakeKV{}, nil, nm)
	env := actEnv()
	env.RegisterActivity(a.DispatchRunner)

	if _, err := env.ExecuteActivity(a.DispatchRunner, DispatchSpec{
		Repo:        "octo/private",
		Labels:      []string{"self-hosted", "go"},
		Job:         "vault-runner",
		MintToken:   false,
		VaultSecret: "kv/octo-pat",
	}); err != nil {
		t.Fatalf("DispatchRunner vault: %v", err)
	}
	if nm.dispatchedJob != "vault-runner" {
		t.Errorf("dispatched job = %q, want vault-runner", nm.dispatchedJob)
	}
	if _, ok := nm.dispatchedMeta["runner_token"]; ok {
		t.Error("runner_token meta must be absent in vault-mode (job self-registers)")
	}
	if nm.dispatchedMeta["runner_secret"] != "kv/octo-pat" {
		t.Errorf("runner_secret meta = %q, want kv/octo-pat (job reads its own PAT)", nm.dispatchedMeta["runner_secret"])
	}
	if nm.dispatchedMeta["repo_url"] != "https://github.com/octo/private" || nm.dispatchedMeta["labels"] != "self-hosted,go" {
		t.Errorf("bookkeeping meta = %+v, want repo_url + labels set", nm.dispatchedMeta)
	}
}

func TestDispatchRunner_OmitsImageWhenUnset(t *testing.T) {
	nm := &fakeNomad{}
	a := newActs(fakeKV{}, &fakeGitHub{token: "t"}, nm)
	env := actEnv()
	env.RegisterActivity(a.DispatchRunner)

	if _, err := env.ExecuteActivity(a.DispatchRunner, DispatchSpec{
		Repo:      "octo/widget",
		Labels:    []string{"self-hosted"},
		MintToken: true,
	}); err != nil {
		t.Fatalf("DispatchRunner: %v", err)
	}
	if _, ok := nm.dispatchedMeta["runner_image"]; ok {
		t.Error("runner_image meta should be absent when the profile sets no image")
	}
}

func TestDispatchRunner_TokenError(t *testing.T) {
	a := newActs(fakeKV{}, &fakeGitHub{tokenErr: errors.New("422 forbidden")}, &fakeNomad{})
	env := actEnv()
	env.RegisterActivity(a.DispatchRunner)
	if _, err := env.ExecuteActivity(a.DispatchRunner, DispatchSpec{
		Repo:      "octo/widget",
		Labels:    []string{"self-hosted"},
		MintToken: true,
	}); err == nil {
		t.Fatal("expected an error when registration-token minting fails")
	}
}

func TestDispatchRunner_DispatchError(t *testing.T) {
	a := newActs(fakeKV{}, &fakeGitHub{token: "t"}, &fakeNomad{dispatchErr: errors.New("403 permission denied")})
	env := actEnv()
	env.RegisterActivity(a.DispatchRunner)
	if _, err := env.ExecuteActivity(a.DispatchRunner, DispatchSpec{
		Repo:      "octo/widget",
		Labels:    []string{"self-hosted"},
		MintToken: true,
	}); err == nil {
		t.Fatal("expected an error when the Nomad dispatch fails")
	}
}

func TestDispatchRunner_InvalidRepo(t *testing.T) {
	a := newActs(fakeKV{}, &fakeGitHub{}, &fakeNomad{})
	env := actEnv()
	env.RegisterActivity(a.DispatchRunner)
	if _, err := env.ExecuteActivity(a.DispatchRunner, DispatchSpec{
		Repo:   "no-slash",
		Labels: []string{"self-hosted"},
	}); err == nil {
		t.Fatal("expected an error dispatching for an unparseable repo")
	}
}

// --- ReapRunner --------------------------------------------------------------

func TestReapRunner_ToleratesMissingJob(t *testing.T) {
	// IsJobNotFound matches the "job not found" message (its string fallback), so
	// a reaper hitting an already-gone job succeeds.
	nm := &fakeNomad{stopErr: errors.New("job not found")}
	a := newActs(fakeKV{}, nil, nm)
	env := actEnv()
	env.RegisterActivity(a.ReapRunner)
	if _, err := env.ExecuteActivity(a.ReapRunner, "ci-runner/dispatch-1-abc"); err != nil {
		t.Fatalf("ReapRunner should tolerate a missing job: %v", err)
	}
}

func TestReapRunner_PropagatesRealError(t *testing.T) {
	nm := &fakeNomad{stopErr: errors.New("boom")}
	a := newActs(fakeKV{}, nil, nm)
	env := actEnv()
	env.RegisterActivity(a.ReapRunner)
	if _, err := env.ExecuteActivity(a.ReapRunner, "x"); err == nil {
		t.Fatal("expected ReapRunner to propagate a non-not-found error")
	}
}

// --- WaitRunnerDone ----------------------------------------------------------

func TestWaitRunnerDone(t *testing.T) {
	// Shorten the poll so the loop runs fast under test.
	old := waitPollInterval
	waitPollInterval = time.Millisecond
	defer func() { waitPollInterval = old }()

	calls := 0
	nm := &fakeNomad{runnerTerminal: func() (bool, error) {
		calls++
		switch calls {
		case 1:
			return false, errors.New("nomad blip") // transient: skipped + retried
		case 2:
			return false, nil // scheduled but still running
		default:
			return true, nil // finished
		}
	}}
	a := newActs(fakeKV{}, nil, nm)
	env := actEnv()
	env.RegisterActivity(a.WaitRunnerDone)

	if _, err := env.ExecuteActivity(a.WaitRunnerDone, "ci-runner/dispatch-1-abc"); err != nil {
		t.Fatalf("WaitRunnerDone: %v", err)
	}
	if calls < 3 {
		t.Errorf("polled %d times, want >=3 (skip blip -> not done -> done)", calls)
	}
}

// --- CountActiveRunners ------------------------------------------------------

func TestCountActiveRunners(t *testing.T) {
	// Active runners bucket by (repo, labels); label order is normalized so a
	// runner's dispatch meta lands in the same bucket as the matching job.
	nm := &fakeNomad{slots: []nomad.RunnerSlot{
		{RepoURL: "https://github.com/octo/a", Labels: "self-hosted"},
		{RepoURL: "https://github.com/octo/a", Labels: "self-hosted"},
		{RepoURL: "https://github.com/octo/a", Labels: "self-hosted,amd64"},
		{RepoURL: "https://github.com/octo/b", Labels: "self-hosted"},
		{RepoURL: "https://github.com/octo/c", Labels: ""}, // no-label runner -> "octo/c|" bucket
	}}
	a := newActs(fakeKV{}, nil, nm)
	env := actEnv()
	env.RegisterActivity(a.CountActiveRunners)

	val, err := env.ExecuteActivity(a.CountActiveRunners, []string{})
	if err != nil {
		t.Fatalf("CountActiveRunners: %v", err)
	}
	var counts map[string]int
	if err := val.Get(&counts); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if counts["octo/a|self-hosted"] != 2 {
		t.Errorf("octo/a self-hosted = %d, want 2", counts["octo/a|self-hosted"])
	}
	if counts["octo/a|amd64,self-hosted"] != 1 {
		t.Errorf("octo/a amd64 bucket = %d, want 1 (labels sorted)", counts["octo/a|amd64,self-hosted"])
	}
	if counts["octo/b|self-hosted"] != 1 {
		t.Errorf("octo/b self-hosted = %d, want 1", counts["octo/b|self-hosted"])
	}
	if counts["octo/c|"] != 1 {
		t.Errorf("octo/c no-label bucket = %d, want 1", counts["octo/c|"])
	}
}

func TestCountActiveRunners_UnionsAcrossJobs(t *testing.T) {
	// A vault-mode runner comes from its own parent job; counting must span the
	// default job and every extra job, or the extra-job bucket reads active = 0
	// and over-provisions. Passing a duplicate extra job must not double-count.
	nm := &fakeNomad{slotsByJob: map[string][]nomad.RunnerSlot{
		"ci-runner":    {{RepoURL: "https://github.com/octo/a", Labels: "self-hosted"}},
		"vault-runner": {{RepoURL: "https://github.com/octo/private", Labels: "self-hosted,custom"}},
	}}
	a := newActs(fakeKV{}, nil, nm)
	env := actEnv()
	env.RegisterActivity(a.CountActiveRunners)

	val, err := env.ExecuteActivity(a.CountActiveRunners, []string{"vault-runner", "vault-runner"})
	if err != nil {
		t.Fatalf("CountActiveRunners: %v", err)
	}
	var counts map[string]int
	if err := val.Get(&counts); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if counts["octo/a|self-hosted"] != 1 {
		t.Errorf("default-job bucket = %d, want 1", counts["octo/a|self-hosted"])
	}
	if counts["octo/private|custom,self-hosted"] != 1 {
		t.Errorf("extra-job bucket = %d, want 1 (counted once despite duplicate extra job)", counts["octo/private|custom,self-hosted"])
	}
}

func TestCountActiveRunners_Error(t *testing.T) {
	nm := &fakeNomad{slotsErr: errors.New("nomad down")}
	a := newActs(fakeKV{}, nil, nm)
	env := actEnv()
	env.RegisterActivity(a.CountActiveRunners)
	if _, err := env.ExecuteActivity(a.CountActiveRunners, []string{}); err == nil {
		t.Fatal("expected CountActiveRunners to propagate the Nomad error")
	}
}

func TestSplitLabels(t *testing.T) {
	if got := splitLabels(""); got != nil {
		t.Errorf("splitLabels(\"\") = %v, want nil", got)
	}
	got := splitLabels("self-hosted,amd64")
	if len(got) != 2 || got[0] != "self-hosted" || got[1] != "amd64" {
		t.Errorf("splitLabels = %v, want [self-hosted amd64]", got)
	}
}

func TestRunnerBucketKey(t *testing.T) {
	// A runner dispatched with labels in one order and a job requesting them in
	// another must reconcile in the same bucket.
	if RunnerBucketKey("octo/a", []string{"self-hosted", "amd64"}) !=
		RunnerBucketKey("octo/a", []string{"amd64", "self-hosted"}) {
		t.Error("label order must not change the bucket key")
	}
	if RunnerBucketKey("octo/a", []string{"self-hosted"}) ==
		RunnerBucketKey("octo/b", []string{"self-hosted"}) {
		t.Error("different repos must have different bucket keys")
	}
}
