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
	"fmt"
	"net/http"
	"os"

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
