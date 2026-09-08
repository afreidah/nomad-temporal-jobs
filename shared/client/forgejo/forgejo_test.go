// -------------------------------------------------------------------------------
// Forgejo Client Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Exercises the client against an httptest server standing in for a Forgejo
// instance. The queued filter carries the weight: Forgejo reports a job as
// waiting from creation until a runner claims it, so the tests pin that a
// claimed job is excluded even while its status still reads waiting.
// -------------------------------------------------------------------------------

package forgejo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestClient points a client at srv with a fixed token.
func newTestClient(t *testing.T, srv *httptest.Server) *Forgejo {
	t.Helper()
	c, err := New(srv.URL, "test-token")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNew_RejectsEmptyInputs(t *testing.T) {
	if _, err := New("", "tok"); err == nil {
		t.Error("New with empty base URL: want error, got nil")
	}
	if _, err := New("http://forge", ""); err == nil {
		t.Error("New with empty token: want error, got nil")
	}
}

func TestNew_TrimsTrailingSlash(t *testing.T) {
	c, err := New("http://forge/", "tok")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.baseURL != "http://forge" {
		t.Errorf("baseURL = %q, want %q", c.baseURL, "http://forge")
	}
}

func TestListQueuedSelfHostedJobs_FiltersToUnclaimedWaiting(t *testing.T) {
	const body = `[
	  {"id":1,"name":"build","repo_id":5,"runs_on":["ubuntu-latest"],"status":"waiting","task_id":0},
	  {"id":2,"name":"claimed","repo_id":5,"runs_on":["ubuntu-latest"],"status":"waiting","task_id":99},
	  {"id":3,"name":"running","repo_id":5,"runs_on":["ops"],"status":"running","task_id":42},
	  {"id":4,"name":"ops-build","repo_id":5,"runs_on":["ops"],"status":"waiting","task_id":0}
	]`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/v1/repos/alex/munchbox/actions/runners/jobs"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("Authorization"), "token test-token"; got != want {
			t.Errorf("auth header = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	jobs, err := newTestClient(t, srv).ListQueuedSelfHostedJobs(context.Background(), "alex", "munchbox")
	if err != nil {
		t.Fatalf("ListQueuedSelfHostedJobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("got %d jobs, want 2 (the unclaimed waiting ones): %+v", len(jobs), jobs)
	}
	if jobs[0].ID != 1 || jobs[1].ID != 4 {
		t.Errorf("got ids %d and %d, want 1 and 4", jobs[0].ID, jobs[1].ID)
	}
	if got, want := jobs[1].Labels[0], "ops"; got != want {
		t.Errorf("labels[0] = %q, want %q", got, want)
	}
	if jobs[0].Name != "build" {
		t.Errorf("name = %q, want %q", jobs[0].Name, "build")
	}
}

func TestListQueuedSelfHostedJobs_EmptyWhenNoneQueued(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":9,"status":"running","task_id":3,"runs_on":["x"]}]`))
	}))
	defer srv.Close()

	jobs, err := newTestClient(t, srv).ListQueuedSelfHostedJobs(context.Background(), "alex", "repo")
	if err != nil {
		t.Fatalf("ListQueuedSelfHostedJobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("got %d jobs, want 0", len(jobs))
	}
}

func TestListQueuedSelfHostedJobs_EscapesPathSegments(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).ListQueuedSelfHostedJobs(context.Background(), "own er", "re/po")
	if err != nil {
		t.Fatalf("ListQueuedSelfHostedJobs: %v", err)
	}
	if want := "/api/v1/repos/own%20er/re%2Fpo/actions/runners/jobs"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
}

func TestListQueuedSelfHostedJobs_ErrorsOnBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	if _, err := newTestClient(t, srv).ListQueuedSelfHostedJobs(context.Background(), "a", "b"); err == nil {
		t.Error("want error on 403, got nil")
	}
}

func TestListQueuedSelfHostedJobs_ErrorsOnBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	if _, err := newTestClient(t, srv).ListQueuedSelfHostedJobs(context.Background(), "a", "b"); err == nil {
		t.Error("want error on malformed body, got nil")
	}
}

func TestCreateRunnerRegistrationToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/v1/repos/alex/munchbox/actions/runners/registration-token"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte(`{"token":"reg-abc123"}`))
	}))
	defer srv.Close()

	tok, expiry, err := newTestClient(t, srv).CreateRunnerRegistrationToken(context.Background(), "alex", "munchbox")
	if err != nil {
		t.Fatalf("CreateRunnerRegistrationToken: %v", err)
	}
	if tok != "reg-abc123" {
		t.Errorf("token = %q, want %q", tok, "reg-abc123")
	}
	// Forgejo reports no expiry; the zero value is the documented contract.
	if !expiry.IsZero() {
		t.Errorf("expiry = %v, want zero", expiry)
	}
}

func TestCreateRunnerRegistrationToken_RejectsEmptyToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"token":""}`))
	}))
	defer srv.Close()

	if _, _, err := newTestClient(t, srv).CreateRunnerRegistrationToken(context.Background(), "a", "b"); err == nil {
		t.Error("want error on empty token, got nil")
	}
}

func TestCreateRunnerRegistrationToken_ErrorsOnBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, _, err := newTestClient(t, srv).CreateRunnerRegistrationToken(context.Background(), "a", "b"); err == nil {
		t.Error("want error on 500, got nil")
	}
}

func TestGet_ErrorsOnUnreachableHost(t *testing.T) {
	c, err := New("http://127.0.0.1:1", "tok")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.ListQueuedSelfHostedJobs(context.Background(), "a", "b"); err == nil {
		t.Error("want error against a closed port, got nil")
	}
}
