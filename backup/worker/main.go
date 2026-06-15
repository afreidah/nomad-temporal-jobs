// -------------------------------------------------------------------------------
// Backup Worker - Temporal Worker for Infrastructure Backups
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Starts a Temporal worker on the backup-task-queue. The shared runtime owns
// tracing, logging, metrics, and the Temporal client; this file only builds
// the backup activities and registers the workflow.
// -------------------------------------------------------------------------------

package main

import (
	"cmp"
	"context"
	"log"
	"log/slog"
	"os"

	"munchbox/temporal-workers/backup/activities"
	"munchbox/temporal-workers/backup/workflows"
	"munchbox/temporal-workers/shared"

	"go.temporal.io/sdk/worker"
)

func main() {
	err := shared.RunWorker(context.Background(), shared.WorkerSpec{
		Service:   "backup-worker",
		TaskQueue: "backup-task-queue",
		Register: func(_ context.Context, _ *slog.Logger, w worker.Worker) (func(), error) {
			acts, err := activities.New(activities.Config{
				S3Endpoint:   cmp.Or(os.Getenv("S3_ENDPOINT"), "http://s3-orchestrator.service.consul:9000"),
				S3Bucket:     cmp.Or(os.Getenv("S3_BUCKET"), "unified"),
				S3AccessKey:  os.Getenv("S3_ACCESS_KEY"),
				S3SecretKey:  os.Getenv("S3_SECRET_KEY"),
				PostgresHost: cmp.Or(os.Getenv("PG_HOST"), "postgres-primary.service.consul"),
				PostgresUser: cmp.Or(os.Getenv("PG_USER"), "postgres"),
			})
			if err != nil {
				return nil, err
			}
			w.RegisterWorkflow(workflows.Backup)
			w.RegisterActivity(acts)
			return nil, nil
		},
	})
	if err != nil {
		log.Fatalln(err)
	}
}
