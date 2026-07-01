// -------------------------------------------------------------------------------
// Runner Scaler Activities - Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Runs the activities in a TestActivityEnvironment with in-memory fakes for the
// githubRunners, kvGetter, and jobDispatcher consumer interfaces: repo/profile
// parsing from Consul KV (including missing/malformed keys), queued-job
// discovery, the dispatch meta the runner job receives (registration token
// minted internally, image carried only when the profile sets one), and the
// reaper tolerating an already-gone job.
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

type fakeNomad struct {
	dispatchedMeta map[string]string
	dispatchedJob  string
	stopped        []string
	stopErr        error
	slots          []nomad.RunnerSlot
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
	return "ci-runner/dispatch-1-abc", nil
}

func (f *fakeNomad) StopJob(_ context.Context, jobID string) error {
	f.stopped = append(f.stopped, jobID)
	return f.stopErr
}

func (f *fakeNomad) ActiveRunnerSlots(_ context.Context, _ string) ([]nomad.RunnerSlot, error) {
	return f.slots, f.slotsErr
}

func actEnv() *testsuite.TestActivityEnvironment {
	return (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
}

func newActs(kv fakeKV, gh *fakeGitHub, nm *fakeNomad) *Activities {
	return New(Config{GitHub: gh, KV: kv, Nomad: nm})
}

// --- ListWatchedRepos --------------------------------------------------------

func TestListWatchedRepos(t *testing.T) {
	a := newActs(fakeKV{"runners/repos": []byte("octo/a\n# comment\n\nocto/b\n")}, nil, nil)
	env := actEnv()
	env.RegisterActivity(a.ListWatchedRepos)

	val, err := env.ExecuteActivity(a.ListWatchedRepos)
	if err != nil {
		t.Fatalf("ListWatchedRepos: %v", err)
	}
	var repos []string
	if err := val.Get(&repos); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(repos) != 2 || repos[0] != "octo/a" || repos[1] != "octo/b" {
		t.Errorf("repos = %v, want [octo/a octo/b]", repos)
	}
}

func TestListWatchedRepos_MissingKeyIsNonRetryable(t *testing.T) {
	a := newActs(fakeKV{}, nil, nil)
	env := actEnv()
	env.RegisterActivity(a.ListWatchedRepos)
	if _, err := env.ExecuteActivity(a.ListWatchedRepos); err == nil {
		t.Fatal("expected an error for a missing repo-list key")
	}
}

// --- LoadProfiles ------------------------------------------------------------

func TestLoadProfiles(t *testing.T) {
	kv := fakeKV{"runners/profiles": []byte(`{"amd64":{"image":"reg/ci-amd64:latest"},"default":{"image":"reg/ci:latest"}}`)}
	a := newActs(kv, nil, nil)
	env := actEnv()
	env.RegisterActivity(a.LoadProfiles)

	val, err := env.ExecuteActivity(a.LoadProfiles)
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	var profiles map[string]Profile
	if err := val.Get(&profiles); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if profiles["amd64"].Image != "reg/ci-amd64:latest" || profiles["default"].Image != "reg/ci:latest" {
		t.Errorf("profiles = %+v", profiles)
	}
}

func TestLoadProfiles_MissingKeyIsEmpty(t *testing.T) {
	a := newActs(fakeKV{}, nil, nil)
	env := actEnv()
	env.RegisterActivity(a.LoadProfiles)

	val, err := env.ExecuteActivity(a.LoadProfiles)
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	var profiles map[string]Profile
	if err := val.Get(&profiles); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(profiles) != 0 {
		t.Errorf("profiles = %v, want empty (missing key is not an error)", profiles)
	}
}

func TestLoadProfiles_MalformedIsNonRetryable(t *testing.T) {
	a := newActs(fakeKV{"runners/profiles": []byte("not json")}, nil, nil)
	env := actEnv()
	env.RegisterActivity(a.LoadProfiles)
	if _, err := env.ExecuteActivity(a.LoadProfiles); err == nil {
		t.Fatal("expected an error for malformed profiles JSON")
	}
}

// --- ListQueuedJobs ----------------------------------------------------------

func TestListQueuedJobs(t *testing.T) {
	gh := &fakeGitHub{jobs: []git.QueuedJob{{ID: 7, RunID: 1, Name: "build", Labels: []string{"self-hosted"}}}}
	a := newActs(fakeKV{}, gh, nil)
	env := actEnv()
	env.RegisterActivity(a.ListQueuedJobs)

	val, err := env.ExecuteActivity(a.ListQueuedJobs, "octo/widget")
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

func TestListQueuedJobs_InvalidRepo(t *testing.T) {
	a := newActs(fakeKV{}, &fakeGitHub{}, nil)
	env := actEnv()
	env.RegisterActivity(a.ListQueuedJobs)
	if _, err := env.ExecuteActivity(a.ListQueuedJobs, "no-slash"); err == nil {
		t.Fatal("expected an error for an unparseable repo")
	}
}

// --- DispatchRunner ----------------------------------------------------------

func TestDispatchRunner_BuildsMetaWithImage(t *testing.T) {
	gh := &fakeGitHub{token: "ARRT_reg"}
	nm := &fakeNomad{}
	a := newActs(fakeKV{}, gh, nm)
	env := actEnv()
	env.RegisterActivity(a.DispatchRunner)

	val, err := env.ExecuteActivity(a.DispatchRunner, DispatchSpec{
		Repo:   "octo/widget",
		Labels: []string{"self-hosted", "amd64"},
		Image:  "reg/ci-amd64:latest",
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
		t.Errorf("dispatched job = %q, want ci-runner", nm.dispatchedJob)
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

func TestDispatchRunner_OmitsImageWhenUnset(t *testing.T) {
	nm := &fakeNomad{}
	a := newActs(fakeKV{}, &fakeGitHub{token: "t"}, nm)
	env := actEnv()
	env.RegisterActivity(a.DispatchRunner)

	if _, err := env.ExecuteActivity(a.DispatchRunner, DispatchSpec{
		Repo:   "octo/widget",
		Labels: []string{"self-hosted"},
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
		Repo:   "octo/widget",
		Labels: []string{"self-hosted"},
	}); err == nil {
		t.Fatal("expected an error when registration-token minting fails")
	}
}

func TestDispatchRunner_DispatchError(t *testing.T) {
	a := newActs(fakeKV{}, &fakeGitHub{token: "t"}, &fakeNomad{dispatchErr: errors.New("403 permission denied")})
	env := actEnv()
	env.RegisterActivity(a.DispatchRunner)
	if _, err := env.ExecuteActivity(a.DispatchRunner, DispatchSpec{
		Repo:   "octo/widget",
		Labels: []string{"self-hosted"},
	}); err == nil {
		t.Fatal("expected an error when the Nomad dispatch fails")
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

	val, err := env.ExecuteActivity(a.CountActiveRunners)
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

func TestCountActiveRunners_Error(t *testing.T) {
	nm := &fakeNomad{slotsErr: errors.New("nomad down")}
	a := newActs(fakeKV{}, nil, nm)
	env := actEnv()
	env.RegisterActivity(a.CountActiveRunners)
	if _, err := env.ExecuteActivity(a.CountActiveRunners); err == nil {
		t.Fatal("expected CountActiveRunners to propagate the Nomad error")
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
