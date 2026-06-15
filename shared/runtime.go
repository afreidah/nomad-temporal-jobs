// -------------------------------------------------------------------------------
// Shared Worker Runtime - Common Temporal Worker Bootstrap
//
// Author: Alex Freidah
//
// Every worker domain starts the same way: initialize tracing, structured
// logging, and Prometheus metrics, dial Temporal with the OTel tracing
// interceptor, create a worker on a task queue, register its workflows and
// activities, and run until interrupted. RunWorker owns all of that so a
// domain's main() only has to declare its identity and wire up its own
// activities/workflows in the Register hook.
// -------------------------------------------------------------------------------

package shared

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"os"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
)

// WorkerSpec describes one worker domain. Register is where the domain builds
// its dependencies and registers its workflows and activities; everything else
// (tracing, logging, metrics, the Temporal client, the worker lifecycle) is
// handled by RunWorker.
type WorkerSpec struct {
	// Service is the OTel service name and log identity (e.g. "backup-worker").
	Service string
	// TaskQueue is the queue this worker polls (e.g. "backup-task-queue").
	TaskQueue string
	// Register builds the domain's dependencies and registers its workflows
	// and activities on w. It runs after the Temporal client connects, with a
	// ctx scoped to the worker's lifetime (use it for background helpers like a
	// Vault token refresher) and the worker's slog logger. Return a cleanup
	// func (may be nil) for resources that must be closed on shutdown, such as
	// a database pool. An error aborts startup.
	Register func(ctx context.Context, log *slog.Logger, w worker.Worker) (cleanup func(), err error)
}

// RunWorker boots a Temporal worker from spec and blocks until it is
// interrupted (SIGINT/SIGTERM) or fails. It is the single entry point every
// domain's main() delegates to.
func RunWorker(ctx context.Context, spec WorkerSpec) error {
	// --- Tracing ---
	shutdownTracer := InitTracer(ctx, spec.Service)
	defer func() { _ = shutdownTracer(ctx) }()

	// --- Structured logging ---
	temporalLogger, slogger := NewLogger(spec.Service)

	// --- Metrics ---
	metricsAddr := cmp.Or(os.Getenv("METRICS_LISTEN"), ":9090")
	metricsHandler := NewMetricsHandler(metricsAddr)

	// --- Temporal client ---
	temporalAddr := cmp.Or(os.Getenv("TEMPORAL_ADDRESS"), "localhost:7233")

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
		return fmt.Errorf("dial temporal at %s: %w", temporalAddr, err)
	}
	defer c.Close()

	// --- Worker + domain registration ---
	// worker.Options is intentionally left at defaults; this is the central
	// place to tune concurrency or graceful-stop behavior for every worker.
	w := worker.New(c, spec.TaskQueue, worker.Options{})

	cleanup, err := spec.Register(ctx, slogger, w)
	if err != nil {
		return fmt.Errorf("register %s: %w", spec.Service, err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	slogger.Info("Worker starting",
		"service", spec.Service,
		"temporal", temporalAddr,
		"queue", spec.TaskQueue,
		"metrics", metricsAddr)

	if err := w.Run(worker.InterruptCh()); err != nil {
		return fmt.Errorf("worker %s failed: %w", spec.Service, err)
	}
	return nil
}
