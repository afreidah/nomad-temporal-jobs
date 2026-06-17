// -------------------------------------------------------------------------------
// Trivy Scan Worker - Temporal Worker for Vulnerability Scanning
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Starts a Temporal worker on the trivy-task-queue. The shared runtime owns
// tracing, logging, metrics, and the Temporal client; this file only builds
// the scan activities (and closes their DB pool on shutdown) and registers
// the workflow.
// -------------------------------------------------------------------------------

package main

import (
	"cmp"
	"context"
	"log"
	"log/slog"
	"os"

	"munchbox/temporal-workers/shared"
	"munchbox/temporal-workers/trivyscan/activities"
	"munchbox/temporal-workers/trivyscan/workflows"

	"go.temporal.io/sdk/worker"
)

func main() {
	err := shared.RunWorker(context.Background(), shared.WorkerSpec{
		Service:   "trivy-scan-worker",
		TaskQueue: "trivy-task-queue",
		Register: func(_ context.Context, _ *slog.Logger, w worker.Worker) (func(), error) {
			acts, err := activities.New(activities.Config{
				TrivyServerAddr: cmp.Or(os.Getenv("TRIVY_SERVER_ADDR"), "http://trivy-server.service.consul:4954"),
				DBHost:          cmp.Or(os.Getenv("TRIVY_DB_HOST"), "postgres-shared.service.consul"),
				DBPort:          cmp.Or(os.Getenv("TRIVY_DB_PORT"), "5432"),
				DBUser:          os.Getenv("TRIVY_DB_USER"),
				DBPassword:      os.Getenv("TRIVY_DB_PASSWORD"),
				DBName:          cmp.Or(os.Getenv("TRIVY_DB_NAME"), "trivy"),
				DBSSLMode:       cmp.Or(os.Getenv("DB_SSLMODE"), "verify-ca"),
				DBSSLRootCert:   os.Getenv("DB_SSLROOTCERT"),
			})
			if err != nil {
				return nil, err
			}
			w.RegisterWorkflow(workflows.Scan)
			w.RegisterActivity(acts)
			return func() { _ = acts.Close() }, nil
		},
	})
	if err != nil {
		log.Fatalln(err)
	}
}
