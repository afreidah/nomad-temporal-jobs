// -------------------------------------------------------------------------------
// Shared Vault Client - HTTP Integration Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Drives the KV v2 helpers against an httptest server returning canned KV v2
// responses, plus the token helpers. Hermetic -- no real Vault.
// -------------------------------------------------------------------------------

package vault

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	vault "github.com/hashicorp/vault/api"
)

// writeTemp writes data to a temp file under t.TempDir() and returns its path.
func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

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

// Ticks reload a rotated token from the file and survive a failed read.
func TestStartTokenRefresher_Ticks(t *testing.T) {
	ts := vaultStub()
	defer ts.Close()

	v := testVault(t, ts)
	v.tokenFile = writeTemp(t, "tok", []byte("rotated\n"))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	v.StartTokenRefresher(ctx, time.Millisecond, slog.Default())
	if got := v.api.Token(); got != "rotated" {
		t.Errorf("token = %q, want rotated", got)
	}

	v.tokenFile = filepath.Join(t.TempDir(), "absent")
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	v.StartTokenRefresher(ctx, time.Millisecond, slog.Default())
	if got := v.api.Token(); got != "rotated" {
		t.Errorf("token after failed read = %q, want rotated", got)
	}
}

// statusVault returns a client whose server answers every request with code
// and body.
func statusVault(t *testing.T, code int, body string) *VaultClient {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(ts.Close)
	return testVault(t, ts)
}

func TestVault_ServerErrors(t *testing.T) {
	v := statusVault(t, http.StatusInternalServerError, `{"errors":["boom"]}`)
	ctx := context.Background()

	if _, err := v.ReadKV(ctx, "myapp"); err == nil {
		t.Error("ReadKV: expected an error")
	}
	if _, err := v.ReadKVField(ctx, "myapp", "password"); err == nil {
		t.Error("ReadKVField: expected an error")
	}
	if _, _, err := v.ReadKVMaybe(ctx, "myapp"); err == nil {
		t.Error("ReadKVMaybe: expected an error")
	}
	if err := v.WriteKV(ctx, "myapp", map[string]any{"k": "v"}); err == nil {
		t.Error("WriteKV: expected an error")
	}
}

func TestVault_ReadKVField_NotString(t *testing.T) {
	v := statusVault(t, http.StatusOK, `{"data":{"data":{"port":5432},"metadata":{"version":1}}}`)
	if _, err := v.ReadKVField(context.Background(), "myapp", "port"); !errors.Is(err, ErrSecretFieldMissing) {
		t.Errorf("err = %v, want ErrSecretFieldMissing", err)
	}
}

// A KV v2 response whose inner data is not a map reads as absent.
func TestVault_ReadKVMaybe_MalformedData(t *testing.T) {
	v := statusVault(t, http.StatusOK, `{"data":{"data":"x"}}`)
	if _, found, err := v.ReadKVMaybe(context.Background(), "myapp"); err != nil || found {
		t.Errorf("ReadKVMaybe = (found=%v, err=%v), want (false, nil)", found, err)
	}
}

func TestWorkloadToken_FileReadError(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "")
	t.Setenv("VAULT_TOKEN_FILE", filepath.Join(t.TempDir(), "absent"))
	if _, err := workloadToken(); err == nil {
		t.Error("expected an error for an unreadable token file")
	}
}
