# Temporal Workers

Temporal workflow workers for automated infrastructure operations. Each domain (backup, vulnerability scanning, node cleanup) runs as an independent worker with its own container image, task queue, and Nomad service job. A shared trigger binary dispatches workflows on schedule.

## Table of Contents

- [Architecture](#architecture)
- [Workflow Domains](#workflow-domains)
- [Shared Infrastructure](#shared-infrastructure)
- [Trigger Binary](#trigger-binary)
- [Configuration](#configuration)
- [Observability](#observability)
- [Development](#development)
- [Deployment](#deployment)
- [Project Structure](#project-structure)

## Architecture

```
  Nomad periodic triggers           Temporal Server
  (temporal-*-trigger)              (temporal-server.service.consul:7233)
        |                                  |
        | ExecuteWorkflow                  | Task Queue dispatch
        v                                  v
  +-------------+    +------------------+    +----------------+
  | workflow-    |    | backup-worker    |    | trivy-scan-    |
  | trigger      |--->| backup-task-queue|    | worker          |
  | (all 3)     |    +------------------+    | trivy-task-queue|
  +-------------+    | cleanup-worker   |    +----------------+
                     | cleanup-task-queue|
                     +------------------+
                            |
                            | SSH
                            v
                     Nomad client nodes
```

Each worker is a long-running Temporal worker process that polls its dedicated task queue. Workflows are pure orchestration; all I/O happens in activities. Activities are registered as struct methods, sharing pooled connections (DB, S3) across invocations.

## Workflow Domains

### Backup

Snapshots Nomad Raft state, Consul Raft state (includes Vault data), all PostgreSQL databases (pg_dumpall), and the container registry. Each snapshot is stored locally on an NFS mount and uploaded to S3 for off-site redundancy. Old backups are cleaned up based on configurable retention (default: 7 days local, 30 days S3).

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

| Package | Purpose |
|---------|---------|
| `shared/telemetry.go` | OpenTelemetry tracer initialization with OTLP gRPC export to Tempo |
| `shared/logging.go` | JSON slog logger wrapped for Temporal SDK compatibility |
| `shared/metrics.go` | Prometheus metrics handler for Temporal SDK metrics (Tally bridge) |
| `shared/nomad.go` | OTel-instrumented Nomad API client factory |

All workers use `StartClientSpan` with `PeerServiceAttr` to produce service graph edges in Tempo/Grafana for every external call (Nomad, Consul, PostgreSQL, S3, Trivy server).

## Trigger Binary

A single trigger binary (`cmd/trigger/`) handles all three workflows. It connects to Temporal with OTel tracing, starts the requested workflow, waits for completion, and logs the result. The binary is bundled into the `backup-worker` image and selected via the `WORKFLOW_NAME` environment variable.

| WORKFLOW_NAME | Workflow | Task Queue | Additional Env Vars |
|---------------|----------|------------|---------------------|
| `backup` | `Backup` | `backup-task-queue` | `LOCAL_RETENTION_DAYS`, `S3_RETENTION_DAYS` |
| `trivy` | `Scan` | `trivy-task-queue` | -- |
| `cleanup` | `Cleanup` | `cleanup-task-queue` | `DRY_RUN`, `GRACE_DAYS`, `DOCKER_PRUNE`, `CLEANUP_DATA_DIR` |

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

### Metrics

Temporal SDK metrics are exposed via Prometheus on `:9090/metrics`. Includes workflow/activity latency histograms, retry counts, task queue depth, and failure rates.

### Logging

JSON structured logs via `log/slog` to stdout. The Temporal SDK logger is wrapped via `log.NewStructuredLogger` so SDK-internal logs (task polling, activity dispatch, retries) share the same JSON format for Alloy/Loki collection.

## Development

```bash
# Build all packages
go build ./...

# Run tests for a specific domain
go test ./trivyscan/... ./shared/...
go test ./backup/... ./shared/...
go test ./nodecleanup/... ./shared/...

# Lint
go vet ./...
```

Each domain has its own `Makefile` for container builds:

```bash
cd trivyscan && make push      # build and push trivy-scan-worker image
cd backup && make push         # build and push backup-worker image
cd nodecleanup && make push    # build and push cleanup-worker image
```

## Deployment

Each domain is deployed as a separate Nomad service job. Trigger jobs are Nomad periodic batch jobs that invoke the shared trigger binary.

Nomad job files are in `nomad/jobs/temporal-workflows/`:

| Job File | Type | Description |
|----------|------|-------------|
| `backup-worker/backup-worker.nomad.hcl` | service | Backup workflow worker |
| `trivy-scan-worker/trivy-scan-worker.nomad.hcl` | service | Trivy scan workflow worker |
| `cleanup-worker/cleanup-worker.nomad.hcl` | service | Node cleanup workflow worker |
| `temporal-backup-trigger.nomad.hcl` | periodic batch | Triggers backup at 2 AM |
| `temporal-trivy-trigger.nomad.hcl` | periodic batch | Triggers trivy scan at 3 AM |
| `temporal-cleanup-trigger.nomad.hcl` | periodic batch | Triggers cleanup at 5 AM |

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
temporal-workers/
  go.mod                           Module definition (munchbox/temporal-workers)
  go.sum                           Dependency lock file
  README.md                        This file
  shared/
    telemetry.go                   OTel tracer init, span helpers, peer.service attributes
    logging.go                     JSON slog logger with Temporal SDK adapter
    metrics.go                     Prometheus metrics handler via Tally bridge
    nomad.go                       OTel-instrumented Nomad API client factory
  cmd/
    trigger/
      main.go                     Workflow dispatcher (backup, trivy, cleanup)
  backup/
    .version                       Image version tag
    Dockerfile                     Multi-stage build (Debian, Nomad/Consul/PG18 CLI)
    Makefile                       Build and push targets
    activities/
      activities.go                Activity struct: snapshots, S3 upload, retention cleanup
    workflows/
      backup.go                    Sequential snapshot orchestration with S3 upload
    worker/
      main.go                     Worker entry point (tracing, slog, metrics, Temporal client)
  trivyscan/
    .version                       Image version tag
    Dockerfile                     Multi-stage build (Alpine, Trivy CLI)
    Makefile                       Build and push targets
    activities/
      activities.go                Activity struct: Nomad image discovery, Trivy scan, DB save
    workflows/
      scan.go                      Parallel batch scanning with per-image error handling
    worker/
      main.go                     Worker entry point (tracing, slog, metrics, Temporal client)
  nodecleanup/
    .version                       Image version tag
    Dockerfile                     Multi-stage build (Alpine, SSH only)
    Makefile                       Build and push targets
    activities/
      activities.go                Activity struct: node discovery, SSH cleanup, script generation
    workflows/
      cleanup.go                   Sequential per-node cleanup with failure tracking
    worker/
      main.go                     Worker entry point (tracing, slog, metrics, Temporal client)
```
