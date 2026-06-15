// -------------------------------------------------------------------------------
// Node Cleanup Worker - Temporal Worker for Orphaned Data Removal
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Starts a Temporal worker on the cleanup-task-queue. The shared runtime owns
// tracing, logging, metrics, and the Temporal client; this file only builds
// the cleanup activities and registers the cleanup, registry-GC, postgres-
// maintenance, and aptly-cleanup workflows. Requires SSH key access to Nomad
// client nodes for remote cleanup execution.
// -------------------------------------------------------------------------------

package main

import (
	"cmp"
	"context"
	"log"
	"log/slog"
	"os"

	"munchbox/temporal-workers/nodecleanup/activities"
	"munchbox/temporal-workers/nodecleanup/workflows"
	"munchbox/temporal-workers/shared"

	"go.temporal.io/sdk/worker"
)

func main() {
	err := shared.RunWorker(context.Background(), shared.WorkerSpec{
		Service:   "cleanup-worker",
		TaskQueue: "cleanup-task-queue",
		Register: func(_ context.Context, _ *slog.Logger, w worker.Worker) (func(), error) {
			acts := activities.New(activities.Config{
				SSHKeyPath:    cmp.Or(os.Getenv("SSH_KEY_PATH"), "/root/.ssh/id_ed25519"),
				SSHCertPath:   cmp.Or(os.Getenv("SSH_CERT_PATH"), "/root/.ssh/id_ed25519-cert.pub"),
				SSHHostCAPath: cmp.Or(os.Getenv("SSH_HOST_CA_PATH"), "/root/.ssh/ssh-host-ca.pub"),

				PostgresHost:        cmp.Or(os.Getenv("PG_HOST"), "postgres-primary.service.consul"),
				PostgresPort:        cmp.Or(os.Getenv("PG_PORT"), "5432"),
				PostgresUser:        cmp.Or(os.Getenv("PG_USER"), "postgres"),
				PostgresPassword:    os.Getenv("PGPASSWORD"),
				PostgresSSLMode:     cmp.Or(os.Getenv("PG_SSLMODE"), "prefer"),
				PostgresSSLRootCert: os.Getenv("PG_SSLROOTCERT"),
			})

			w.RegisterWorkflow(workflows.Cleanup)
			w.RegisterWorkflow(workflows.RegistryGC)
			w.RegisterWorkflow(workflows.PostgresMaintenance)
			w.RegisterWorkflow(workflows.AptlyCleanup)
			w.RegisterActivity(acts)
			return nil, nil
		},
	})
	if err != nil {
		log.Fatalln(err)
	}
}
