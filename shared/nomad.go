// -------------------------------------------------------------------------------
// Shared Nomad Client - Instrumented Nomad API Client Factory
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Creates Nomad API clients with OTel-instrumented HTTP transport so that
// Nomad API calls appear as edges in the Tempo service graph. Used by
// trivyscan and nodecleanup workflows.
// -------------------------------------------------------------------------------

package shared

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/hashicorp/nomad/api"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// NewNomadClient creates a configured Nomad API client with OTel-instrumented
// HTTP transport so calls appear as edges in the service graph.
func NewNomadClient() (*api.Client, error) {
	nomadAddr := os.Getenv("NOMAD_ADDR")
	if nomadAddr == "" {
		nomadAddr = "https://nomad.service.consul:4646"
	}

	config := api.DefaultConfig()
	config.Address = nomadAddr

	if token := os.Getenv("NOMAD_TOKEN"); token != "" {
		config.SecretID = token
	}
	if caCert := os.Getenv("NOMAD_CACERT"); caCert != "" {
		config.TLSConfig.CACert = caCert
	}

	config.HttpClient = &http.Client{
		Transport: otelhttp.NewTransport(
			http.DefaultTransport,
			otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
				return fmt.Sprintf("nomad.%s", r.URL.Path)
			}),
		),
	}

	return api.NewClient(config)
}

// ScaleNomadJob scales a job's task group to count. Idempotent -- Nomad
// accepts the call when the job is already at the requested count. The error
// is returned verbatim so callers can classify it (e.g. job-not-found as
// non-retryable).
func ScaleNomadJob(client *api.Client, jobName, groupName string, count int, reason string) error {
	c := count
	if _, _, err := client.Jobs().Scale(jobName, groupName, &c, reason, false, nil, nil); err != nil {
		return fmt.Errorf("scale %s/%s to %d: %w", jobName, groupName, count, err)
	}
	return nil
}

// WaitNomadAllocCount polls until the job's running-allocation count meets
// target -- target 0 succeeds when running drops to 0, target>=1 succeeds when
// running is at least target. onPoll (may be nil) is called each poll with the
// current running count, for heartbeats/logging. Returns ctx.Err() if the
// context ends first. Transient list errors are skipped and retried.
func WaitNomadAllocCount(ctx context.Context, client *api.Client, jobName string, target int, interval time.Duration, onPoll func(running int)) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if allocs, _, err := client.Jobs().Allocations(jobName, false, nil); err == nil {
			running := 0
			for _, al := range allocs {
				if al.ClientStatus == "running" {
					running++
				}
			}
			if onPoll != nil {
				onPoll(running)
			}
			if (target == 0 && running == 0) || (target > 0 && running >= target) {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
