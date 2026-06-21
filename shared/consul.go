// -------------------------------------------------------------------------------
// Shared Consul Client - Instrumented Consul Client with Vault-Sourced Token
//
// Author: Alex Freidah
//
// Creates a Consul API client whose ACL token is fetched from Vault through
// the shared Vault client, so a worker carries no static Consul token. The
// Consul address and TLS settings come from the standard CONSUL_* environment
// variables (default: the node's local agent), and the HTTP transport is
// OTel-instrumented so Consul calls appear as edges in the Tempo service graph.
// -------------------------------------------------------------------------------

package shared

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	consulapi "github.com/hashicorp/consul/api"
)

// -------------------------------------------------------------------------
// DEFAULTS
// -------------------------------------------------------------------------

// Default Vault location of the Consul ACL token. Override per worker with
// CONSUL_TOKEN_VAULT_PATH / CONSUL_TOKEN_VAULT_FIELD.
const (
	defaultConsulTokenPath  = "consul/nomad-client-token"
	defaultConsulTokenField = "token"
)

// -------------------------------------------------------------------------
// CLIENT
// -------------------------------------------------------------------------

// NewConsulClient creates an OTel-instrumented Consul API client whose ACL
// token is read from Vault via vc. When vc is nil the token falls back to the
// standard CONSUL_HTTP_TOKEN env var (local/test). Address and TLS come from
// the CONSUL_* environment.
func NewConsulClient(ctx context.Context, vc *VaultClient) (*consulapi.Client, error) {
	cfg := consulapi.DefaultConfig()

	cfg.HttpClient = &http.Client{Transport: otelTransport("consul", nil)}

	if vc != nil {
		path := os.Getenv("CONSUL_TOKEN_VAULT_PATH")
		if path == "" {
			path = defaultConsulTokenPath
		}
		field := os.Getenv("CONSUL_TOKEN_VAULT_FIELD")
		if field == "" {
			field = defaultConsulTokenField
		}

		token, err := vc.ReadKVField(ctx, path, field)
		if err != nil {
			return nil, fmt.Errorf("fetch consul token from vault: %w", err)
		}
		cfg.Token = token
	}

	client, err := consulapi.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("create consul client: %w", err)
	}
	return client, nil
}

// Consul wraps the instrumented Consul client with the operations workers use.
// Workers consume it through their own narrow interfaces.
type Consul struct {
	client *consulapi.Client
}

// NewConsul builds a Consul service. Token sourcing follows NewConsulClient
// (from Vault via vc, or CONSUL_HTTP_TOKEN when vc is nil).
func NewConsul(ctx context.Context, vc *VaultClient) (*Consul, error) {
	client, err := NewConsulClient(ctx, vc)
	if err != nil {
		return nil, err
	}
	return &Consul{client: client}, nil
}

// SaveSnapshot streams a Raft snapshot of the Consul cluster state (which
// includes Vault's storage, as Vault uses Consul as its backend) from the API.
// The caller must close the returned reader.
func (c *Consul) SaveSnapshot(ctx context.Context) (io.ReadCloser, error) {
	snap, _, err := c.client.Snapshot().Save((&consulapi.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return nil, err
	}
	return snap, nil
}

// KVGet reads the raw value at a Consul KV key. The boolean is false (with a nil
// value and nil error) when the key does not exist, so callers can distinguish
// an absent key from an empty value.
func (c *Consul) KVGet(ctx context.Context, key string) ([]byte, bool, error) {
	pair, _, err := c.client.KV().Get(key, (&consulapi.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return nil, false, fmt.Errorf("consul kv get %s: %w", key, err)
	}
	if pair == nil {
		return nil, false, nil
	}
	return pair.Value, true, nil
}
