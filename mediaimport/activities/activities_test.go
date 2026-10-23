// -------------------------------------------------------------------------------
// Media Import Activities - Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
// -------------------------------------------------------------------------------

package activities

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubDoer lets a test inject transport behavior (network errors, statuses)
// without standing up a server.
type stubDoer struct {
	fn func(*http.Request) (*http.Response, error)
}

func (s stubDoer) Do(r *http.Request) (*http.Response, error) { return s.fn(r) }

// fakeVault satisfies secretReader with a static map.
type fakeVault struct{ vals map[string]map[string]string }

func (f fakeVault) ReadKVField(_ context.Context, path, field string) (string, error) {
	if v, ok := f.vals[path][field]; ok {
		return v, nil
	}
	return "", fmt.Errorf("missing %s/%s", path, field)
}

func fullVault() fakeVault {
	return fakeVault{vals: map[string]map[string]string{
		vaultMediaImportPath: {fieldSonarrAPIKey: "sk", fieldRadarrAPIKey: "rk"},
		vaultDelugePath:      {fieldDelugePass: "pw"},
		vaultJellyfinPath:    {fieldJellyfinKey: "jk"},
	}}
}

func newTestActs(t *testing.T, cfg Config) *Activities {
	t.Helper()
	cfg.Vault = fullVault()
	acts, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return acts
}

func TestConfigValidate(t *testing.T) {
	if err := (Config{}).Validate(); err == nil {
		t.Fatal("empty config should be invalid")
	}
	ok := Config{Vault: fullVault(), SonarrAddr: "a", RadarrAddr: "b", DelugeAddr: "c", JellyfinAddr: "d"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestNewMissingSecret(t *testing.T) {
	cfg := Config{
		Vault:      fakeVault{vals: map[string]map[string]string{}},
		SonarrAddr: "a", RadarrAddr: "b", DelugeAddr: "c", JellyfinAddr: "d",
	}
	if _, err := New(context.Background(), cfg); err == nil {
		t.Fatal("expected error when secrets are missing")
	}
}

func TestImportable(t *testing.T) {
	cases := []struct {
		name    string
		reasons []string
		want    bool
	}{
		{"clean", nil, true},
		{"unexpected pack", []string{"Episode 3x01 was unexpected considering the release"}, true},
		{"not an upgrade", []string{"Not an upgrade for existing episode file(s)."}, false},
		{"mixed", []string{"Episode unexpected considering", "Not an upgrade"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rej []arrRejection
			for _, r := range c.reasons {
				rej = append(rej, arrRejection{Reason: r})
			}
			if got := importable(&arrDecision{Rejects: rej}); got != c.want {
				t.Fatalf("importable=%v want %v", got, c.want)
			}
		})
	}
}

func TestListCompleted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req delugeRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "auth.login":
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
		case "core.get_torrents_status":
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{
				"h1": map[string]any{"name": "Show S01", "progress": 100.0, "state": "Seeding"},
				"h2": map[string]any{"name": "Movie WIP", "progress": 42.0, "state": "Downloading"},
			}})
		}
	}))
	defer srv.Close()

	acts := newTestActs(t, Config{SonarrAddr: "a", RadarrAddr: "b", DelugeAddr: srv.URL, JellyfinAddr: "d"})
	done, err := acts.ListCompleted(context.Background())
	if err != nil {
		t.Fatalf("ListCompleted: %v", err)
	}
	if len(done) != 1 || done[0] != "Show S01" {
		t.Fatalf("expected [Show S01], got %v", done)
	}
}

func TestSonarrImport(t *testing.T) {
	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode([]arrDecision{
				{Path: "/data/torrents/x/e1.mkv", Series: &arrRef{ID: 5}, Episodes: []arrEpisode{{ID: 11}}, Quality: json.RawMessage(`{}`)},
				{Path: "/data/torrents/x/e2.mkv", Series: &arrRef{ID: 5}, Episodes: []arrEpisode{{ID: 12}}, Rejects: []arrRejection{{Reason: "Not an upgrade for existing episode file(s)."}}},
			})
		case http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(&posted)
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()

	acts := newTestActs(t, Config{SonarrAddr: srv.URL, RadarrAddr: "b", DelugeAddr: "c", JellyfinAddr: "d"})
	res, err := acts.SonarrImport(context.Background(), ImportRequest{Folder: "x"})
	if err != nil {
		t.Fatalf("SonarrImport: %v", err)
	}
	if res.Imported != 1 || res.Skipped != 1 || res.NoMatch {
		t.Fatalf("unexpected result: %+v", res)
	}
	if files, _ := posted["files"].([]any); len(files) != 1 {
		t.Fatalf("expected 1 file posted, got %v", posted["files"])
	}
}

func TestSonarrImportNoMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]arrDecision{{Path: "/x/f.mkv"}}) // no series
	}))
	defer srv.Close()
	acts := newTestActs(t, Config{SonarrAddr: srv.URL, RadarrAddr: "b", DelugeAddr: "c", JellyfinAddr: "d"})
	res, err := acts.SonarrImport(context.Background(), ImportRequest{Folder: "x"})
	if err != nil {
		t.Fatalf("SonarrImport: %v", err)
	}
	if !res.NoMatch {
		t.Fatalf("expected NoMatch, got %+v", res)
	}
}

func TestSonarrImportDryRun(t *testing.T) {
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
		}
		_ = json.NewEncoder(w).Encode([]arrDecision{
			{Path: "/x/e.mkv", Series: &arrRef{ID: 1}, Episodes: []arrEpisode{{ID: 2}}, Quality: json.RawMessage(`{}`)},
		})
	}))
	defer srv.Close()
	acts := newTestActs(t, Config{SonarrAddr: srv.URL, RadarrAddr: "b", DelugeAddr: "c", JellyfinAddr: "d"})
	res, _ := acts.SonarrImport(context.Background(), ImportRequest{Folder: "x", DryRun: true})
	if res.Imported != 1 || posts != 0 {
		t.Fatalf("dry-run should not POST: imported=%d posts=%d", res.Imported, posts)
	}
}

func TestJellyfinRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); !strings.Contains(auth, `Token="jk"`) {
			t.Errorf("expected MediaBrowser Token in Authorization, got %q", auth)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	acts := newTestActs(t, Config{SonarrAddr: "a", RadarrAddr: "b", DelugeAddr: "c", JellyfinAddr: srv.URL})
	if err := acts.JellyfinRefresh(context.Background()); err != nil {
		t.Fatalf("JellyfinRefresh: %v", err)
	}
}

func TestRadarrImport(t *testing.T) {
	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode([]arrDecision{
				{Path: "/data/torrents/m/movie.mkv", Movie: &arrRef{ID: 9}, Quality: json.RawMessage(`{}`)},
				{Path: "/data/torrents/m/dupe.mkv", Movie: &arrRef{ID: 9}, Rejects: []arrRejection{{Reason: "Not an upgrade for existing movie file."}}},
			})
		case http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(&posted)
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()
	acts := newTestActs(t, Config{SonarrAddr: "a", RadarrAddr: srv.URL, DelugeAddr: "c", JellyfinAddr: "d"})
	res, err := acts.RadarrImport(context.Background(), ImportRequest{Folder: "m"})
	if err != nil {
		t.Fatalf("RadarrImport: %v", err)
	}
	if res.Imported != 1 || res.Skipped != 1 || res.NoMatch {
		t.Fatalf("unexpected result: %+v", res)
	}
	if files, _ := posted["files"].([]any); len(files) != 1 {
		t.Fatalf("expected 1 file posted, got %v", posted["files"])
	}
}

func TestRadarrImportNoMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]arrDecision{{Path: "/m/f.mkv"}}) // no movie
	}))
	defer srv.Close()
	acts := newTestActs(t, Config{SonarrAddr: "a", RadarrAddr: srv.URL, DelugeAddr: "c", JellyfinAddr: "d"})
	res, _ := acts.RadarrImport(context.Background(), ImportRequest{Folder: "m"})
	if !res.NoMatch {
		t.Fatalf("expected NoMatch, got %+v", res)
	}
}

func TestListCompletedLoginRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req delugeRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "auth.login" {
			_ = json.NewEncoder(w).Encode(map[string]any{"result": false})
		}
	}))
	defer srv.Close()
	acts := newTestActs(t, Config{SonarrAddr: "a", RadarrAddr: "b", DelugeAddr: srv.URL, JellyfinAddr: "d"})
	if _, err := acts.ListCompleted(context.Background()); err == nil {
		t.Fatal("expected error when auth.login is rejected")
	}
}

func TestActivitiesTransportErrors(t *testing.T) {
	boom := stubDoer{fn: func(*http.Request) (*http.Response, error) { return nil, errors.New("boom") }}
	acts := newTestActs(t, Config{SonarrAddr: "http://s", RadarrAddr: "http://r", DelugeAddr: "http://d", JellyfinAddr: "http://j"})
	acts.http = boom
	ctx := context.Background()
	if _, err := acts.ListCompleted(ctx); err == nil {
		t.Error("ListCompleted should surface a transport error")
	}
	if _, err := acts.SonarrImport(ctx, ImportRequest{Folder: "x"}); err == nil {
		t.Error("SonarrImport should surface a transport error")
	}
	if _, err := acts.RadarrImport(ctx, ImportRequest{Folder: "x"}); err == nil {
		t.Error("RadarrImport should surface a transport error")
	}
	if err := acts.JellyfinRefresh(ctx); err == nil {
		t.Error("JellyfinRefresh should surface a transport error")
	}
}

func TestSonarrImportBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	acts := newTestActs(t, Config{SonarrAddr: srv.URL, RadarrAddr: "b", DelugeAddr: "c", JellyfinAddr: "d"})
	if _, err := acts.SonarrImport(context.Background(), ImportRequest{Folder: "x"}); err == nil {
		t.Fatal("expected error on non-200 manualimport")
	}
}

func TestSonarrImportPostError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode([]arrDecision{
			{Path: "/x/e.mkv", Series: &arrRef{ID: 1}, Episodes: []arrEpisode{{ID: 2}}, Quality: json.RawMessage(`{}`)},
		})
	}))
	defer srv.Close()
	acts := newTestActs(t, Config{SonarrAddr: srv.URL, RadarrAddr: "b", DelugeAddr: "c", JellyfinAddr: "d"})
	if _, err := acts.SonarrImport(context.Background(), ImportRequest{Folder: "x"}); err == nil {
		t.Fatal("expected error when the ManualImport POST fails")
	}
}

func TestJellyfinRefreshBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	acts := newTestActs(t, Config{SonarrAddr: "a", RadarrAddr: "b", DelugeAddr: "c", JellyfinAddr: srv.URL})
	if err := acts.JellyfinRefresh(context.Background()); err == nil {
		t.Fatal("expected error on non-2xx refresh")
	}
}

func TestConfigValidateMissingAddr(t *testing.T) {
	cfg := Config{Vault: fullVault(), SonarrAddr: "a", RadarrAddr: "b", DelugeAddr: "c"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "JellyfinAddr") {
		t.Fatalf("expected JellyfinAddr error, got %v", err)
	}
}

func TestNewInvalidConfig(t *testing.T) {
	if _, err := New(context.Background(), Config{}); err == nil {
		t.Fatal("expected error for invalid config")
	}
}

// Each Vault read is dropped in turn so every credential's error path fires.
func TestNewEachMissingSecret(t *testing.T) {
	cases := []struct {
		path, field string
	}{
		{vaultMediaImportPath, fieldSonarrAPIKey},
		{vaultMediaImportPath, fieldRadarrAPIKey},
		{vaultDelugePath, fieldDelugePass},
		{vaultJellyfinPath, fieldJellyfinKey},
	}
	for _, c := range cases {
		t.Run(c.field, func(t *testing.T) {
			v := fullVault()
			delete(v.vals[c.path], c.field)
			cfg := Config{Vault: v, SonarrAddr: "a", RadarrAddr: "b", DelugeAddr: "c", JellyfinAddr: "d"}
			_, err := New(context.Background(), cfg)
			if err == nil || !strings.Contains(err.Error(), c.field) {
				t.Fatalf("expected error naming %s, got %v", c.field, err)
			}
		})
	}
}

func TestReconcileConfigApplyDefaults(t *testing.T) {
	c := ReconcileConfig{}
	c.ApplyDefaults()
	if c.Concurrency != 4 {
		t.Fatalf("default concurrency=%d want 4", c.Concurrency)
	}
	c = ReconcileConfig{Concurrency: 7}
	c.ApplyDefaults()
	if c.Concurrency != 7 {
		t.Fatalf("explicit concurrency overwritten: %d", c.Concurrency)
	}
}

// delugeServer accepts auth.login and answers get_torrents_status with fn.
func delugeServer(fn func(http.ResponseWriter)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req delugeRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "auth.login" {
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
			return
		}
		fn(w)
	}))
}

func TestListCompletedStatusErrors(t *testing.T) {
	cases := []struct {
		name string
		fn   func(http.ResponseWriter)
	}{
		{"rpc error", func(w http.ResponseWriter) {
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "not authed"}})
		}},
		{"bad status", func(w http.ResponseWriter) { w.WriteHeader(http.StatusBadGateway) }},
		{"bad json", func(w http.ResponseWriter) { _, _ = w.Write([]byte("{not json")) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := delugeServer(c.fn)
			defer srv.Close()
			acts := newTestActs(t, Config{SonarrAddr: "a", RadarrAddr: "b", DelugeAddr: srv.URL, JellyfinAddr: "d"})
			if _, err := acts.ListCompleted(context.Background()); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestDelugeCallMarshalError(t *testing.T) {
	acts := newTestActs(t, Config{SonarrAddr: "a", RadarrAddr: "b", DelugeAddr: "c", JellyfinAddr: "d"})
	err := acts.delugeCall(context.Background(), delugeRequest{Params: []any{func() {}}}, &struct{}{})
	if err == nil || !strings.Contains(err.Error(), "marshal") {
		t.Fatalf("expected marshal error, got %v", err)
	}
}

// A control character in the address makes http.NewRequest fail, covering the
// request-build error path of every outbound call.
func TestActivitiesBuildRequestErrors(t *testing.T) {
	bad := "http://bad\x7f"
	acts := newTestActs(t, Config{SonarrAddr: bad, RadarrAddr: bad, DelugeAddr: bad, JellyfinAddr: bad})
	ctx := context.Background()
	if _, err := acts.ListCompleted(ctx); err == nil {
		t.Error("ListCompleted should fail to build its request")
	}
	if _, err := acts.RadarrImport(ctx, ImportRequest{Folder: "x"}); err == nil {
		t.Error("RadarrImport should fail to build its request")
	}
	if err := acts.arrManualImport(ctx, bad, "k", nil); err == nil {
		t.Error("arrManualImport should fail to build its request")
	}
	if err := acts.JellyfinRefresh(ctx); err == nil {
		t.Error("JellyfinRefresh should fail to build its request")
	}
}

func TestArrDecisionsDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	}))
	defer srv.Close()
	acts := newTestActs(t, Config{SonarrAddr: "a", RadarrAddr: srv.URL, DelugeAddr: "c", JellyfinAddr: "d"})
	if _, err := acts.RadarrImport(context.Background(), ImportRequest{Folder: "m"}); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestArrManualImportMarshalError(t *testing.T) {
	acts := newTestActs(t, Config{SonarrAddr: "a", RadarrAddr: "b", DelugeAddr: "c", JellyfinAddr: "d"})
	files := []arrImportFile{{Path: "p", Quality: json.RawMessage(`{`)}}
	if err := acts.arrManualImport(context.Background(), "http://r", "k", files); err == nil {
		t.Fatal("expected marshal error for invalid raw quality")
	}
}

func TestArrManualImportTransportError(t *testing.T) {
	acts := newTestActs(t, Config{SonarrAddr: "a", RadarrAddr: "b", DelugeAddr: "c", JellyfinAddr: "d"})
	acts.http = stubDoer{fn: func(*http.Request) (*http.Response, error) { return nil, errors.New("boom") }}
	if err := acts.arrManualImport(context.Background(), "http://r", "k", nil); err == nil {
		t.Fatal("expected transport error")
	}
}

func TestRadarrImportPostError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode([]arrDecision{
			{Path: "/m/movie.mkv", Movie: &arrRef{ID: 9}, Quality: json.RawMessage(`{}`)},
		})
	}))
	defer srv.Close()
	acts := newTestActs(t, Config{SonarrAddr: "a", RadarrAddr: srv.URL, DelugeAddr: "c", JellyfinAddr: "d"})
	if _, err := acts.RadarrImport(context.Background(), ImportRequest{Folder: "m"}); err == nil {
		t.Fatal("expected error when the ManualImport POST fails")
	}
}
