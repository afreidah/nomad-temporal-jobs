// -------------------------------------------------------------------------------
// Cert Acquirer Worker - Temporal Worker for Wildcard Certificate Renewal
//
// Author: Alex Freidah
//
// Starts a Temporal worker on the cert-task-queue that issues the
// *.munchbox.cc wildcard via ACME DNS-01 and publishes it to Vault. The
// worker authenticates to Vault with its Nomad Workload Identity token and
// pulls every other credential (the Cloudflare DNS token) through that client,
// so the only secret the Nomad job carries is its identity. Initializes
// tracing, structured logging, and Prometheus metrics like the other workers.
// -------------------------------------------------------------------------------

package main

import (
	"context"
	"log"
	"os"
	"time"

	"munchbox/temporal-workers/certacquirer/activities"
	"munchbox/temporal-workers/certacquirer/workflows"
	"munchbox/temporal-workers/shared"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
)

const (
	taskQueue            = "cert-task-queue"
	tokenRefreshInterval = time.Minute
)

func main() {
	ctx := context.Background()

	// --- Tracing ---
	shutdownTracer := shared.InitTracer(ctx, "cert-acquirer-worker")
	defer func() { _ = shutdownTracer(ctx) }()

	// --- Structured logging ---
	temporalLogger, slogger := shared.NewLogger("cert-acquirer-worker")

	// --- Metrics ---
	metricsAddr := envOrDefault("METRICS_LISTEN", ":9090")
	metricsHandler := shared.NewMetricsHandler(metricsAddr)

	// --- Vault client (Workload Identity); other creds are pulled through it ---
	vc, err := shared.NewVaultClient()
	if err != nil {
		log.Fatalln("Unable to create Vault client:", err)
	}
	go vc.StartTokenRefresher(ctx, tokenRefreshInterval, slogger)

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
		Vault:    vc,
		CADirURL: os.Getenv("ACME_CA_DIR_URL"),
	})

	// --- Worker registration ---
	w := worker.New(c, taskQueue, worker.Options{})

	w.RegisterWorkflow(workflows.CertAcquirer)
	w.RegisterActivity(acts)

	slogger.Info("Cert acquirer worker starting",
		"temporal", temporalAddr,
		"queue", taskQueue,
		"metrics", metricsAddr)

	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatalln("Worker failed:", err)
	}
}

// envOrDefault reads an environment variable, returning fallback if unset or
// empty.
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
