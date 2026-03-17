// -------------------------------------------------------------------------------
// Node Cleanup Worker - Temporal Worker for Orphaned Data Removal
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Starts a Temporal worker listening on the cleanup-task-queue. Registers
// the cleanup workflow and activity struct, initializes tracing, structured
// logging, and Prometheus metrics. Requires SSH key access to Nomad client
// nodes for remote cleanup execution.
// -------------------------------------------------------------------------------

package main

import (
	"context"
	"log"
	"os"

	"munchbox/temporal-workers/nodecleanup/activities"
	"munchbox/temporal-workers/nodecleanup/workflows"
	"munchbox/temporal-workers/shared"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
)

const taskQueue = "cleanup-task-queue"

func main() {
	ctx := context.Background()

	// --- Tracing ---
	shutdownTracer := shared.InitTracer(ctx, "cleanup-worker")
	defer func() { _ = shutdownTracer(ctx) }()

	// --- Structured logging ---
	temporalLogger, slogger := shared.NewLogger("cleanup-worker")

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
	acts := activities.New(activities.Config{
		SSHKeyPath:    envOrDefault("SSH_KEY_PATH", "/root/.ssh/id_ed25519"),
		SSHCertPath:   envOrDefault("SSH_CERT_PATH", "/root/.ssh/id_ed25519-cert.pub"),
		SSHHostCAPath: envOrDefault("SSH_HOST_CA_PATH", "/root/.ssh/ssh-host-ca.pub"),
	})

	// --- Worker registration ---
	w := worker.New(c, taskQueue, worker.Options{})

	w.RegisterWorkflow(workflows.Cleanup)
	w.RegisterActivity(acts)

	slogger.Info("Cleanup worker starting",
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
