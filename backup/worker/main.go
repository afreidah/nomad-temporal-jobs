// -------------------------------------------------------------------------------
// Backup Worker - Temporal Worker for Infrastructure Backups
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Starts a Temporal worker listening on the backup-task-queue. Registers the
// backup workflow and activity struct, initializes tracing, structured logging,
// and Prometheus metrics.
// -------------------------------------------------------------------------------

package main

import (
	"context"
	"log"
	"os"

	"munchbox/temporal-workers/backup/activities"
	"munchbox/temporal-workers/backup/workflows"
	"munchbox/temporal-workers/shared"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
)

const taskQueue = "backup-task-queue"

func main() {
	ctx := context.Background()

	// --- Tracing ---
	shutdownTracer := shared.InitTracer(ctx, "backup-worker")
	defer shutdownTracer(ctx)

	// --- Structured logging ---
	temporalLogger, slogger := shared.NewLogger("backup-worker")

	// --- Metrics ---
	metricsAddr := envOrDefault("METRICS_LISTEN", ":9090")
	metricsHandler := shared.NewMetricsHandler(metricsAddr)

	// --- Temporal client ---
	temporalAddr := envOrDefault("TEMPORAL_ADDRESS", "localhost:7233")

	tracingInterceptor, err := opentelemetry.NewTracingInterceptor(opentelemetry.TracerOptions{})
	if err != nil {
		slogger.Warn("Failed to create tracing interceptor", "error", err)
	}

	clientOpts := client.Options{
		HostPort:       temporalAddr,
		Logger:         temporalLogger,
		MetricsHandler: metricsHandler,
	}
	if tracingInterceptor != nil {
		clientOpts.Interceptors = []interceptor.ClientInterceptor{tracingInterceptor}
	}

	c, err := client.Dial(clientOpts)
	if err != nil {
		log.Fatalln("Unable to create Temporal client:", err)
	}
	defer c.Close()

	// --- Activity dependencies ---
	acts, err := activities.New(activities.Config{
		S3Endpoint:  envOrDefault("S3_ENDPOINT", "http://s3-orchestrator.service.consul:9000"),
		S3Bucket:    envOrDefault("S3_BUCKET", "unified"),
		S3AccessKey: os.Getenv("S3_ACCESS_KEY"),
		S3SecretKey: os.Getenv("S3_SECRET_KEY"),
	})
	if err != nil {
		log.Fatalln("Failed to initialize activities:", err)
	}

	// --- Worker registration ---
	w := worker.New(c, taskQueue, worker.Options{})

	w.RegisterWorkflow(workflows.Backup)
	w.RegisterActivity(acts)

	slogger.Info("Backup worker starting",
		"temporal", temporalAddr,
		"queue", taskQueue,
		"metrics", metricsAddr)

	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatalln("Worker failed:", err)
	}
}

// envOrDefault reads an environment variable, returning fallback if unset
// or empty.
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
