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
	"net/http"
	"os"

	consulapi "github.com/hashicorp/consul/api"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
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

	cfg.HttpClient = &http.Client{
		Transport: otelhttp.NewTransport(
			http.DefaultTransport,
			otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
				return fmt.Sprintf("consul.%s", r.URL.Path)
			}),
		),
	}

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
