// -------------------------------------------------------------------------------
// Shared Vault Client - Self-Authenticating, Instrumented Vault Client Factory
//
// Author: Alex Freidah
//
// Creates a Vault API client authenticated with the worker's Nomad Workload
// Identity token, so a worker carries only its identity and no static service
// tokens are templated into its Nomad job. Other shared clients (Nomad,
// Consul, Postgres) pull their own credentials through this client. The HTTP
// transport is OTel-instrumented so Vault reads and writes appear as edges in
// the Tempo service graph, and a background refresher reloads the token from
// its file as Nomad rotates it.
// -------------------------------------------------------------------------------

package shared

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	vault "github.com/hashicorp/vault/api"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// -------------------------------------------------------------------------
// ERRORS
// -------------------------------------------------------------------------

// ErrNoVaultToken is returned when no Workload Identity token is available to
// authenticate the Vault client.
var ErrNoVaultToken = errors.New("no vault token available (set VAULT_TOKEN or VAULT_TOKEN_FILE)")

// ErrSecretFieldMissing is returned when a requested KV field is absent or is
// not a string value.
var ErrSecretFieldMissing = errors.New("vault secret field missing")

// -------------------------------------------------------------------------
// CLIENT
// -------------------------------------------------------------------------

// VaultClient wraps a Vault API client with typed KV v2 access. Construct it
// with NewVaultClient; the zero value is not usable.
type VaultClient struct {
	api       *vault.Client
	mount     string
	tokenFile string
}

// NewVaultClient builds an OTel-instrumented Vault client authenticated with
// the worker's Workload Identity token. VAULT_ADDR defaults to the in-cluster
// frontend; the token comes from VAULT_TOKEN or the file at VAULT_TOKEN_FILE
// (Nomad writes and rotates the WI token there). The KV v2 mount defaults to
// "secret" and is overridable with VAULT_KV_MOUNT.
func NewVaultClient() (*VaultClient, error) {
	cfg := vault.DefaultConfig()
	if cfg.Error != nil {
		return nil, fmt.Errorf("default vault config: %w", cfg.Error)
	}
	cfg.Address = "https://vault.service.consul:8200"
	if addr := os.Getenv("VAULT_ADDR"); addr != "" {
		cfg.Address = addr
	}

	// DefaultConfig has already applied TLS from VAULT_CACERT et al onto the
	// default *http.Transport. Wrap that configured transport with OTel last:
	// Vault's ConfigureTLS only understands a *http.Transport, so it must run
	// before the otelhttp wrapper, not after.
	cfg.HttpClient.Transport = otelhttp.NewTransport(
		cfg.HttpClient.Transport,
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return "vault." + r.URL.Path
		}),
	)

	c, err := vault.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("create vault client: %w", err)
	}

	token, err := workloadToken()
	if err != nil {
		return nil, err
	}
	c.SetToken(token)

	mount := os.Getenv("VAULT_KV_MOUNT")
	if mount == "" {
		mount = "secret"
	}

	return &VaultClient{api: c, mount: mount, tokenFile: os.Getenv("VAULT_TOKEN_FILE")}, nil
}

// workloadToken returns the Vault token from VAULT_TOKEN, falling back to the
// file named by VAULT_TOKEN_FILE.
func workloadToken() (string, error) {
	if t := os.Getenv("VAULT_TOKEN"); t != "" {
		return strings.TrimSpace(t), nil
	}
	if path := os.Getenv("VAULT_TOKEN_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read vault token file: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return "", ErrNoVaultToken
}

// -------------------------------------------------------------------------
// TOKEN REFRESH
// -------------------------------------------------------------------------

// StartTokenRefresher reloads the token from VAULT_TOKEN_FILE on an interval
// for the life of ctx, picking up rotations Nomad makes before the lease
// ends. It is a no-op when the token came from VAULT_TOKEN rather than a file.
// Run it in a goroutine.
func (v *VaultClient) StartTokenRefresher(ctx context.Context, interval time.Duration, log *slog.Logger) {
	if v.tokenFile == "" {
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b, err := os.ReadFile(v.tokenFile)
			if err != nil {
				log.WarnContext(ctx, "vault token refresh failed", "error", err.Error())
				continue
			}
			if token := strings.TrimSpace(string(b)); token != v.api.Token() {
				v.api.SetToken(token)
				log.InfoContext(ctx, "vault token reloaded from file")
			}
		}
	}
}

// -------------------------------------------------------------------------
// KV V2 ACCESS
// -------------------------------------------------------------------------

// ReadKV reads a KV v2 secret at path under the configured mount and returns
// its data map.
func (v *VaultClient) ReadKV(ctx context.Context, path string) (map[string]any, error) {
	s, err := v.api.KVv2(v.mount).Get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("read %s/%s: %w", v.mount, path, err)
	}
	return s.Data, nil
}

// ReadKVMaybe reads a KV v2 secret, returning found=false with no error when
// the secret does not exist. This lets callers distinguish an absent secret
// from a real Vault failure (a down Vault must not read as "absent").
func (v *VaultClient) ReadKVMaybe(ctx context.Context, path string) (map[string]any, bool, error) {
	dataPath := fmt.Sprintf("%s/data/%s", v.mount, path)
	secret, err := v.api.Logical().ReadWithContext(ctx, dataPath)
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", dataPath, err)
	}
	if secret == nil || secret.Data == nil {
		return nil, false, nil
	}
	data, ok := secret.Data["data"].(map[string]any)
	if !ok {
		return nil, false, nil
	}
	return data, true, nil
}

// ReadKVField reads a single string field from a KV v2 secret. It returns
// ErrSecretFieldMissing when the field is absent or not a string.
func (v *VaultClient) ReadKVField(ctx context.Context, path, field string) (string, error) {
	data, err := v.ReadKV(ctx, path)
	if err != nil {
		return "", err
	}
	raw, ok := data[field]
	if !ok {
		return "", fmt.Errorf("%w: %s/%s field %q", ErrSecretFieldMissing, v.mount, path, field)
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%w: %s/%s field %q is %T not string", ErrSecretFieldMissing, v.mount, path, field, raw)
	}
	return s, nil
}

// WriteKV writes data as a KV v2 secret at path under the configured mount.
func (v *VaultClient) WriteKV(ctx context.Context, path string, data map[string]any) error {
	if _, err := v.api.KVv2(v.mount).Put(ctx, path, data); err != nil {
		return fmt.Errorf("write %s/%s: %w", v.mount, path, err)
	}
	return nil
}

// API returns the underlying Vault client for operations beyond KV v2 (auth,
// sys, logical). Prefer the typed helpers above where they suffice.
func (v *VaultClient) API() *vault.Client {
	return v.api
}
