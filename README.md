<p align="center">
  <img src="logo.png" alt="nomad-temporal-jobs" width="400">
</p>

# Nomad Temporal Jobs

[![CI](https://github.com/afreidah/nomad-temporal-jobs/actions/workflows/ci.yml/badge.svg)](https://github.com/afreidah/nomad-temporal-jobs/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

Temporal workflow workers for automated infrastructure operations. Each domain (backup, vulnerability scanning, node cleanup) runs as an independent worker with its own container image, task queue, and Nomad service job. A shared trigger binary dispatches workflows on schedule.

```
  Nomad periodic triggers              Temporal Server
  (temporal-*-trigger)                 (temporal-server.service.consul:7233)
        |                                     |
        | ExecuteWorkflow                     | Task Queue dispatch
        v                                     v
  +--------------+     +-----------------------+     +------------------+
  | workflow-    |     | backup-worker         |     | trivy-scan-      |
  | trigger      |---->| backup-task-queue     |     | worker           |
  | (all three) |     +-----------------------+     | trivy-task-queue |
  +--------------+     | cleanup-worker        |     +------------------+
                       | cleanup-task-queue    |
                       +-----------------------+
                              |
                              | SSH
                              v
                       Nomad client nodes
```

Each worker is a long-running Temporal worker process that polls its dedicated task queue. Workflows are pure orchestration; all I/O happens in activities. Activities are registered as struct methods, sharing pooled connections (DB, S3) across invocations.

## Table of Contents

- [Workflow Domains](#workflow-domains)
- [Shared Infrastructure](#shared-infrastructure)
- [Trigger Binary](#trigger-binary)
- [Retry and Error Handling](#retry-and-error-handling)
- [Configuration](#configuration)
- [Observability](#observability)
- [Development](#development)
- [Deployment](#deployment)
- [Project Structure](#project-structure)

## Workflow Domains

### Backup

Snapshots Nomad Raft state, Consul Raft state (includes Vault data), and all PostgreSQL databases (pg_dumpall). Each snapshot is stored locally on an NFS mount and uploaded to S3 for off-site redundancy. Old backups are cleaned up based on configurable retention (default: 7 days local, 30 days S3).

**Task queue:** `backup-task-queue`
**Schedule:** Daily at 2 AM
**Image:** `backup-worker`
**Dependencies:** Nomad CLI, Consul CLI, pg_dumpall, S3-compatible storage

### Trivy Scan

Discovers all running Docker images from the Nomad API, scans each through the Trivy server in parallel batches, and stores CVE results in PostgreSQL. Transient errors (server down) are retried by Temporal; permanent errors (image not found) are recorded and skipped.

**Task queue:** `trivy-task-queue`
**Schedule:** Daily at 3 AM
**Image:** `trivy-scan-worker`
**Dependencies:** Trivy CLI, Nomad API, PostgreSQL

### Node Cleanup

SSHes to each Nomad client node, identifies job data directories that no longer correspond to running allocations, and removes those older than the grace period. Optionally prunes unused Docker images. Supports dry-run mode for safe previewing.

**Task queue:** `cleanup-task-queue`
**Schedule:** Daily at 5 AM
**Image:** `cleanup-worker`
**Dependencies:** SSH access to all Nomad client nodes, Nomad API

## Shared Infrastructure

The `shared/` package provides common functionality used by all workers:

| File | Purpose |
|------|---------|
| `telemetry.go` | OpenTelemetry tracer initialization with OTLP gRPC export to Tempo |
| `logging.go` | JSON slog logger wrapped for Temporal SDK compatibility |
| `metrics.go` | Prometheus metrics handler for Temporal SDK metrics (Tally bridge) |
| `nomad.go` | OTel-instrumented Nomad API client factory |

All workers use `StartClientSpan` with `PeerServiceAttr` to produce service graph edges in Tempo/Grafana for every external call (Nomad, Consul, PostgreSQL, S3, Trivy server).

## Trigger Binary

A single trigger binary (`cmd/trigger/`) handles all three workflows. It connects to Temporal with OTel tracing, starts the requested workflow, waits for completion, and logs the result. The binary is bundled into the `backup-worker` image and selected via the `WORKFLOW_NAME` environment variable.

| WORKFLOW_NAME | Workflow | Task Queue | Additional Env Vars |
|---------------|----------|------------|---------------------|
| `backup` | `Backup` | `backup-task-queue` | `LOCAL_RETENTION_DAYS`, `S3_RETENTION_DAYS` |
| `trivy` | `Scan` | `trivy-task-queue` | -- |
| `cleanup` | `Cleanup` | `cleanup-task-queue` | `DRY_RUN`, `GRACE_DAYS`, `DOCKER_PRUNE`, `CLEANUP_DATA_DIR` |

## Retry and Error Handling

### Activity Timeouts

Each activity has both a `StartToCloseTimeout` (max time for a single attempt) and a `ScheduleToCloseTimeout` (max total time including all retries). Quick operations (Nomad/Consul snapshots, DB saves) use 5/15 minute timeouts. Long operations (PostgreSQL dumps, image scanning) use 30/60 minute timeouts.

### Retry Policy

All activities share a common retry policy with exponential backoff:

| Parameter | Value |
|-----------|-------|
| Initial interval | 1 second |
| Backoff coefficient | 2.0 |
| Maximum interval | 1 minute |
| Maximum attempts | 3 |

### Error Classification (Trivy Scan)

Trivy scan activities distinguish between transient and permanent failures:

| Error Type | Examples | Behavior |
|------------|----------|----------|
| Transient | Connection refused, timeout, connection reset | Returns error; Temporal retries automatically |
| Permanent | Image not found, manifest unknown | Returns `NonRetryableApplicationError`; Temporal stops immediately |
| Parse failure | Invalid trivy JSON output | Returns `NonRetryableApplicationError` |

### Backup Failure Behavior

| Step | On Failure |
|------|-----------|
| Nomad/Consul/PostgreSQL snapshot | Workflow terminates with error |
| S3 upload | Warning logged, workflow continues |
| S3 quota exceeded | Oldest backup evicted, upload retried (up to 3 evictions) |
| Local/S3 cleanup | Warning logged, workflow continues |

### Cleanup Safety Features

- Dry-run mode (default: enabled) reports what would be deleted without removing anything
- Grace period (default: 7 days) prevents deletion of recently-used directories
- System directories (`alloc`, `plugins`, `tmp`, `server`, `client`) are always excluded
- Node failures are tracked; the workflow reports which nodes failed

## Configuration

All configuration is via environment variables, injected by Nomad job templates from Vault.

### Common (all workers)

| Variable | Default | Description |
|----------|---------|-------------|
| `TEMPORAL_ADDRESS` | `localhost:7233` | Temporal server endpoint |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `tempo.service.consul:4317` | OTLP gRPC endpoint |
| `METRICS_LISTEN` | `:9090` | Prometheus metrics listen address |

### Backup Worker

| Variable | Default | Description |
|----------|---------|-------------|
| `S3_ENDPOINT` | -- | S3-compatible endpoint URL |
| `S3_BUCKET` | -- | Target bucket name |
| `S3_ACCESS_KEY` | -- | S3 access key ID |
| `S3_SECRET_KEY` | -- | S3 secret access key |
| `NOMAD_TOKEN` | -- | Nomad API token (snapshot permissions) |
| `CONSUL_HTTP_TOKEN` | -- | Consul API token (snapshot permissions) |
| `PGPASSWORD` | -- | PostgreSQL password for pg_dumpall |

### Trivy Scan Worker

| Variable | Default | Description |
|----------|---------|-------------|
| `TRIVY_SERVER_ADDR` | `http://trivy-server.service.consul:4954` | Trivy server endpoint |
| `TRIVY_DB_HOST` | `postgres-shared.service.consul` | PostgreSQL host for scan results |
| `TRIVY_DB_PORT` | `5432` | PostgreSQL port |
| `TRIVY_DB_USER` | -- | PostgreSQL username |
| `TRIVY_DB_PASSWORD` | -- | PostgreSQL password |
| `TRIVY_DB_NAME` | `trivy` | Database name |
| `DB_SSLMODE` | `verify-ca` | PostgreSQL SSL mode |
| `DB_SSLROOTCERT` | -- | Path to CA certificate |
| `NOMAD_TOKEN` | -- | Nomad API token (read allocations) |

### Cleanup Worker

| Variable | Default | Description |
|----------|---------|-------------|
| `SSH_KEY_PATH` | `/root/.ssh/id_ed25519` | SSH private key path |
| `SSH_CERT_PATH` | `/root/.ssh/id_ed25519-cert.pub` | SSH client certificate path |
| `SSH_HOST_CA_PATH` | `/root/.ssh/ssh-host-ca.pub` | SSH host CA public key path |
| `NOMAD_TOKEN` | -- | Nomad API token (read nodes/allocations) |

## Observability

### Tracing

All workers initialize OpenTelemetry with OTLP gRPC export. The Temporal SDK tracing interceptor automatically creates spans for workflow execution and activity dispatch. Activities create explicit client spans with `peer.service` attributes for service graph edges:

- `backup-worker` -> nomad, consul, postgres-primary, s3-orchestrator
- `trivy-scan-worker` -> nomad, trivy-server, postgres (via otelsql)
- `cleanup-worker` -> nomad

The trigger binary also initializes tracing, producing a root span that connects to the workflow execution trace.

### Metrics

Temporal SDK metrics are exposed via Prometheus on `:9090/metrics`. Key metrics include:

| Metric prefix | Description |
|---------------|-------------|
| `temporal_workflow_*` | Workflow execution counts, latency, failures |
| `temporal_activity_*` | Activity execution counts, latency, retries |
| `temporal_task_queue_*` | Task queue depth and poll latency |

### Logging

JSON structured logs via `log/slog` to stdout. The Temporal SDK logger is wrapped via `log.NewStructuredLogger` so SDK-internal logs (task polling, activity dispatch, retries) share the same JSON format for Alloy/Loki collection.

## Development

```bash
# Build all packages
make build

# Run all tests with race detector
make test

# Static analysis
make vet

# Lint (golangci-lint)
make lint

# Vulnerability scan
make govulncheck

# Build and push all images
make push-all

# Build and push a specific domain
make push-backup
make push-trivy
make push-cleanup
```

Each domain also has its own `Makefile` in its subdirectory for independent builds:

```bash
cd backup && make help
cd trivyscan && make help
cd nodecleanup && make help
```

## Deployment

Each domain is deployed as a separate Nomad service job. Trigger jobs are Nomad periodic batch jobs that invoke the shared trigger binary.

### Nomad Jobs

| Job | Type | Image | Task Queue |
|-----|------|-------|------------|
| `backup-worker` | service | `backup-worker` | `backup-task-queue` |
| `trivy-scan-worker` | service | `trivy-scan-worker` | `trivy-task-queue` |
| `cleanup-worker` | service | `cleanup-worker` | `cleanup-task-queue` |
| `temporal-backup-trigger` | periodic batch | `backup-worker` | -- |
| `temporal-trivy-trigger` | periodic batch | `backup-worker` | -- |
| `temporal-cleanup-trigger` | periodic batch | `backup-worker` | -- |

### Manual Triggering

```bash
# Backup
temporal workflow start --task-queue backup-task-queue --type Backup \
  --address temporal-server.service.consul:7233 \
  --input '{"local_days":7,"s3_days":30}'

# Trivy scan
temporal workflow start --task-queue trivy-task-queue --type Scan \
  --address temporal-server.service.consul:7233

# Node cleanup (dry run)
temporal workflow start --task-queue cleanup-task-queue --type Cleanup \
  --address temporal-server.service.consul:7233 \
  --input '{"data_dir":"/opt/nomad/data","grace_days":7,"dry_run":true,"docker_prune":false}'
```

## Project Structure

```
nomad-temporal-jobs/
  .github/workflows/
    ci.yml                           CI: lint, test, vet, govulncheck, version check
  .gitignore                         Ignores coverage output and dist artifacts
  .golangci.yml                      Linter configuration (gocritic, misspell)
  .version                           Root version tag
  LICENSE                            MIT
  Makefile                           Root: build, test, lint, push-all targets
  README.md                          This file
  go.mod                             Module definition
  go.sum                             Dependency lock file
  shared/
    telemetry.go                     OTel tracer init, span helpers, peer.service attributes
    logging.go                       JSON slog logger with Temporal SDK adapter
    metrics.go                       Prometheus metrics handler via Tally bridge
    nomad.go                         OTel-instrumented Nomad API client factory
  cmd/
    trigger/
      main.go                       Workflow dispatcher (backup, trivy, cleanup)
  backup/
    .version                         Image version tag
    Dockerfile                       Multi-stage build (Debian, Nomad/Consul/PG18 CLI)
    Makefile                         Build and push targets
    activities/
      activities.go                  Activity struct: snapshots, S3 upload, retention cleanup
    workflows/
      backup.go                      Sequential snapshot orchestration with S3 upload
    worker/
      main.go                       Worker entry point (tracing, slog, metrics, Temporal client)
  trivyscan/
    .version                         Image version tag
    Dockerfile                       Multi-stage build (Alpine, Trivy CLI)
    Makefile                         Build and push targets
    activities/
      activities.go                  Activity struct: Nomad image discovery, Trivy scan, DB save
    workflows/
      scan.go                        Parallel batch scanning with per-image error handling
    worker/
      main.go                       Worker entry point (tracing, slog, metrics, Temporal client)
  nodecleanup/
    .version                         Image version tag
    Dockerfile                       Multi-stage build (Alpine, SSH only)
    Makefile                         Build and push targets
    activities/
      activities.go                  Activity struct: node discovery, SSH cleanup, script gen
    workflows/
      cleanup.go                     Sequential per-node cleanup with failure tracking
    worker/
      main.go                       Worker entry point (tracing, slog, metrics, Temporal client)
```

## License

MIT
