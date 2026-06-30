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

	"munchbox/temporal-workers/shared/client/consul"
	"munchbox/temporal-workers/shared/client/git"
	"munchbox/temporal-workers/shared/client/sonarcloud"
	"munchbox/temporal-workers/shared/client/vault"

	"go.temporal.io/sdk/worker"
)

func main() {
	err := shared.RunWorker(context.Background(), shared.WorkerSpec{
		Service:   "github-token-renewer",
		TaskQueue: "github-token-renewer-task-queue",
		Register: func(ctx context.Context, slogger *slog.Logger, w worker.Worker) (func(), error) {
			// Vault (Workload Identity); the GitHub App key is pulled through it,
			// so the Nomad job carries only its identity.
			vc, err := vault.NewVaultWithRefresher(ctx, slogger)
			if err != nil {
				return nil, err
			}

			gh, err := newGitHub(ctx, vc)
			if err != nil {
				return nil, err
			}
			// Consul KV (the repo list) uses the local agent's default ACL token
			// over host networking -- no per-worker Consul token to manage.
			consul, err := consul.NewConsul(ctx, nil)
			if err != nil {
				return nil, err
			}

			cfg := activities.Config{
				GitHub:      gh,
				Repos:       consul,
				RepoListKey: os.Getenv("REPO_LIST_KEY"),
				SecretName:  os.Getenv("SECRET_NAME"),
			}

			// SonarCloud renewal is additive and optional: enable it only when
			// the master token and org are both available, so this image can ship
			// before the Vault secret + policy exist without breaking GitHub
			// renewal. When disabled, the workflow is simply not registered.
			sonarEnabled := configureSonar(ctx, vc, &cfg, slogger)

			acts := activities.New(cfg)
			w.RegisterWorkflow(workflows.RenewTokens)
			if sonarEnabled {
				w.RegisterWorkflow(workflows.RenewSonarCloudTokens)
			}
			w.RegisterActivity(acts)
			return nil, nil
		},
	})
	if err != nil {
		log.Fatalln(err)
	}
}

// configureSonar enables SonarCloud token renewal on cfg when the master token
// is present in Vault, returning whether it was enabled. A missing token
// disables the feature with a warning rather than an error, so the worker still
// renews GitHub tokens when SonarCloud isn't wired up yet. Defaults: secret
// SONAR_TOKEN, 90-day token TTL.
func configureSonar(ctx context.Context, vc *vault.VaultClient, cfg *activities.Config, log *slog.Logger) bool {
	path := cmp.Or(os.Getenv("SONARCLOUD_TOKEN_VAULT_PATH"), "sonarcloud/token")
	data, found, err := vc.ReadKVMaybe(ctx, path)
	if err != nil {
		log.Warn("SonarCloud token renewal disabled (Vault read failed)", "path", path, "error", err)
		return false
	}
	token := vaultString(data, "token")
	if !found || token == "" {
		log.Warn("SonarCloud token renewal disabled (no token in Vault)", "path", path)
		return false
	}

	ttlDays := 90
	if s := os.Getenv("SONARCLOUD_TOKEN_TTL_DAYS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			ttlDays = n
		} else {
			log.Warn("Invalid SONARCLOUD_TOKEN_TTL_DAYS, using default", "value", s, "default", ttlDays)
		}
	}

	cfg.Sonar = sonarcloud.NewSonarCloud(sonarcloud.SonarCloudConfig{
		Token:   token,
		BaseURL: os.Getenv("SONARCLOUD_BASE_URL"),
	})
	cfg.SonarSecretName = cmp.Or(os.Getenv("SONAR_SECRET_NAME"), "SONAR_TOKEN")
	cfg.SonarTokenTTL = time.Duration(ttlDays) * 24 * time.Hour

	log.Info("SonarCloud token renewal enabled", "secret", cfg.SonarSecretName, "ttl_days", ttlDays)
	return true
}

// newGitHub reads the GitHub App credentials from Vault and builds the App
// client. The Vault KV path holds app_id, installation_id (optional, discovered
// when absent), and private_key (PEM).
func newGitHub(ctx context.Context, vc *vault.VaultClient) (*git.GitHub, error) {
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

	return git.NewGitHub(ctx, git.GitHubConfig{
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
