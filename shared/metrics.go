// -------------------------------------------------------------------------------
// Shared Metrics - Prometheus Metrics for Temporal SDK
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Exposes Temporal SDK metrics (workflow/activity latency, task queue depth,
// retry counts, failure rates) via a Prometheus /metrics endpoint. Uses the
// Tally library as the Temporal SDK requires it for metrics emission.
// -------------------------------------------------------------------------------

package shared

import (
	"log"
	"net/http"
	"time"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/uber-go/tally/v4"
	"github.com/uber-go/tally/v4/prometheus"
	sdktally "go.temporal.io/sdk/contrib/tally"
	tclient "go.temporal.io/sdk/client"
)

// NewMetricsHandler creates a Temporal MetricsHandler backed by Prometheus.
// Starts an HTTP server on listenAddr exposing /metrics. Returns nil if
// listenAddr is empty (metrics disabled).
func NewMetricsHandler(listenAddr string) tclient.MetricsHandler {
	if listenAddr == "" {
		return nil
	}

	reporter := prometheus.NewReporter(prometheus.Options{
		Registerer: prom.DefaultRegisterer,
		Gatherer:   prom.DefaultGatherer,
	})

	scope, closer := tally.NewRootScope(tally.ScopeOptions{
		Prefix:         "temporal",
		Tags:           map[string]string{},
		CachedReporter: reporter,
		Separator:      prometheus.DefaultSeparator,
	}, time.Second)

	// Scope closer runs on process exit; no explicit cleanup needed
	_ = closer

	// --- Serve /metrics on a separate port ---
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", reporter.HTTPHandler())
		srv := &http.Server{
			Addr:              listenAddr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		}
		log.Printf("Prometheus metrics serving on %s/metrics", listenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Warning: metrics server failed: %v", err)
		}
	}()

	return sdktally.NewMetricsHandler(scope)
}
