<p align="center">
  <img src="logo.png" alt="nomad-temporal-jobs" width="400">
</p>

# Nomad Temporal Jobs

[![CI](https://github.com/afreidah/nomad-temporal-jobs/actions/workflows/ci.yml/badge.svg)](https://github.com/afreidah/nomad-temporal-jobs/actions/workflows/ci.yml)
[![Coverage](https://sonarcloud.io/api/project_badges/measure?project=afreidah_nomad-temporal-jobs&metric=coverage)](https://sonarcloud.io/summary/new_code?id=afreidah_nomad-temporal-jobs)
[![Quality Gate](https://sonarcloud.io/api/project_badges/measure?project=afreidah_nomad-temporal-jobs&metric=alert_status)](https://sonarcloud.io/summary/new_code?id=afreidah_nomad-temporal-jobs)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

<p align="center">
  <strong><a href="https://nomad-temporal-jobs.munchbox.cc">Project Website</a></strong>
</p>

Temporal workflow workers for automated infrastructure operations. **Four worker binaries host seven scheduled jobs.** Three workers map to one job each — backup, vulnerability scanning, and certificate acquisition — while the **cleanup worker** hosts four maintenance workflows on a shared task queue: orphaned-data node cleanup, registry GC, aptly cleanup, and PostgreSQL `VACUUM` maintenance. Each worker is an independent container image with its own Nomad service job; the cert-acquirer worker issues the `*.munchbox.cc` wildcard via ACME. Newer workers authenticate to Vault with their Nomad Workload Identity and pull every other credential through a shared client, so no static service tokens are templated into the job. Workflows fire on cron from Temporal Schedules, managed as code in the `infrastructure/terragrunt` repo.

```
  Temporal Schedules            Temporal Server
  (Terraform-managed)           (temporal-server.service.consul:7233)
         |                               |
         | start workflow on cron        | Task Queue dispatch
         v                               v
                            +------------------------+
                            | backup-worker          |
                            | backup-task-queue      |
                            +------------------------+
                            | trivy-scan-worker      |
                            | trivy-task-queue       |
                            +------------------------+
                            | cleanup-worker         |
                            | cleanup-task-queue     |
                            | (cleanup, registry-gc, |
                            |  aptly, postgres-maint)|
                            +------------------------+
                            | cert-acquirer-worker   |
                            | cert-task-queue        |
                            +------------------------+
                                        |
                                        v
                              Nomad, Consul, S3,
                              PostgreSQL, Trivy,
                              SSH (client nodes),
                              Vault, ACME, Cloudflare
```

Each worker is a long-running Temporal worker process that polls its dedicated task queue. Workflows are pure orchestration; all I/O happens in activities. Activities are registered as struct methods, sharing pooled connections (DB, S3) across invocations.

## Table of Contents

- [Workflow Domains](#workflow-domains)
- [Maintenance Sagas](#maintenance-sagas)
- [Shared Infrastructure](#shared-infrastructure)
- [Scheduling](#scheduling)
- [Retry and Error Handling](#retry-and-error-handling)
- [Configuration](#configuration)
- [Observability](#observability)
- [Development](#development)
- [Deployment](#deployment)
- [Project Structure](#project-structure)

## Workflow Domains

### Backup

Snapshots Nomad Raft state, Consul Raft state (includes Vault data), and PostgreSQL. The three legs run concurrently. The PostgreSQL leg dumps cluster-wide globals (roles, tablespaces, grants) once, enumerates the databases, then dumps each one to its own file with bounded concurrency (`PG_DUMP_CONCURRENCY`, default 4). Each artifact is stored locally on an NFS mount and uploaded to S3 for off-site redundancy. Old backups are cleaned up based on configurable retention (default: 7 days local, 30 days S3).

**Task queue:** `backup-task-queue`
**Schedule:** Nomad periodic job
**Image:** `backup-worker`
**Dependencies:** pg_dump/pg_dumpall (PostgreSQL 18 client), S3-compatible storage. Nomad and Consul snapshots are taken through their native Go APIs, so no Nomad/Consul CLI is bundled.

### Trivy Scan

Discovers all running Docker images from the Nomad API, scans them through the Trivy server with bounded concurrency, and stores CVE results in PostgreSQL. Transient errors (server down) are retried by Temporal; permanent errors (image not found) are recorded and skipped.

**Task queue:** `trivy-task-queue`
**Schedule:** Nomad periodic job
**Image:** `trivy-scan-worker`
**Dependencies:** Trivy CLI, Nomad API, PostgreSQL

### Node Cleanup

Removes Nomad job data directories that no longer correspond to a running allocation. The set of running jobs comes from the central Nomad API; the per-node directory work is done over SSH purely as native operations — directories are enumerated and deleted over **SFTP**, never a remote shell script. Removes only directories older than the grace period; optionally prunes unused Docker images through the Docker API (tunneled over SSH). Supports dry-run mode for safe previewing.

**Task queue:** `cleanup-task-queue`
**Schedule:** Nomad periodic job
**Image:** `cleanup-worker`
**Dependencies:** SSH access to all Nomad client nodes, Nomad API

### Registry Garbage Collection

Reclaims disk space from the Docker registry by running the registry's `garbage-collect` against its bind-mounted storage. Because the registry tool requires the registry to be offline, the workflow scales the registry Nomad job to 0, waits for its allocations to drain, runs the `garbage-collect` as a one-shot container through the Docker API (tunneled over SSH), then scales it back to 1. It reports blobs deleted and bytes reclaimed (before/after sizes). Runs on the cleanup worker, sharing its SSH and Nomad-client infrastructure. See [Maintenance Sagas](#maintenance-sagas) for the compensation guarantees.

**Task queue:** `cleanup-task-queue` (shared with node cleanup)
**Workflow:** `RegistryGC`
**Image:** `cleanup-worker`
**Dependencies:** SSH access to the registry host, Nomad API (job scaling), Docker on the registry host

### Aptly Cleanup

Reclaims storage from the aptly Debian repository pool by running `aptly db cleanup` against the pool volume, dropping packages no longer referenced by any snapshot or repo. Because aptly holds a single-writer leveldb lock while running, the workflow scales the aptly Nomad job to 0, waits for its allocations to drain, runs the cleanup as a one-shot container through the Docker API (tunneled over SSH), then scales it back to 1. It reuses the same find / scale / wait / measure saga activities as registry GC and reports bytes reclaimed (before/after sizes). See [Maintenance Sagas](#maintenance-sagas) for the compensation guarantees.

**Task queue:** `cleanup-task-queue` (shared with node cleanup)
**Workflow:** `AptlyCleanup`
**Image:** `cleanup-worker`
**Dependencies:** SSH access to the aptly host, Nomad API (job scaling), Docker on the aptly host

### Postgres Maintenance

Runs online `VACUUM (ANALYZE)` across every database in the cluster to reclaim bloat and refresh planner statistics. Lists the non-template databases from the primary, then vacuums each with a bounded-concurrency fan-out (`Concurrency`, default 2) so the maintenance burst doesn't overwhelm the primary. Online and lock-light — no `FULL`. A per-database failure is recorded and the run continues; the workflow returns an error only after every database has been attempted.

**Task queue:** `cleanup-task-queue` (shared with node cleanup)
**Workflow:** `PostgresMaintenance`
**Image:** `cleanup-worker`
**Dependencies:** PostgreSQL primary (via the shared instrumented client)

### Cert Acquirer

Issues the `*.munchbox.cc` wildcard certificate via ACME DNS-01 (Cloudflare) using the `go-acme/lego` library, and publishes the result to Vault for Traefik to read. The ACME account is persisted to Vault so registration happens once rather than on every run. Issuance and publish are separate activities: the issued cert+key are written to a staging Vault path so a publish failure never re-runs ACME issuance (Let's Encrypt rate-limits duplicate issuance), and the private key never transits Temporal workflow history. The worker authenticates to Vault with its Nomad Workload Identity and pulls the Cloudflare token through that client; no static secrets are templated into the job.

**Task queue:** `cert-task-queue`
**Workflow:** `CertAcquirer`
**Image:** `cert-acquirer-worker`
**Dependencies:** Vault (Workload Identity), Cloudflare DNS API, Let's Encrypt

## Maintenance Sagas

The registry GC and aptly cleanup workflows share one saga skeleton so the job being maintained is never left stranded offline: scale it to 0, do the work while it's down, then always scale it back. The generic steps — locate the job's node, scale, wait for drain/running, measure the data dir — are the **shared saga activities** (`maintenance/internal/nodes`); only the middle "do the work" step is job-specific. Each step has its own retry policy:

| Step | Activity | Retry |
|------|----------|-------|
| Locate the job's host | `FindJobNode` | 3 attempts, exponential backoff |
| Measure storage (before) | `MeasureDataDir` | 3 attempts |
| Scale job to 0 | `ScaleJob` | 3 attempts (idempotent) |
| Wait for allocs to drain | `WaitJobDrained` | bounded by timeout, heartbeats each poll |
| Do the work | `RunRegistryGarbageCollect` / `RunAptlyDBCleanup` | **1 attempt** (no retry on partial work) |
| Measure storage (after) | `MeasureDataDir` | 3 attempts |

Once the scale-down to 0 succeeds, a **compensation** is registered with `defer` and `workflow.NewDisconnectedContext`. It scales the job back to 1 (`ScaleJob`) and waits for a running allocation (`WaitJobRunning`) — and it always fires, even if the work step fails, an activity times out, or the workflow is cancelled mid-flight. Scaling is idempotent, so re-issuing `count=1` is a safe no-op on the happy path. If the scale-back itself fails, the workflow logs a `CRITICAL` recovery message and joins the error so it surfaces to the operator.

| Config | Default | Description |
|--------|---------|-------------|
| `JobName` | `registry` | Nomad job for the registry |
| `GroupName` | (= `JobName`) | Task group to scale |
| `RegistryDataDir` | `/mnt/gdrive/munchbox-data/registry` | Host path bind-mounted as `/var/lib/registry` |
| `RegistryImage` | `registry:3` | Image used for the one-shot GC run |
| `DryRun` | `true` (overridden by schedule input) | Report blobs that would be deleted without freeing space |
| `DeleteUntagged` | `true` | Also remove manifests not referenced by any tag |

## Shared Infrastructure

The `shared/` package provides common functionality used by all workers:

| File | Purpose |
|------|---------|
| `runtime.go` | `RunWorker` — the common worker bootstrap (tracing, logging, metrics, Temporal client + interceptor, workflow/activity registration, run-until-interrupt). Each worker `main.go` is ~15 lines. |
| `telemetry.go` | OpenTelemetry tracer init, span helpers, `peer.service` attributes |
| `logging.go` | JSON slog logger wrapped for Temporal SDK compatibility |
| `metrics.go` | Prometheus metrics handler for Temporal SDK metrics (Tally bridge) |
| `temporal.go` | Shared retry-policy and activity-option presets (`StandardRetry`, `NoRetry`, `QuickActivityOptions`, `LongActivityOptions`) |
| `heartbeat.go` | `WithHeartbeat` — run a function while emitting activity heartbeats |
| `nomad.go` | OTel-instrumented Nomad client + `Nomad` service (running images, client nodes, running job IDs, find-job-node, idempotent scaling, alloc-count waits) |
| `postgres.go` | OTel-instrumented PostgreSQL connection factory + `Postgres` maintenance service (list databases, VACUUM ANALYZE) |
| `s3store.go` | `S3Store` — multipart upload, listing, and retention / quota-eviction over an S3-compatible API |
| `vault.go` | Self-authenticating Vault client (Workload Identity + KV) |
| `consul.go` | OTel-instrumented Consul client + `Consul` service (Raft snapshots), token sourced from Vault |
| `ssh.go` | Certificate-authenticated SSH client with SFTP file operations (`ReadDir`/`RemoveAll`/`DirSize`) — no remote shell |
| `docker.go` | Remote Docker daemon driven through the Docker API, tunneled over the SSH connection to `/var/run/docker.sock` (one-shot containers + prune) |

Reusable clients (`Nomad`, `Postgres`, `Consul`, `S3Store`, `VaultClient`) are concrete `shared` services; each worker declares its own **narrow consumer interface** over the subset it calls (e.g. `nomadImages`, `pgMaintainer`, `s3Store`), satisfied structurally. Workers depend on small, testable surfaces and the shared client can grow without bloating existing consumers.

All workers use `StartPeerSpan` to produce service graph edges in Tempo/Grafana for every external call (Nomad, Consul, PostgreSQL, S3, Trivy server, Vault, ACME, Cloudflare).

**No remote shell.** Workers operate remote infrastructure through native Go APIs end-to-end — Nomad/Consul snapshots and job control via their APIs, the remote Docker daemon over an SSH socket tunnel, and remote files over SFTP. The cleanup worker connects as `root` everywhere (the Vault SSH CA issues a root principal the Oracle hosts accept), so no per-node user/sudo handling is needed. The only remaining subprocesses are `pg_dump`/`pg_dumpall`, which have no Go-native equivalent and whose output is gzipped in-process.

## Scheduling

Workflows fire on cron from Temporal Schedules, defined as code in `infrastructure/terragrunt` (the `temporal-config` module, applied via the `global/temporal-config` leaf). Each schedule starts one workflow on its task queue with a JSON `input` that deserializes into the workflow's config struct. The workers themselves just poll their queues — nothing in this repo triggers them.

| Schedule | Workflow | Task Queue | Cron | Input |
|----------|----------|------------|------|-------|
| `backup-daily` | `Backup` | `backup-task-queue` | `0 1 * * *` | `BackupConfig` (local/S3 days, dump concurrency) |
| `trivy-daily` | `Scan` | `trivy-task-queue` | `0 3 * * *` | `ScanConfig` (scan concurrency) |
| `cleanup-daily` | `Cleanup` | `cleanup-task-queue` | `0 5 * * *` | `CleanupConfig` (data dir, grace days, dry-run, docker prune) |
| `registry-gc-weekly` | `RegistryGC` | `cleanup-task-queue` | `0 2 * * 0` | `RegistryGCConfig` (job/dir/image, dry-run, delete-untagged) |
| `aptly-cleanup-weekly` | `AptlyCleanup` | `cleanup-task-queue` | `0 4 * * 0` | `AptlyCleanupConfig` (job/group/image, data dir) |
| `postgres-maintenance-weekly` | `PostgresMaintenance` | `cleanup-task-queue` | `0 6 * * 0` | `PostgresMaintenanceConfig` (concurrency) |
| `cert-acquirer-weekly` | `CertAcquirer` | `cert-task-queue` | `0 4 * * 1` | `IssueRequest` (domains, email) |

## Retry and Error Handling

### Activity Timeouts

Each activity has both a `StartToCloseTimeout` (max time for a single attempt) and a `ScheduleToCloseTimeout` (max total time including all retries). Quick operations (Nomad/Consul snapshots, globals dump, database listing, S3 uploads, cleanup) use 5/15 minute timeouts. Long operations (per-database `pg_dump`, image scanning) use 30/60 minute timeouts; per-database dumps additionally heartbeat with a 2 minute timeout.

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
| Nomad/Consul snapshot | Leg fails; workflow terminates with error |
| PostgreSQL globals dump or database listing | Leg fails; workflow terminates with error |
| Per-database dump | Leg fails after all databases are attempted; workflow terminates with error |
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
| `PG_HOST` | `postgres-primary.service.consul` | PostgreSQL host for dumps |
| `PG_USER` | `postgres` | PostgreSQL user for dumps |
| `PGPASSWORD` | -- | PostgreSQL password for pg_dump/pg_dumpall |

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

The cleanup worker hosts all four maintenance workflows, so its environment covers SSH (node cleanup + the registry/aptly sagas), the Nomad token, and Postgres (the maintenance workflow).

| Variable | Default | Description |
|----------|---------|-------------|
| `SSH_KEY_PATH` | `/root/.ssh/id_ed25519` | SSH private key path |
| `SSH_CERT_PATH` | `/root/.ssh/id_ed25519-cert.pub` | SSH client certificate path |
| `SSH_HOST_CA_PATH` | `/root/.ssh/ssh-host-ca.pub` | SSH host CA public key path |
| `NOMAD_TOKEN` | -- | Nomad API token (read nodes/allocations; job-scale for the sagas) |
| `PG_HOST` | `postgres-primary.service.consul` | PostgreSQL host for VACUUM maintenance |
| `PG_PORT` | `5432` | PostgreSQL port |
| `PG_USER` | `postgres` | PostgreSQL user |
| `PGPASSWORD` | -- | PostgreSQL password |
| `PG_SSLMODE` | `prefer` | PostgreSQL SSL mode |
| `PG_SSLROOTCERT` | -- | Optional CA path for `verify-ca`/`verify-full` |

### Maintenance Workflow Config

The registry GC, aptly cleanup, and postgres maintenance workflows all run on the cleanup worker, inheriting the SSH/Nomad/Postgres settings above. Their per-run config comes from the schedule input, not the environment: `RegistryGCConfig` (job/dir/image, dry-run, delete-untagged — see [Maintenance Sagas](#maintenance-sagas)), `AptlyCleanupConfig` (job/group/image, data dir), and `PostgresMaintenanceConfig` (concurrency).

### Cert Acquirer Worker

The cert worker carries no static service tokens: it reads its Vault token from the Workload Identity file Nomad provides and pulls the Cloudflare token through it.

| Variable | Default | Description |
|----------|---------|-------------|
| `VAULT_ADDR` | `https://vault.service.consul:8200` | Vault endpoint |
| `VAULT_TOKEN_FILE` | -- | Path to the WI Vault token (`${NOMAD_SECRETS_DIR}/vault_token`) |
| `VAULT_CACERT` | -- | CA cert path so the client trusts Vault's TLS |
| `VAULT_KV_MOUNT` | `secret` | KV v2 mount holding the Cloudflare token, ACME account, and wildcard paths |
| `ACME_CA_DIR_URL` | Let's Encrypt production | ACME directory endpoint (point at staging for testing) |

## Observability

### Tracing

All workers initialize OpenTelemetry with OTLP gRPC export. The Temporal SDK tracing interceptor automatically creates spans for workflow execution and activity dispatch. Activities create explicit client spans with `peer.service` attributes for service graph edges:

- `backup-worker` -> nomad, consul, postgres-primary, s3-orchestrator
- `trivy-scan-worker` -> nomad, trivy-server, postgres (via otelsql)
- `cleanup-worker` -> nomad, postgres-primary
- `cert-acquirer-worker` -> vault, acme, cloudflare

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
make push-cert
```

Each domain also has its own `Makefile` in its subdirectory for independent builds:

```bash
cd backup && make help
cd trivyscan && make help
cd maintenance && make help
cd certacquirer && make help
```

### Image builds

All four worker images build from a single root `Dockerfile` with one shared builder stage and one runtime *profile* per worker. Each domain `Makefile` picks its profile via `RUNTIME_TARGET`, so the build half is written once:

| Profile (`--target`) | Base | Worker | Runtime user |
|----------------------|------|--------|--------------|
| `runtime-distroless-nonroot` | distroless static | cert-acquirer | non-root |
| `runtime-distroless-root` | distroless static | cleanup | root (reads root-owned SSH key) |
| `runtime-backup` | Debian slim | backup | root (bundles the PostgreSQL 18 client; `pg_dump`/`pg_dumpall` have no Go-native equivalent) |
| `runtime-trivy` | Alpine | trivy-scan | non-root (Trivy CLI, downloaded and SHA256-verified) |

The builder cross-compiles with `CGO_ENABLED=0` (`$BUILDPLATFORM` → `$TARGETARCH`), so the Go toolchain never emulates. Images are built **multi-arch (`linux/amd64` + `linux/arm64`)** since the Nomad clients are a mix of both (e.g. the cleanup-worker is pinned to an arm64 node). For the pure-Go distroless profiles (cleanup, cert) the arm64 leg is free — just the cross-compiled binary in a multi-arch base. The backup (`apt`) and trivy (`apk`) profiles have `RUN` steps in their runtime stage, so their arm64 leg emulates under QEMU; the build host needs binfmt registered (`docker run --privileged --rm tonistiigi/binfmt --install arm64`). BuildKit cache mounts persist the module and build caches across builds and across workers, so the standard library and shared dependencies compile once for the whole fleet.

Adding a pure-Go worker needs **no Dockerfile** — its `Makefile` just sets `IMAGE`, `PKG`, and `RUNTIME_TARGET := runtime-distroless-nonroot`.

### Versioning

Nothing is hand-bumped. Every version is derived from git:

- **Image tags** (all workers + the `temporal-workers-web` image) come from `git describe --tags --always --dirty`, computed in `_common.mk`. `make push-backup` (and `push-all`) tag whatever the current commit resolves to, so an image tag always points back to an exact commit. Override with `VERSION=...` if you ever need to. Each push also publishes `:latest` for the same image — the Nomad jobs pin `:latest`, while the git-describe tag stays as the immutable, traceable alias for that exact build.
- **Release tags** are computed from the conventional-commit history by [`svu`](https://github.com/caarlos0/svu). `make release` runs `svu next`, tags it, and pushes — which triggers the Release workflow. You decide *when* to cut a release; the *number* is derived from the `feat:`/`fix:`/breaking commits since the last tag.

## Deployment

Each domain is deployed as a separate Nomad service job. Workflows are started on cron by Temporal Schedules (Terraform-managed in `infrastructure/terragrunt`), not by Nomad jobs.

### Nomad Jobs

| Job | Type | Image | Task Queue | Workflows |
|-----|------|-------|------------|-----------|
| `backup-worker` | service | `backup-worker` | `backup-task-queue` | `Backup` |
| `trivy-scan-worker` | service | `trivy-scan-worker` | `trivy-task-queue` | `Scan` |
| `cleanup-worker` | service | `cleanup-worker` | `cleanup-task-queue` | `Cleanup`, `RegistryGC`, `AptlyCleanup`, `PostgresMaintenance` |
| `cert-acquirer-worker` | service | `cert-acquirer-worker` | `cert-task-queue` | `CertAcquirer` |

### Manual Runs

Schedules can be triggered on demand (`temporal schedule trigger --schedule-id backup-daily`), or a one-off workflow started directly with the Temporal CLI:

```bash
# Backup
temporal workflow start --task-queue backup-task-queue --type Backup \
  --address temporal-server.service.consul:7233 \
  --input '{"local_days":7,"s3_days":30,"dump_concurrency":4}'

# Trivy scan
temporal workflow start --task-queue trivy-task-queue --type Scan \
  --address temporal-server.service.consul:7233 \
  --input '{"concurrency":10}'

# Node cleanup (dry run)
temporal workflow start --task-queue cleanup-task-queue --type Cleanup \
  --address temporal-server.service.consul:7233 \
  --input '{"data_dir":"/opt/nomad/data","grace_days":7,"dry_run":true,"docker_prune":false}'

# Registry garbage collection (dry run)
temporal workflow start --task-queue cleanup-task-queue --type RegistryGC \
  --address temporal-server.service.consul:7233 \
  --input '{"job_name":"registry","registry_data_dir":"/mnt/gdrive/munchbox-data/registry","registry_image":"registry:3","dry_run":true,"delete_untagged":true}'

# Aptly repository cleanup
temporal workflow start --task-queue cleanup-task-queue --type AptlyCleanup \
  --address temporal-server.service.consul:7233 \
  --input '{"job_name":"aptly","image":"urpylka/aptly:1.6.2","data_dir":"/mnt/gdrive/aptly"}'

# Postgres maintenance (VACUUM ANALYZE every database)
temporal workflow start --task-queue cleanup-task-queue --type PostgresMaintenance \
  --address temporal-server.service.consul:7233 \
  --input '{"concurrency":2}'

# Wildcard certificate acquisition
temporal workflow start --task-queue cert-task-queue --type CertAcquirer \
  --address temporal-server.service.consul:7233 \
  --input '{"domains":["*.munchbox.cc"],"email":"ops@munchbox.cc"}'
```

## Project Structure

```
nomad-temporal-jobs/
  .github/workflows/
    ci.yml                           CI: lint, test, vet, govulncheck
  .gitignore                         Ignores coverage output and dist artifacts
  .golangci.yml                      Linter configuration (gocritic, misspell)
  LICENSE                            MIT
  Makefile                           Root: build, test, lint, push-all targets
  README.md                          This file
  go.mod                             Module definition
  go.sum                             Dependency lock file
  Dockerfile                         Unified image build: shared builder + one runtime profile per worker
  _common.mk                         Shared worker build rules (selects a runtime profile; each domain Makefile includes it)
  shared/
    runtime.go                       RunWorker: common worker bootstrap (each main.go is ~15 lines)
    telemetry.go                     OTel tracer init, span helpers, peer.service attributes
    logging.go                       JSON slog logger with Temporal SDK adapter
    metrics.go                       Prometheus metrics handler via Tally bridge
    temporal.go                      Shared retry-policy + activity-option presets
    heartbeat.go                     WithHeartbeat: run a func while emitting activity heartbeats
    nomad.go                         OTel-instrumented Nomad client + Nomad service (images, nodes, scaling, alloc waits)
    postgres.go                      OTel-instrumented PostgreSQL factory + Postgres maintenance service (list, vacuum)
    s3store.go                       S3Store: multipart upload, listing, retention/quota eviction
    vault.go                         Self-authenticating Vault client (Workload Identity + KV)
    consul.go                        OTel-instrumented Consul client + Consul service (Raft snapshots)
    ssh.go                           Cert-auth SSH client with SFTP file ops (no remote shell)
    docker.go                        Docker API over an SSH socket tunnel (one-shot runs + prune)
  backup/
    Makefile                         Sets IMAGE/PKG/RUNTIME_TARGET (runtime-backup), includes ../_common.mk
    activities/
      activities.go                  Activity struct: snapshots, S3 upload, retention cleanup
    workflows/
      backup.go                      Concurrent snapshot legs + per-database PostgreSQL fan-out
    worker/
      main.go                       Worker entry point (tracing, slog, metrics, Temporal client)
  trivyscan/
    Makefile                         Sets IMAGE/PKG/RUNTIME_TARGET (runtime-trivy) + TRIVY_VERSION, includes ../_common.mk
    activities/
      activities.go                  Activity struct: Nomad image discovery, Trivy scan, DB save
    workflows/
      scan.go                        Bounded-concurrency scanning with per-image error handling
    worker/
      main.go                       Worker entry point (tracing, slog, metrics, Temporal client)
  maintenance/                       cleanup-worker: four workflows on one task queue
    Makefile                         Sets IMAGE/PKG/RUNTIME_TARGET (runtime-distroless-root), includes ../_common.mk
    internal/nodes/                  Shared primitives: NodeInfo, SSH target, HumanBytes,
                                       and the generic find/scale/wait/measure saga activities
    nodecleanup/                     Orphaned data-dir removal over SSH/SFTP (+ optional Docker prune)
      activities.go, workflow.go     node discovery, per-node cleanup, sequential orchestration
    registrygc/                      Docker registry GC saga
      activities.go, workflow.go     one-shot garbage-collect + deferred scale-back compensation
    aptlycleanup/                    aptly `db cleanup` saga (same skeleton as registry GC)
      activities.go, workflow.go     one-shot cleanup + deferred scale-back compensation
    postgresmaint/                   PostgreSQL VACUUM (ANALYZE) maintenance
      activities.go, workflow.go     list databases + bounded-concurrency vacuum fan-out
    worker/
      main.go                        Worker entry point (registers all four workflows + activity sets)
  certacquirer/
    Makefile                         Sets IMAGE/PKG/RUNTIME_TARGET (runtime-distroless-nonroot), includes ../_common.mk
    activities/
      activities.go                  Activity struct: ACME issuance (lego), account + publish to Vault
    workflows/
      cert_acquirer.go               Issue-then-publish orchestration with split retry policies
    worker/
      main.go                       Worker entry point (builds the shared Vault client)
```

## License

MIT
