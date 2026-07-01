// -------------------------------------------------------------------------------
// Runner Scaler Worker - Temporal Worker for On-Demand CI Runners
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Starts a Temporal worker on the ci-runner-scaler-task-queue that polls GitHub
// for queued self-hosted Actions jobs and dispatches one ephemeral Nomad runner
// per job, leaning on Temporal's workflow-ID dedup so there is no external state
// store. The worker authenticates to Vault with its Nomad Workload Identity and
// pulls the GitHub App key through that client (the same App the token renewer
// uses), so the only secret the Nomad job carries is its identity. Consul (the
// watched repos + profiles) uses the local agent's default token; Nomad uses its
// NOMAD_TOKEN. The shared runtime owns tracing, logging, metrics, and the
// Temporal client.
// -------------------------------------------------------------------------------

package main

import (
	"cmp"
	"context"
	"log"
	"log/slog"
	"os"

	"munchbox/temporal-workers/runnerscaler/activities"
	"munchbox/temporal-workers/runnerscaler/workflows"
	"munchbox/temporal-workers/shared"

	"munchbox/temporal-workers/shared/client/consul"
	"munchbox/temporal-workers/shared/client/git"
	"munchbox/temporal-workers/shared/client/nomad"
	"munchbox/temporal-workers/shared/client/vault"

	"go.temporal.io/sdk/worker"
)

func main() {
	err := shared.RunWorker(context.Background(), shared.WorkerSpec{
		Service:   "ci-runner-scaler",
		TaskQueue: "ci-runner-scaler-task-queue",
		Register: func(ctx context.Context, slogger *slog.Logger, w worker.Worker) (func(), error) {
			// Vault (Workload Identity); the GitHub App key is pulled through it,
			// so the Nomad job carries only its identity.
			vc, err := vault.NewVaultWithRefresher(ctx, slogger)
			if err != nil {
				return nil, err
			}
			// Reuses the token-renewer App (also needs Administration + Actions perms).
			appPath := cmp.Or(os.Getenv("GITHUB_APP_VAULT_PATH"), "github/token-renewer-app")
			gh, err := git.NewGitHubFromVault(ctx, vc, appPath)
			if err != nil {
				return nil, err
			}
			// Consul KV (watched repos + runner profiles) uses the local agent's
			// default ACL token over host networking -- no per-worker Consul token.
			kv, err := consul.NewConsul(ctx, nil)
			if err != nil {
				return nil, err
			}
			nm, err := nomad.NewNomad()
			if err != nil {
				return nil, err
			}

			acts := activities.New(activities.Config{
				GitHub:      gh,
				KV:          kv,
				Nomad:       nm,
				RepoListKey: os.Getenv("RUNNERS_REPOS_KEY"),
				ProfilesKey: os.Getenv("RUNNERS_PROFILES_KEY"),
				RunnerJobID: os.Getenv("RUNNER_JOB_ID"),
			})
			w.RegisterWorkflow(workflows.PollAndDispatch)
			w.RegisterWorkflow(workflows.HandleRunner)
			w.RegisterActivity(acts)
			return nil, nil
		},
	})
	if err != nil {
		log.Fatalln(err)
	}
}
