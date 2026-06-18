// Package workflows holds the trivy-scan orchestration: discover running
// images across the cluster, scan them through the Trivy server under bounded
// concurrency, and persist each result to PostgreSQL. Scan failures are
// recorded with error status and do not block the run. Pure orchestration --
// all I/O happens in activities.
package workflows
