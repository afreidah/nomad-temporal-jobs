// -------------------------------------------------------------------------------
// Shared GitHub App Client - NewGitHubFromVault Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Drives NewGitHubFromVault with an in-memory KV reader: a well-formed secret
// builds a client with the stored installation ID, and every malformed or
// missing field (or a failed read) surfaces as an error. installation_id is
// always set on the success path so NewGitHub never calls api.github.com.
// -------------------------------------------------------------------------------

package git

import (
	"context"
	"errors"
	"testing"
)

// fakeKV serves one fixed secret (or a fixed error) for any path.
type fakeKV struct {
	data map[string]any
	err  error
}

func (f fakeKV) ReadKV(_ context.Context, _ string) (map[string]any, error) {
	return f.data, f.err
}

func TestNewGitHubFromVault(t *testing.T) {
	gh, err := NewGitHubFromVault(context.Background(), fakeKV{data: map[string]any{
		"app_id":          "123",
		"installation_id": "456",
		"private_key":     string(testAppKey(t)),
	}}, "secret/github-app")
	if err != nil {
		t.Fatalf("NewGitHubFromVault: %v", err)
	}
	if gh.instID != 456 {
		t.Errorf("instID = %d, want 456", gh.instID)
	}
	if gh.baseURL != "" {
		t.Errorf("baseURL = %q, want empty", gh.baseURL)
	}
}

func TestNewGitHubFromVault_Errors(t *testing.T) {
	key := string(testAppKey(t))
	readErr := errors.New("vault sealed")

	tests := []struct {
		name string
		kv   fakeKV
	}{
		{"read fails", fakeKV{err: readErr}},
		{"app_id missing", fakeKV{data: map[string]any{"installation_id": "456", "private_key": key}}},
		{"app_id not a string", fakeKV{data: map[string]any{"app_id": 123, "installation_id": "456", "private_key": key}}},
		{"app_id not numeric", fakeKV{data: map[string]any{"app_id": "abc", "installation_id": "456", "private_key": key}}},
		{"installation_id not numeric", fakeKV{data: map[string]any{"app_id": "123", "installation_id": "xyz", "private_key": key}}},
		{"private_key missing", fakeKV{data: map[string]any{"app_id": "123", "installation_id": "456"}}},
		{"private_key not PEM", fakeKV{data: map[string]any{"app_id": "123", "installation_id": "456", "private_key": "not a key"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gh, err := NewGitHubFromVault(context.Background(), tt.kv, "secret/github-app")
			if err == nil {
				t.Fatalf("expected an error, got client %+v", gh)
			}
			if tt.kv.err != nil && !errors.Is(err, tt.kv.err) {
				t.Errorf("err = %v, want wrapping %v", err, tt.kv.err)
			}
		})
	}
}

func TestKVString(t *testing.T) {
	m := map[string]any{"s": "value", "n": 42}
	if got := kvString(m, "s"); got != "value" {
		t.Errorf("kvString(s) = %q, want value", got)
	}
	if got := kvString(m, "n"); got != "" {
		t.Errorf("kvString(n) = %q, want empty for non-string", got)
	}
	if got := kvString(m, "absent"); got != "" {
		t.Errorf("kvString(absent) = %q, want empty", got)
	}
}
