// -------------------------------------------------------------------------------
// Shared Nomad Client - HTTP Integration Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Drives the Nomad client methods against an httptest server that returns canned
// Nomad API responses (marshaled from the real api.* types, so shapes can't
// drift). Hermetic -- no real Nomad agent. Covers the list/info/scale/wait paths
// and their error handling.
// -------------------------------------------------------------------------------

package nomad

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
)

// nomadStub serves canned responses for the endpoints the client hits. When
// failPath is set and matches, it returns 500 so error paths can be exercised.
func nomadStub(failPath string) *httptest.Server {
	h := func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if failPath != "" && strings.Contains(p, failPath) {
			http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		switch {
		case p == "/v1/nodes":
			_ = enc.Encode([]*api.NodeListStub{
				{ID: "n1", Name: "worker-1", Status: api.NodeStatusReady},
				{ID: "n2", Name: "worker-2", Status: api.NodeStatusDown}, // skipped: not ready
			})
		case p == "/v1/node/n1":
			_ = enc.Encode(&api.Node{
				ID: "n1", Name: "worker-1", HTTPAddr: "10.0.0.1:4646",
				Attributes: map[string]string{"unique.network.ip-address": "10.0.0.1"},
			})
		case p == "/v1/node/n1/allocations":
			_ = enc.Encode([]*api.Allocation{
				{JobID: "web", ClientStatus: api.AllocClientStatusRunning},
				{JobID: "db", ClientStatus: api.AllocClientStatusComplete}, // skipped: not running
			})
		case p == "/v1/allocations":
			_ = enc.Encode([]*api.AllocationListStub{{ID: "a1", ClientStatus: api.AllocClientStatusRunning}})
		case p == "/v1/allocation/a1":
			_ = enc.Encode(&api.Allocation{ID: "a1", Job: &api.Job{
				TaskGroups: []*api.TaskGroup{{Tasks: []*api.Task{
					{Driver: "docker", Config: map[string]any{"image": "nginx:1.27"}},
				}}},
			}})
		case p == "/v1/job/web/allocations":
			_ = enc.Encode([]*api.AllocationListStub{
				{ID: "a1", NodeID: "n1", ClientStatus: api.AllocClientStatusRunning},
			})
		case strings.HasSuffix(p, "/scale"):
			_ = enc.Encode(&api.JobRegisterResponse{EvalID: "e1"})
		case strings.HasSuffix(p, "/dispatch"):
			_ = enc.Encode(&api.JobDispatchResponse{DispatchedJobID: "ci-runner/dispatch-1-abc", EvalID: "e2"})
		case r.Method == http.MethodDelete && strings.HasPrefix(p, "/v1/job/"):
			_ = enc.Encode(&api.JobDeregisterResponse{EvalID: "e3"})
		default:
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		}
	}
	return httptest.NewServer(http.HandlerFunc(h))
}

func testNomad(t *testing.T, ts *httptest.Server) *Nomad {
	t.Helper()
	client, err := api.NewClient(&api.Config{Address: ts.URL})
	if err != nil {
		t.Fatalf("api.NewClient: %v", err)
	}
	return &Nomad{client: client}
}

func TestNomad_ClientNodes(t *testing.T) {
	ts := nomadStub("")
	defer ts.Close()
	nodes, err := testNomad(t, ts).ClientNodes(context.Background())
	if err != nil {
		t.Fatalf("ClientNodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("got %d nodes, want 1 (only the ready one)", len(nodes))
	}
	if nodes[0].Address != "10.0.0.1" {
		t.Errorf("Address = %q, want 10.0.0.1 (from ip-address attr)", nodes[0].Address)
	}
}

func TestNomad_RunningJobIDs(t *testing.T) {
	ts := nomadStub("")
	defer ts.Close()
	jobs, err := testNomad(t, ts).RunningJobIDs(context.Background(), "n1")
	if err != nil {
		t.Fatalf("RunningJobIDs: %v", err)
	}
	if _, ok := jobs["web"]; !ok || len(jobs) != 1 {
		t.Errorf("jobs = %v, want only {web} (db is complete)", jobs)
	}
}

func TestNomad_FindJobNode(t *testing.T) {
	ts := nomadStub("")
	defer ts.Close()
	node, err := testNomad(t, ts).FindJobNode(context.Background(), "web")
	if err != nil {
		t.Fatalf("FindJobNode: %v", err)
	}
	if node.ID != "n1" || node.Address != "10.0.0.1" {
		t.Errorf("node = %+v, want ID n1 / Address 10.0.0.1", node)
	}
}

func TestNomad_RunningImages(t *testing.T) {
	ts := nomadStub("")
	defer ts.Close()
	imgs, err := testNomad(t, ts).RunningImages(context.Background())
	if err != nil {
		t.Fatalf("RunningImages: %v", err)
	}
	if len(imgs) != 1 || imgs[0] != "nginx:1.27" {
		t.Errorf("images = %v, want [nginx:1.27]", imgs)
	}
}

func TestNomad_ScaleJob(t *testing.T) {
	ts := nomadStub("")
	defer ts.Close()
	if err := testNomad(t, ts).ScaleJob(context.Background(), "web", "grp", 0, "test"); err != nil {
		t.Fatalf("ScaleJob: %v", err)
	}
}

func TestNomad_WaitAllocCount(t *testing.T) {
	ts := nomadStub("")
	defer ts.Close()
	got := -1
	err := testNomad(t, ts).WaitAllocCount(context.Background(), "web", 1, time.Millisecond, func(running int) { got = running })
	if err != nil {
		t.Fatalf("WaitAllocCount: %v", err)
	}
	if got != 1 {
		t.Errorf("onPoll running = %d, want 1", got)
	}
}

func TestNomad_DispatchJob(t *testing.T) {
	ts := nomadStub("")
	defer ts.Close()
	id, err := testNomad(t, ts).DispatchJob(context.Background(), "ci-runner", map[string]string{"repo_url": "https://github.com/octo/widget"})
	if err != nil {
		t.Fatalf("DispatchJob: %v", err)
	}
	if id != "ci-runner/dispatch-1-abc" {
		t.Errorf("dispatched id = %q, want ci-runner/dispatch-1-abc", id)
	}
}

func TestNomad_StopJob(t *testing.T) {
	ts := nomadStub("")
	defer ts.Close()
	if err := testNomad(t, ts).StopJob(context.Background(), "ci-runner/dispatch-1-abc"); err != nil {
		t.Fatalf("StopJob: %v", err)
	}
}

func TestNomad_ListError(t *testing.T) {
	ts := nomadStub("/v1/nodes")
	defer ts.Close()
	if _, err := testNomad(t, ts).ClientNodes(context.Background()); err == nil {
		t.Fatal("expected an error when the node list call fails")
	}
}

// activeRunnersStub serves an allocation list mixing dispatched runner children
// (active and finished), an unrelated job, and children whose job info is either
// meta-less or errors -- so ActiveRunnerSlots's filter, meta read, and skip
// paths are all exercised.
func activeRunnersStub() *httptest.Server {
	h := func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		switch {
		case p == "/v1/allocations":
			_ = enc.Encode([]*api.AllocationListStub{
				{ID: "r1", JobID: "ci-runner/dispatch-100-aaa", ClientStatus: api.AllocClientStatusRunning},
				{ID: "r2", JobID: "ci-runner/dispatch-200-bbb", ClientStatus: api.AllocClientStatusPending},
				{ID: "r3", JobID: "ci-runner/dispatch-300-ccc", ClientStatus: api.AllocClientStatusComplete}, // finished: skipped
				{ID: "x1", JobID: "unrelated-job", ClientStatus: api.AllocClientStatusRunning},               // wrong prefix: skipped
				{ID: "r4", JobID: "ci-runner/dispatch-400-ddd", ClientStatus: api.AllocClientStatusRunning},  // job info 500: skipped
				{ID: "r5", JobID: "ci-runner/dispatch-500-eee", ClientStatus: api.AllocClientStatusRunning},  // no meta: empty slot
			})
		case strings.Contains(p, "dispatch-100"):
			_ = enc.Encode(&api.Job{Meta: map[string]string{"repo_url": "https://github.com/octo/a", "labels": "self-hosted"}})
		case strings.Contains(p, "dispatch-200"):
			_ = enc.Encode(&api.Job{Meta: map[string]string{"repo_url": "https://github.com/octo/a", "labels": "self-hosted,amd64"}})
		case strings.Contains(p, "dispatch-400"):
			http.Error(w, `{"error":"gone"}`, http.StatusInternalServerError) // Info fails -> skipped
		case strings.Contains(p, "dispatch-500"):
			_ = enc.Encode(&api.Job{}) // nil Meta -> metaString returns ""
		default:
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		}
	}
	return httptest.NewServer(http.HandlerFunc(h))
}

func TestNomad_ActiveRunnerSlots(t *testing.T) {
	ts := activeRunnersStub()
	defer ts.Close()

	slots, err := testNomad(t, ts).ActiveRunnerSlots(context.Background(), "ci-runner")
	if err != nil {
		t.Fatalf("ActiveRunnerSlots: %v", err)
	}
	// Only the active children with fetchable job info: r1, r2, and r5 (meta-less
	// -> empty slot). r3 (complete), x1 (wrong prefix) and r4 (info 500) drop out.
	got := make(map[RunnerSlot]bool)
	for _, s := range slots {
		got[s] = true
	}
	want := []RunnerSlot{
		{RepoURL: "https://github.com/octo/a", Labels: "self-hosted"},
		{RepoURL: "https://github.com/octo/a", Labels: "self-hosted,amd64"},
		{RepoURL: "", Labels: ""},
	}
	if len(slots) != len(want) {
		t.Fatalf("got %d slots (%v), want %d", len(slots), slots, len(want))
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing slot %+v; got %v", w, slots)
		}
	}
}

func TestNomad_ActiveRunnerSlots_ListError(t *testing.T) {
	ts := nomadStub("/v1/allocations")
	defer ts.Close()
	if _, err := testNomad(t, ts).ActiveRunnerSlots(context.Background(), "ci-runner"); err == nil {
		t.Fatal("expected an error when the allocation list call fails")
	}
}

func TestNomad_RunnerTerminal(t *testing.T) {
	h := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		p := r.URL.Path
		switch {
		case strings.Contains(p, "running-job/allocations"):
			_ = enc.Encode([]*api.AllocationListStub{{ID: "a", ClientStatus: api.AllocClientStatusRunning}})
		case strings.Contains(p, "done-job/allocations"):
			_ = enc.Encode([]*api.AllocationListStub{{ID: "a", ClientStatus: api.AllocClientStatusComplete}})
		case strings.Contains(p, "empty-job/allocations"):
			_ = enc.Encode([]*api.AllocationListStub{})
		case strings.Contains(p, "gone-job/allocations"):
			http.Error(w, `{"error":"job not found"}`, http.StatusNotFound)
		case strings.Contains(p, "boom-job/allocations"):
			http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
		default:
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(h))
	defer ts.Close()
	n := testNomad(t, ts)
	ctx := context.Background()

	cases := []struct {
		job      string
		wantDone bool
		wantErr  bool
	}{
		{"running-job", false, false}, // an alloc still running
		{"done-job", true, false},     // all allocs terminal
		{"empty-job", false, false},   // dispatched, not scheduled yet
		{"gone-job", true, false},     // job not found -> already gone
		{"boom-job", false, true},     // API error surfaces
	}
	for _, c := range cases {
		done, err := n.RunnerTerminal(ctx, c.job)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", c.job, err, c.wantErr)
		}
		if err == nil && done != c.wantDone {
			t.Errorf("%s: done = %v, want %v", c.job, done, c.wantDone)
		}
	}
}
