// -------------------------------------------------------------------------------
// Shared Vault Client - HTTP Integration Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Drives the KV v2 helpers against an httptest server returning canned KV v2
// responses, plus the token helpers. Hermetic -- no real Vault.
// -------------------------------------------------------------------------------

package shared

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	vault "github.com/hashicorp/vault/api"
)

func vaultStub() *httptest.Server {
	const kvRead = `{"data":{"data":{"username":"admin","password":"s3cr3t"},"metadata":{"version":1,"created_time":"2020-01-01T00:00:00Z"}}}`
	h := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/secret/data/missing"):
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[]}`)
		case strings.HasPrefix(r.URL.Path, "/v1/secret/data/"):
			if r.Method == http.MethodGet {
				fmt.Fprint(w, kvRead)
			} else {
				fmt.Fprint(w, `{"data":{"version":1,"created_time":"2020-01-01T00:00:00Z"}}`)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[]}`)
		}
	}
	return httptest.NewServer(http.HandlerFunc(h))
}

func testVault(t *testing.T, ts *httptest.Server) *VaultClient {
	t.Helper()
	c, err := vault.NewClient(&vault.Config{Address: ts.URL})
	if err != nil {
		t.Fatalf("vault.NewClient: %v", err)
	}
	c.SetToken("test-token")
	return &VaultClient{api: c, mount: "secret"}
}

func TestVault_ReadKV_and_Field(t *testing.T) {
	ts := vaultStub()
	defer ts.Close()
	v := testVault(t, ts)

	data, err := v.ReadKV(context.Background(), "myapp")
	if err != nil {
		t.Fatalf("ReadKV: %v", err)
	}
	if data["username"] != "admin" {
		t.Errorf("username = %v, want admin", data["username"])
	}

	field, err := v.ReadKVField(context.Background(), "myapp", "password")
	if err != nil || field != "s3cr3t" {
		t.Errorf("ReadKVField = (%q, %v), want (s3cr3t, nil)", field, err)
	}
	if _, err := v.ReadKVField(context.Background(), "myapp", "absent"); !errors.Is(err, ErrSecretFieldMissing) {
		t.Errorf("missing field err = %v, want ErrSecretFieldMissing", err)
	}
}

func TestVault_ReadKVMaybe(t *testing.T) {
	ts := vaultStub()
	defer ts.Close()
	v := testVault(t, ts)

	data, found, err := v.ReadKVMaybe(context.Background(), "myapp")
	if err != nil || !found || data["password"] != "s3cr3t" {
		t.Errorf("ReadKVMaybe(myapp) = (%v, %v, %v), want found secret", data, found, err)
	}

	_, found, err = v.ReadKVMaybe(context.Background(), "missing")
	if err != nil || found {
		t.Errorf("ReadKVMaybe(missing) = (found=%v, err=%v), want (false, nil)", found, err)
	}
}

func TestVault_WriteKV_and_API(t *testing.T) {
	ts := vaultStub()
	defer ts.Close()
	v := testVault(t, ts)

	if err := v.WriteKV(context.Background(), "myapp", map[string]any{"k": "v"}); err != nil {
		t.Fatalf("WriteKV: %v", err)
	}
	if v.API() == nil {
		t.Error("API() returned nil")
	}
}

func TestWorkloadToken(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "env-token")
	if tok, err := workloadToken(); err != nil || tok != "env-token" {
		t.Errorf("from env = (%q, %v), want (env-token, nil)", tok, err)
	}

	t.Setenv("VAULT_TOKEN", "")
	t.Setenv("VAULT_TOKEN_FILE", writeTemp(t, "tok", []byte("file-token\n")))
	if tok, err := workloadToken(); err != nil || tok != "file-token" {
		t.Errorf("from file = (%q, %v), want (file-token, nil)", tok, err)
	}

	t.Setenv("VAULT_TOKEN_FILE", "")
	if _, err := workloadToken(); !errors.Is(err, ErrNoVaultToken) {
		t.Errorf("no token err = %v, want ErrNoVaultToken", err)
	}
}

func TestStartTokenRefresher(t *testing.T) {
	// No-op when the token didn't come from a file.
	(&VaultClient{}).StartTokenRefresher(context.Background(), time.Hour, slog.Default())

	// With a token file, a cancelled context returns promptly.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	(&VaultClient{tokenFile: writeTemp(t, "tok", []byte("t"))}).
		StartTokenRefresher(ctx, time.Hour, slog.Default())
}
