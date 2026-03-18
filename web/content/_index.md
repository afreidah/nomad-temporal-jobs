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
{{% badge style="warning" title=" " icon="fas fa-fire" %}}Prometheus Metrics{{% /badge %}}
{{% badge style="primary" icon="fas fa-project-diagram" %}}OpenTelemetry{{% /badge %}}

</div>

<div style="text-align: center; margin-top: 1.5rem;">

{{% button style="primary" href="diagrams/architecture/" %}}Architecture{{% /button %}}
{{% button style="primary" href="godoc/" %}}Go API{{% /button %}}
{{% button style="primary" href="https://github.com/afreidah/nomad-temporal-jobs" %}}GitHub{{% /button %}}

</div>

---

<h2 style="text-align: center; color: #34d399;">Temporal workflow workers for infrastructure automation</h2>

<p style="text-align: center; max-width: 700px; margin: 0 auto; color: #94a3b8; font-size: 1.1rem;">
Three independent Temporal workers handle nightly backup orchestration, container vulnerability scanning, and orphaned data cleanup across Nomad client nodes.
</p>

<div class="hero-bullets">

- Automated nightly backups of Nomad, Consul, and PostgreSQL with S3 offsite replication and configurable retention
- Vulnerability scanning of all running container images with parallel batched Trivy scans and CVE persistence
- Orphaned data directory cleanup across Nomad nodes with dry-run safety and grace period filtering

</div>

---

## Key Features

<div class="feature-grid">

<div class="feature-item">
<div>
<strong>Automated Backups</strong>
<p>Nomad Raft, Consul Raft, and PostgreSQL snapshots with S3 upload and retention cleanup.</p>
<div class="feature-detail">Scheduled daily at 2 AM. Snapshots are stored locally on NFS and uploaded to S3. S3 uploads are non-fatal &mdash; local backups succeed even if S3 is unreachable. Configurable retention: 7 days local, 30 days S3 by default.</div>
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
<p>SSH to each Nomad client node, identify orphaned directories, and remove stale data safely.</p>
<div class="feature-detail">Connects to every Nomad client node via SSH. Enumerates job data directories, cross-references against running jobs, and removes orphaned entries older than the grace period. Optional Docker image pruning. Dry-run mode enabled by default for safe preview.</div>
</div>
</div>

<div class="feature-item">
<div>
<strong>OpenTelemetry Tracing</strong>
<p>Every activity traced end-to-end with Tempo export and service graph edges.</p>
<div class="feature-detail">All workers initialize an OTLP gRPC exporter to Tempo. Client spans with peer.service attributes produce service graph edges in Grafana for every external call &mdash; Nomad, Consul, PostgreSQL, S3, and Trivy.</div>
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

