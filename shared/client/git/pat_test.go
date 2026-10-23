// -------------------------------------------------------------------------------
// Shared GitHub Client - Personal Access Token Job Discovery Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Drives GitHubPAT against an httptest GitHub stub: the PAT is sent as the
// bearer token with no installation-token mint, queued self-hosted jobs are
// returned, and a malformed base URL is rejected at construction.
// -------------------------------------------------------------------------------

package git

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitHubPAT_ListQueuedSelfHostedJobs(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/octo/widget/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer ghp_pat" {
			t.Errorf("Authorization = %q, want Bearer ghp_pat", got)
		}
		runs := []map[string]any{}
		if r.URL.Query().Get("status") == "queued" {
			runs = append(runs, map[string]any{"id": 7})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(runs), "workflow_runs": runs})
	})
	mux.HandleFunc("GET /repos/octo/widget/actions/runs/7/jobs", func(w http.ResponseWriter, _ *http.Request) {
		jobs := []map[string]any{{"id": 70, "run_id": 7, "name": "build", "status": "queued", "labels": []string{"self-hosted", "arm64"}}}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(jobs), "jobs": jobs})
	})
	srv := httptest.NewServer(http.StripPrefix("/api/v3", mux))
	t.Cleanup(srv.Close)

	gh, err := NewGitHubPAT("ghp_pat", srv.URL+"/")
	if err != nil {
		t.Fatalf("NewGitHubPAT: %v", err)
	}
	got, err := gh.ListQueuedSelfHostedJobs(context.Background(), "octo", "widget")
	if err != nil {
		t.Fatalf("ListQueuedSelfHostedJobs: %v", err)
	}
	if len(got) != 1 || got[0].ID != 70 || got[0].RunID != 7 || got[0].Name != "build" {
		t.Errorf("jobs = %+v, want one job id 70 / run 7 / name build", got)
	}
}

func TestNewGitHubPAT_DefaultBaseURL(t *testing.T) {
	gh, err := NewGitHubPAT("ghp_pat", "")
	if err != nil {
		t.Fatalf("NewGitHubPAT: %v", err)
	}
	if got := gh.cli.BaseURL(); got != "https://api.github.com/" {
		t.Errorf("BaseURL = %q, want https://api.github.com/", got)
	}
}

func TestNewGitHubPAT_BadBaseURL(t *testing.T) {
	if _, err := NewGitHubPAT("ghp_pat", "://bad"); err == nil {
		t.Error("expected an error for a malformed base URL")
	}
}
