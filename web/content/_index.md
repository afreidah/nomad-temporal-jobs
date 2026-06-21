---
title: "nomad-temporal-jobs"
archetype: "home"
---

<div style="text-align: center; margin-bottom: 1rem;">
  <img src="/images/logo.png" alt="nomad-temporal-jobs" style="max-width: 350px; height: auto;" class="nolightbox">
</div>

<div class="badge-grid">

{{% badge style="primary" icon="fas fa-clock" %}}Temporal Workflows{{% /badge %}}
{{% badge style="info" title=" " icon="fas fa-database" %}}Automated Backups{{% /badge %}}
{{% badge style="red" title=" " icon="fas fa-shield-alt" %}}Trivy Scanning{{% /badge %}}
{{% badge style="green" icon="fas fa-broom" %}}Node Cleanup{{% /badge %}}
{{% badge style="green" title=" " icon="fas fa-recycle" %}}Registry GC{{% /badge %}}
{{% badge style="green" title=" " icon="fas fa-box-archive" %}}Aptly Cleanup{{% /badge %}}
{{% badge style="info" title=" " icon="fas fa-database" %}}Postgres Maintenance{{% /badge %}}
{{% badge style="info" title=" " icon="fas fa-certificate" %}}Cert Renewal{{% /badge %}}
{{% badge style="info" title=" " icon="fas fa-key" %}}GitHub Token Renewal{{% /badge %}}
{{% badge style="warning" title=" " icon="fas fa-fire" %}}Prometheus Metrics{{% /badge %}}
{{% badge style="primary" icon="fas fa-project-diagram" %}}OpenTelemetry{{% /badge %}}

</div>

<div style="text-align: center; margin-top: 1.5rem;">

{{% button style="primary" href="diagrams/architecture/" %}}Architecture{{% /button %}}
{{% button style="primary" href="godoc/" %}}Go API{{% /button %}}
{{% button style="primary" href="https://github.com/afreidah/nomad-temporal-jobs" %}}GitHub{{% /button %}}

</div>

---

<h2 class="hero-heading" style="color: #34d399;">Temporal workers for infrastructure automation</h2>

<p class="hero-lead">
Five independent Temporal workers run eight scheduled infrastructure jobs you'd otherwise babysit &mdash; backups, vulnerability scanning, node cleanup, registry and aptly garbage collection, PostgreSQL maintenance, certificate renewal, and GitHub CI-token renewal. Every remote operation goes through a native Go API or library, never a remote shell.
</p>

<div class="hero-bullets">

- **Backups** &mdash; nightly Nomad, Consul, and PostgreSQL snapshots with S3 offsite replication and configurable retention
- **Vulnerability scanning** &mdash; parallel batched Trivy scans of every running image, with CVE results persisted to PostgreSQL
- **Node cleanup** &mdash; orphaned data directories removed across Nomad nodes over SFTP, with dry-run safety and a grace period
- **Registry GC** &mdash; scales the registry offline, garbage-collects, and always scales it back via saga compensation
- **Aptly cleanup** &mdash; the same saga pattern for `aptly db cleanup`, releasing the leveldb lock and reclaiming pool storage
- **Postgres maintenance** &mdash; online `VACUUM (ANALYZE)` across every database with bounded concurrency
- **Cert renewal** &mdash; ACME DNS-01 wildcard issued to Vault, self-authenticating via Nomad Workload Identity
- **GitHub token renewal** &mdash; a GitHub App mints short-lived, repo-scoped tokens into each repo's Actions secret every run, so CI/release tokens never expire and no PATs are hand-rotated

</div>

---

## Key Features

<div class="feature-grid">

<div class="feature-item">
<div>
<strong>Automated Backups</strong>
<p>Nomad Raft, Consul Raft, and PostgreSQL snapshots with S3 upload and retention cleanup.</p>
<div class="feature-detail">Runs as a Nomad periodic job. Snapshots are stored locally on NFS and uploaded to S3. S3 uploads are non-fatal &mdash; local backups succeed even if S3 is unreachable. Configurable retention: 7 days local, 30 days S3 by default.</div>
</div>
</div>

<div class="feature-item">
<div>
<strong>Vulnerability Scanning</strong>
<p>Discover images from Nomad, batch parallel scans via Trivy, persist CVE results to PostgreSQL.</p>
<div class="feature-detail">Automatically discovers all running Docker images from Nomad allocations. Scans in parallel batches of 10 using a Trivy server. Classifies errors as transient (retryable) or permanent (skipped). Results stored in PostgreSQL with per-vulnerability detail.</div>
</div>
</div>

<div class="feature-item">
<div>
<strong>Node Cleanup</strong>
<p>Identify orphaned directories on each Nomad client node and remove stale data safely.</p>
<div class="feature-detail">Gets each node's running jobs from the Nomad API, then enumerates and deletes orphaned job data directories over SFTP &mdash; no remote shell. Removes only entries older than the grace period. Optional Docker image pruning via the Docker API. Dry-run mode enabled by default for safe preview.</div>
</div>
</div>

<div class="feature-item">
<div>
<strong>Registry Garbage Collection</strong>
<p>Reclaim Docker registry storage with a saga that never leaves the registry offline.</p>
<div class="feature-detail">Scales the registry Nomad job to 0, waits for allocations to drain, runs <code>garbage-collect</code> as a one-shot container through the Docker API (tunneled over SSH), then scales back to 1. The scale-back is a deferred compensation on a disconnected context &mdash; it always fires, even if GC fails or the workflow is cancelled. Reports blobs deleted and bytes reclaimed.</div>
</div>
</div>

<div class="feature-item">
<div>
<strong>Aptly Cleanup</strong>
<p>Reclaim Debian repository pool storage with the same offline-safe saga as registry GC.</p>
<div class="feature-detail">Scales the aptly Nomad job to 0 so the server releases its single-writer leveldb lock, runs <code>aptly db cleanup</code> as a one-shot container through the Docker API (tunneled over SSH), then scales back to 1 via the same deferred compensation. Shares the find / scale / wait / measure saga activities with registry GC. Reports bytes reclaimed.</div>
</div>
</div>

<div class="feature-item">
<div>
<strong>Postgres Maintenance</strong>
<p>Online <code>VACUUM (ANALYZE)</code> across every database with bounded concurrency.</p>
<div class="feature-detail">Lists the cluster's databases from the primary, then vacuums each with a bounded-concurrency fan-out (default 2) so the maintenance burst never overwhelms the primary. Online and lock-light &mdash; no <code>FULL</code>. A per-database failure is recorded and the run continues, failing the workflow only after every database has been attempted.</div>
</div>
</div>

<div class="feature-item">
<div>
<strong>Certificate Acquisition</strong>
<p>Renew the <code>*.munchbox.cc</code> wildcard via ACME DNS-01 and publish it to Vault.</p>
<div class="feature-detail">Issues the wildcard via Let's Encrypt DNS-01 (Cloudflare, go-acme/lego), persisting the ACME account to Vault so registration happens once. Issue and publish are split so a publish retry never re-runs ACME (rate limits), and the private key never transits workflow history. Self-authenticates with its Nomad Workload Identity &mdash; no static secrets.</div>
</div>
</div>

<div class="feature-item">
<div>
<strong>GitHub Token Renewal</strong>
<p>Mint short-lived GitHub App tokens into each repo's Actions secret so CI tokens never expire.</p>
<div class="feature-detail">Reads the managed repo list from Consul, then for each repo mints a repo-scoped GitHub App installation token (<code>contents</code> + <code>pull-requests: write</code>), seals it with a NaCl box against the repo's Actions public key, and writes it to the <code>RELEASE_PAT</code> secret &mdash; all via native go-github, never the <code>gh</code> CLI. Re-minting on every run replaces hand-rotated Personal Access Tokens. Self-authenticates with its Nomad Workload Identity, pulling the App key from Vault.</div>
</div>
</div>

<div class="feature-item">
<div>
<strong>OpenTelemetry Tracing</strong>
<p>Every activity traced end-to-end with Tempo export and service graph edges.</p>
<div class="feature-detail">All workers initialize an OTLP gRPC exporter to Tempo. Client spans with peer.service attributes produce service graph edges in Grafana for every external call &mdash; Nomad, Consul, PostgreSQL, S3, Trivy, Vault, ACME, and Cloudflare.</div>
</div>
</div>

<div class="feature-item">
<div>
<strong>Prometheus Metrics</strong>
<p>Temporal SDK metrics via Tally-Prometheus bridge exposed on :9090/metrics.</p>
<div class="feature-detail">Exposes workflow and activity latency, task queue depth, retry counts, and failure rates. Each worker runs its own metrics HTTP handler. Scraped by Prometheus for dashboard and alerting integration.</div>
</div>
</div>

<div class="feature-item">
<div>
<strong>Structured Logging</strong>
<p>JSON slog output with service identity for Alloy/Loki collection.</p>
<div class="feature-detail">Uses Go's log/slog with JSON handler writing to stdout. A custom adapter bridges Temporal SDK logging into slog. Service name injected as a default attribute for log correlation in Loki.</div>
</div>
</div>

</div>

