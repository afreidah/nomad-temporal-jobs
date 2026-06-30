// -------------------------------------------------------------------------------
// Shared GitHub App Client - Construct From Vault-Stored Credentials
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Every worker that uses the GitHub App reads the same credential shape from
// Vault KV -- app_id, optional installation_id, and the PEM private_key -- and
// builds an App client from it. This constructor owns that read-and-parse so the
// boilerplate lives once instead of being copied into each worker's main(). It
// depends on a narrow KV-read interface, not the concrete Vault client, so the
// git package stays decoupled from vault.
// -------------------------------------------------------------------------------

package git

import (
	"context"
	"fmt"
	"strconv"
)

// kvReader is the Vault KV surface NewGitHubFromVault needs: read a secret's
// fields by path. *vault.VaultClient satisfies it structurally.
type kvReader interface {
	ReadKV(ctx context.Context, path string) (map[string]any, error)
}

// NewGitHubFromVault builds an App client from credentials stored in Vault KV at
// path. The secret holds app_id, installation_id (optional -- discovered when
// absent), and private_key (PEM).
func NewGitHubFromVault(ctx context.Context, kv kvReader, path string) (*GitHub, error) {
	data, err := kv.ReadKV(ctx, path)
	if err != nil {
		return nil, err
	}

	appID, err := strconv.ParseInt(kvString(data, "app_id"), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("github app_id from %s: %w", path, err)
	}
	var instID int64
	if s := kvString(data, "installation_id"); s != "" {
		if instID, err = strconv.ParseInt(s, 10, 64); err != nil {
			return nil, fmt.Errorf("github installation_id from %s: %w", path, err)
		}
	}
	key := kvString(data, "private_key")
	if key == "" {
		return nil, fmt.Errorf("github private_key missing at %s", path)
	}

	return NewGitHub(ctx, GitHubConfig{
		AppID:          appID,
		InstallationID: instID,
		PrivateKeyPEM:  []byte(key),
	})
}

// kvString reads a string field from a Vault KV data map (the Vault KV CLI
// stores values as strings).
func kvString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}
