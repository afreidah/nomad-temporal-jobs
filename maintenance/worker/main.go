// -------------------------------------------------------------------------------
// Maintenance Worker - Temporal Worker for Infrastructure Maintenance
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Starts a Temporal worker on the cleanup-task-queue hosting four independent
// maintenance workflows: orphaned Nomad data-directory removal, registry GC,
// aptly cleanup, and PostgreSQL VACUUM. The shared runtime owns tracing,
// logging, metrics, and the Temporal client; this file builds the shared Nomad,
// SSH, and Postgres clients once and injects them into each workflow's
// activities. Requires SSH key access to Nomad client nodes.
// -------------------------------------------------------------------------------

package main

import (
	"cmp"
	"context"
	"log"
	"log/slog"
	"os"

	"munchbox/temporal-workers/maintenance/aptlycleanup"
	"munchbox/temporal-workers/maintenance/internal/nodes"
	"munchbox/temporal-workers/maintenance/nodecleanup"
	"munchbox/temporal-workers/maintenance/postgresmaint"
	"munchbox/temporal-workers/maintenance/registrygc"
	"munchbox/temporal-workers/shared"

	"munchbox/temporal-workers/shared/client/nomad"
	"munchbox/temporal-workers/shared/client/postgres"
	"munchbox/temporal-workers/shared/client/ssh"

	"go.temporal.io/sdk/worker"
)

func main() {
	err := shared.RunWorker(context.Background(), shared.WorkerSpec{
		Service:   "cleanup-worker",
		TaskQueue: "cleanup-task-queue",
		Register: func(_ context.Context, _ *slog.Logger, w worker.Worker) (func(), error) {
			nomad, err := nomad.NewNomad()
			if err != nil {
				return nil, err
			}
			ssh, err := ssh.NewSSHClient(ssh.SSHConfig{
				KeyPath:    cmp.Or(os.Getenv("SSH_KEY_PATH"), "/root/.ssh/id_ed25519"),
				CertPath:   cmp.Or(os.Getenv("SSH_CERT_PATH"), "/root/.ssh/id_ed25519-cert.pub"),
				HostCAPath: cmp.Or(os.Getenv("SSH_HOST_CA_PATH"), "/root/.ssh/ssh-host-ca.pub"),
			})
			if err != nil {
				return nil, err
			}
			pg := postgres.NewPostgres(postgres.PostgresConfig{
				Host:        cmp.Or(os.Getenv("PG_HOST"), "postgres-primary.service.consul"),
				Port:        cmp.Or(os.Getenv("PG_PORT"), "5432"),
				User:        cmp.Or(os.Getenv("PG_USER"), "postgres"),
				Password:    os.Getenv("PGPASSWORD"),
				SSLMode:     cmp.Or(os.Getenv("PG_SSLMODE"), "prefer"),
				SSLRootCert: os.Getenv("PG_SSLROOTCERT"),
			})

			// One worker, one task queue, four maintenance workflows. The
			// registry-GC and aptly-cleanup sagas share the generic
			// find/scale/wait/measure activities via nodes.SagaActivities.
			w.RegisterWorkflow(nodecleanup.Cleanup)
			w.RegisterWorkflow(registrygc.RegistryGC)
			w.RegisterWorkflow(aptlycleanup.AptlyCleanup)
			w.RegisterWorkflow(postgresmaint.PostgresMaintenance)

			w.RegisterActivity(nodes.NewSagaActivities(nomad, ssh))
			w.RegisterActivity(nodecleanup.New(nomad, ssh))
			w.RegisterActivity(registrygc.New(ssh))
			w.RegisterActivity(aptlycleanup.New(ssh))
			w.RegisterActivity(postgresmaint.New(pg))
			return nil, nil
		},
	})
	if err != nil {
		log.Fatalln(err)
	}
}
