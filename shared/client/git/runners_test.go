// -------------------------------------------------------------------------------
// Shared GitHub App Client - Runner Registration / Job Discovery Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Drives CreateRunnerRegistrationToken and ListQueuedSelfHostedJobs against an
// httptest GitHub stub: the registration token round-trips, and job discovery
// keeps only queued self-hosted jobs while de-duplicating those that show up
// under both the queued and in_progress run sweeps.
// -------------------------------------------------------------------------------

package git

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// testAppKey generates a throwaway RSA key in PEM form so ghinstallation can
// sign the App JWT; the stub server never verifies it.
func testAppKey(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

// newTestGitHub builds a GitHub client whose API calls hit srv. handler owns the
// route table; the installation-token endpoint is wired here since every path
// needs it.
func newTestGitHub(t *testing.T, handler http.Handler) *GitHub {
	t.Helper()
	// WithEnterpriseURLs forces an /api/v3 prefix on every request; strip it so
	// the route tables below read as the bare GitHub API paths.
	srv := httptest.NewServer(http.StripPrefix("/api/v3", handler))
	t.Cleanup(srv.Close)

	gh, err := NewGitHub(context.Background(), GitHubConfig{
		AppID:          123,
		InstallationID: 456, // set so NewGitHub doesn't call ListInstallations
		PrivateKeyPEM:  testAppKey(t),
		BaseURL:        srv.URL + "/",
	})
	if err != nil {
		t.Fatalf("NewGitHub: %v", err)
	}
	return gh
}

// tokenMux returns a mux that serves the installation-token mint and lets the
// caller register the remaining endpoints.
func tokenMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app/installations/456/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_installation", "expires_at": "2099-01-01T00:00:00Z"})
	})
	return mux
}

func TestCreateRunnerRegistrationToken(t *testing.T) {
	mux := tokenMux()
	mux.HandleFunc("POST /repos/octo/widget/actions/runners/registration-token", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "ARRT_reg", "expires_at": "2099-06-01T12:00:00Z"})
	})

	gh := newTestGitHub(t, mux)
	tok, expiry, err := gh.CreateRunnerRegistrationToken(context.Background(), "octo", "widget")
	if err != nil {
		t.Fatalf("CreateRunnerRegistrationToken: %v", err)
	}
	if tok != "ARRT_reg" {
		t.Errorf("token = %q, want ARRT_reg", tok)
	}
	if expiry.Year() != 2099 {
		t.Errorf("expiry = %v, want year 2099", expiry)
	}
}

func TestListQueuedSelfHostedJobs(t *testing.T) {
	mux := tokenMux()

	// Two runs surfaced by the queued sweep, one more by the in_progress sweep.
	// Run 1's queued job 1001 also appears under in_progress (dedup territory).
	runsByStatus := map[string][]map[string]any{
		"queued":      {{"id": 1}, {"id": 2}},
		"in_progress": {{"id": 1}, {"id": 3}},
	}
	mux.HandleFunc("GET /repos/octo/widget/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		runs := runsByStatus[r.URL.Query().Get("status")]
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(runs), "workflow_runs": runs})
	})

	jobsByRun := map[string][]map[string]any{
		// Queued + self-hosted: kept.
		"1": {{"id": 1001, "run_id": 1, "name": "build", "status": "queued", "labels": []string{"self-hosted", "amd64"}}},
		// Queued but GitHub-hosted: dropped (no self-hosted label).
		"2": {{"id": 1002, "run_id": 2, "name": "lint", "status": "queued", "labels": []string{"ubuntu-latest"}}},
		// Self-hosted but already running: dropped (status != queued).
		"3": {{"id": 1003, "run_id": 3, "name": "test", "status": "in_progress", "labels": []string{"self-hosted"}}},
	}
	mux.HandleFunc("GET /repos/octo/widget/actions/runs/{run_id}/jobs", func(w http.ResponseWriter, r *http.Request) {
		jobs := jobsByRun[r.PathValue("run_id")]
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(jobs), "jobs": jobs})
	})

	gh := newTestGitHub(t, mux)
	got, err := gh.ListQueuedSelfHostedJobs(context.Background(), "octo", "widget")
	if err != nil {
		t.Fatalf("ListQueuedSelfHostedJobs: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("got %d jobs, want 1: %+v", len(got), got)
	}
	j := got[0]
	if j.ID != 1001 || j.RunID != 1 || j.Name != "build" {
		t.Errorf("job = %+v, want id 1001 / run 1 / name build", j)
	}
	if !slices.Contains(j.Labels, "self-hosted") {
		t.Errorf("labels = %v, want self-hosted present", j.Labels)
	}
}
