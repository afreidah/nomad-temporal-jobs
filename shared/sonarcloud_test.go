// -------------------------------------------------------------------------------
// Shared SonarCloud Client - HTTP Integration Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Drives the token endpoints against an httptest server returning canned
// responses: mint returns the token value, revoke succeeds with no body, search
// lists names, the master token is sent as HTTP Basic username, and non-2xx
// responses surface an error. Hermetic -- no real SonarCloud.
// -------------------------------------------------------------------------------

package shared

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sonarStub(t *testing.T) *httptest.Server {
	t.Helper()
	h := func(w http.ResponseWriter, r *http.Request) {
		// The master token is sent as the Basic-auth username with a blank password.
		if user, _, ok := r.BasicAuth(); !ok || user != "master-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user_tokens/generate":
			_ = r.ParseForm()
			if r.Form.Get("name") == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"login":"u","name":"` + r.Form.Get("name") + `","token":"sq_minted"}`))
		case "/api/user_tokens/revoke":
			w.WriteHeader(http.StatusNoContent)
		case "/api/user_tokens/search":
			_, _ = w.Write([]byte(`{"userTokens":[{"name":"t1"},{"name":"t2"}]}`))
		case "/api/boom":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errors":[{"msg":"nope"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
	return httptest.NewServer(http.HandlerFunc(h))
}

func testSonar(ts *httptest.Server) *SonarCloud {
	return NewSonarCloud(SonarCloudConfig{Token: "master-token", BaseURL: ts.URL})
}

func TestSonarCloud_MintToken(t *testing.T) {
	ts := sonarStub(t)
	defer ts.Close()
	sc := testSonar(ts)

	tok, err := sc.MintToken(context.Background(), "munchbox-ci/o/r/1", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	if tok != "sq_minted" {
		t.Errorf("token = %q, want sq_minted", tok)
	}
}

func TestSonarCloud_RevokeToken(t *testing.T) {
	ts := sonarStub(t)
	defer ts.Close()
	sc := testSonar(ts)

	if err := sc.RevokeToken(context.Background(), "munchbox-ci/o/r/1"); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
}

func TestSonarCloud_ListTokenNames(t *testing.T) {
	ts := sonarStub(t)
	defer ts.Close()
	sc := testSonar(ts)

	names, err := sc.ListTokenNames(context.Background())
	if err != nil {
		t.Fatalf("ListTokenNames: %v", err)
	}
	if len(names) != 2 || names[0] != "t1" || names[1] != "t2" {
		t.Errorf("names = %v, want [t1 t2]", names)
	}
}

func TestSonarCloud_ErrorStatusSurfaces(t *testing.T) {
	ts := sonarStub(t)
	defer ts.Close()
	sc := testSonar(ts)

	// Point at the 400 path via a raw GET through the search helper's sibling:
	// any non-2xx must surface as an error carrying the status.
	err := sc.get(context.Background(), "/api/boom", new(struct{}))
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Errorf("err = %v, want one mentioning 400", err)
	}
}

func TestSonarCloud_BadAuth(t *testing.T) {
	ts := sonarStub(t)
	defer ts.Close()
	sc := NewSonarCloud(SonarCloudConfig{Token: "wrong", BaseURL: ts.URL})

	if _, err := sc.ListTokenNames(context.Background()); err == nil {
		t.Error("expected an error when the master token is rejected")
	}
}

func TestSonarCloud_DefaultBaseURL(t *testing.T) {
	sc := NewSonarCloud(SonarCloudConfig{Token: "x"})
	if sc.base != "https://sonarcloud.io" {
		t.Errorf("base = %q, want https://sonarcloud.io", sc.base)
	}
}
