// Package shared is the common runtime for every Temporal worker in this
// repo. RunWorker owns the bootstrap each worker shares -- OTel tracing,
// structured slog logging, Prometheus metrics, and a Temporal client wired
// with the tracing interceptor -- so a worker's main() only declares its
// identity and registers its own workflows and activities.
//
// The native API clients the workers operate through (SSH/SFTP, Docker,
// Nomad, Consul, Vault, Postgres, ...), each instrumented for the Tempo
// service graph, live in per-service subpackages under shared/client. This
// package keeps the cross-cutting helpers, including WithHeartbeat for
// long-running activities and OTelTransport for instrumenting client HTTP.
// Workers shell out to an external CLI only where no Go-native client exists.
package shared
