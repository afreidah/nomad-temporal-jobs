// -------------------------------------------------------------------------------
// Trivy Scan Worker - Temporal Worker for Vulnerability Scanning
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Starts a Temporal worker listening on the trivy-task-queue. Registers the
// scan workflow and activity struct, initializes tracing, structured logging,
// and Prometheus metrics. Runs until interrupted.
// -------------------------------------------------------------------------------

package main

import (
	"context"
	"log"
	"os"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"

	"munchbox/temporal-workers/shared"
	"munchbox/temporal-workers/trivyscan/activities"
	"munchbox/temporal-workers/trivyscan/workflows"
)

const taskQueue = "trivy-task-queue"

func main() {
	ctx := context.Background()

	// --- Tracing ---
	shutdownTracer := shared.InitTracer(ctx, "trivy-scan-worker")
	defer func() { _ = shutdownTracer(ctx) }()

	// --- Structured logging ---
	temporalLogger, slogger := shared.NewLogger("trivy-scan-worker")

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
		log.Fatalln("Unable to create Temporal client", err)
	}
	defer c.Close()

	// --- Activity dependencies ---
	acts, err := activities.New(activities.Config{
		TrivyServerAddr: envOrDefault("TRIVY_SERVER_ADDR", "http://trivy-server.service.consul:4954"),
		DBHost:          envOrDefault("TRIVY_DB_HOST", "postgres-shared.service.consul"),
		DBPort:          envOrDefault("TRIVY_DB_PORT", "5432"),
		DBUser:          os.Getenv("TRIVY_DB_USER"),
		DBPassword:      os.Getenv("TRIVY_DB_PASSWORD"),
		DBName:          envOrDefault("TRIVY_DB_NAME", "trivy"),
		DBSSLMode:       envOrDefault("DB_SSLMODE", "verify-ca"),
		DBSSLRootCert:   os.Getenv("DB_SSLROOTCERT"),
	})
	if err != nil {
		log.Fatalln("Failed to initialize activities:", err)
	}
	defer func() { _ = acts.Close() }()

	// --- Worker registration ---
	w := worker.New(c, taskQueue, worker.Options{})

	w.RegisterWorkflow(workflows.Scan)
	w.RegisterActivity(acts)

	slogger.Info("Trivy scan worker starting",
		"temporal", temporalAddr,
		"queue", taskQueue,
		"metrics", metricsAddr)

	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatalln("Worker failed:", err)
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
