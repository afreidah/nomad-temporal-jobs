// -------------------------------------------------------------------------------
// GitHub Token Renewer Worker - Temporal Worker for CI-Token Renewal
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Starts a Temporal worker on the github-token-renewer-task-queue that mints
// fresh GitHub App installation tokens and writes them to each managed repo's
// Actions secret, so the CI/release token never expires. The worker
// authenticates to Vault with its Nomad Workload Identity token and pulls the
// GitHub App key and the Consul ACL token through that client, so the only
// secret the Nomad job carries is its identity. The shared runtime owns tracing,
// logging, metrics, and the Temporal client.
// -------------------------------------------------------------------------------

package main

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strconv"
	"time"

	"munchbox/temporal-workers/ghtokenrenewer/activities"
	"munchbox/temporal-workers/ghtokenrenewer/workflows"
	"munchbox/temporal-workers/shared"

	"go.temporal.io/sdk/worker"
)

const tokenRefreshInterval = time.Minute

func main() {
	err := shared.RunWorker(context.Background(), shared.WorkerSpec{
		Service:   "github-token-renewer",
		TaskQueue: "github-token-renewer-task-queue",
		Register: func(ctx context.Context, slogger *slog.Logger, w worker.Worker) (func(), error) {
			// Vault (Workload Identity); the GitHub App key and the Consul token
			// are pulled through it, so the Nomad job carries only its identity.
			vc, err := shared.NewVaultClient()
			if err != nil {
				return nil, err
			}
			go vc.StartTokenRefresher(ctx, tokenRefreshInterval, slogger)

			gh, err := newGitHub(ctx, vc)
			if err != nil {
				return nil, err
			}
			consul, err := shared.NewConsul(ctx, vc)
			if err != nil {
				return nil, err
			}

			acts := activities.New(activities.Config{
				GitHub:      gh,
				Repos:       consul,
				RepoListKey: os.Getenv("REPO_LIST_KEY"),
				SecretName:  os.Getenv("SECRET_NAME"),
			})

			w.RegisterWorkflow(workflows.RenewTokens)
			w.RegisterActivity(acts)
			return nil, nil
		},
	})
	if err != nil {
		log.Fatalln(err)
	}
}

// newGitHub reads the GitHub App credentials from Vault and builds the App
// client. The Vault KV path holds app_id, installation_id (optional, discovered
// when absent), and private_key (PEM).
func newGitHub(ctx context.Context, vc *shared.VaultClient) (*shared.GitHub, error) {
	path := cmp.Or(os.Getenv("GITHUB_APP_VAULT_PATH"), "github/token-renewer-app")
	data, err := vc.ReadKV(ctx, path)
	if err != nil {
		return nil, err
	}

	appID, err := strconv.ParseInt(vaultString(data, "app_id"), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("github app_id from %s: %w", path, err)
	}
	var instID int64
	if s := vaultString(data, "installation_id"); s != "" {
		if instID, err = strconv.ParseInt(s, 10, 64); err != nil {
			return nil, fmt.Errorf("github installation_id from %s: %w", path, err)
		}
	}
	key := vaultString(data, "private_key")
	if key == "" {
		return nil, fmt.Errorf("github private_key missing at %s", path)
	}

	return shared.NewGitHub(ctx, shared.GitHubConfig{
		AppID:          appID,
		InstallationID: instID,
		PrivateKeyPEM:  []byte(key),
	})
}

// vaultString reads a string field from a Vault KV data map (the Vault KV CLI
// stores values as strings).
func vaultString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}
