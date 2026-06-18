// Package activities implements the Temporal activities for the trivy-scan
// worker: discovering running container images from the Nomad API, scanning
// each through the Trivy server, and persisting CVE results to PostgreSQL.
// Scans run via the Trivy CLI (no stable Go API exists); transient failures
// are retried and permanent ones (image not found) are recorded and skipped.
package activities
