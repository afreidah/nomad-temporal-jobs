// -------------------------------------------------------------------------------
// Cert Acquirer Worker - Temporal Worker for Wildcard Certificate Renewal
//
// Author: Alex Freidah
//
// Starts a Temporal worker on the cert-task-queue that issues the
// *.munchbox.cc wildcard via ACME DNS-01 and publishes it to Vault. The
// worker authenticates to Vault with its Nomad Workload Identity token and
// pulls every other credential (the Cloudflare DNS token) through that client,
// so the only secret the Nomad job carries is its identity. The shared runtime
// owns tracing, logging, metrics, and the Temporal client.
// -------------------------------------------------------------------------------

package main

import (
	"context"
	"log"
	"log/slog"
	"os"

	"munchbox/temporal-workers/certacquirer/activities"
	"munchbox/temporal-workers/certacquirer/workflows"
	"munchbox/temporal-workers/shared"

	"munchbox/temporal-workers/shared/client/vault"

	"go.temporal.io/sdk/worker"
)

func main() {
	err := shared.RunWorker(context.Background(), shared.WorkerSpec{
		Service:   "cert-acquirer-worker",
		TaskQueue: "cert-task-queue",
		Register: func(ctx context.Context, slogger *slog.Logger, w worker.Worker) (func(), error) {
			// Vault client (Workload Identity); other creds are pulled through it.
			vc, err := vault.NewVaultWithRefresher(ctx, slogger)
			if err != nil {
				return nil, err
			}

			acts := activities.New(activities.Config{
				Vault:    vc,
				CADirURL: os.Getenv("ACME_CA_DIR_URL"),
			})

			w.RegisterWorkflow(workflows.CertAcquirer)
			w.RegisterActivity(acts)
			return nil, nil
		},
	})
	if err != nil {
		log.Fatalln(err)
	}
}
