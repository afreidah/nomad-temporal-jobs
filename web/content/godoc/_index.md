---
title: "nomad-temporal-jobs Go API"
linkTitle: "Go API"
chapter: true
weight: 30
---

<div class="landing-subheader">Generated documentation for the Go packages in this project.</div>

<div class="landing-grid">

<a class="landing-card" href="shared/">
<div>
<strong>shared</strong>
<p>Worker runtime bootstrap, OpenTelemetry tracing, Prometheus metrics, structured logging, retry/activity-option presets, and the instrumented client factories: Nomad, Postgres, self-authenticating Vault, Consul, SSH/SFTP, and Docker-over-SSH.</p>
</div>
</a>

<a class="landing-card" href="backup-activities/">
<div>
<strong>backup/activities</strong>
<p>Native Nomad/Consul Raft snapshots, per-database PostgreSQL dumps, multipart S3 upload, and retention cleanup.</p>
</div>
</a>

<a class="landing-card" href="backup-workflows/">
<div>
<strong>backup/workflows</strong>
<p>Concurrent snapshot legs with a per-database PostgreSQL fan-out, non-fatal S3 uploads, and configurable retention.</p>
</div>
</a>

<a class="landing-card" href="trivyscan-activities/">
<div>
<strong>trivyscan/activities</strong>
<p>Image discovery from Nomad, Trivy scanning, and PostgreSQL result storage.</p>
</div>
</a>

<a class="landing-card" href="trivyscan-workflows/">
<div>
<strong>trivyscan/workflows</strong>
<p>Batched parallel scanning with transient vs permanent error classification.</p>
</div>
</a>

<a class="landing-card" href="nodecleanup-activities/">
<div>
<strong>nodecleanup/activities</strong>
<p>Node discovery, orphaned-directory cleanup over SFTP (running jobs from the Nomad API), Docker-API pruning, and the registry-GC / aptly saga steps.</p>
</div>
</a>

<a class="landing-card" href="nodecleanup-workflows/">
<div>
<strong>nodecleanup/workflows</strong>
<p>Sequential node cleanup plus the registry-GC, aptly-cleanup, and postgres-maintenance workflows with deferred scale-back compensation.</p>
</div>
</a>

<a class="landing-card" href="certacquirer-activities/">
<div>
<strong>certacquirer/activities</strong>
<p>ACME DNS-01 wildcard issuance (go-acme/lego, Cloudflare) with the ACME account and issued cert persisted to Vault.</p>
</div>
</a>

<a class="landing-card" href="certacquirer-workflows/">
<div>
<strong>certacquirer/workflows</strong>
<p>Issue-then-publish orchestration with split retry policies so a publish retry never re-runs ACME issuance.</p>
</div>
</a>

</div>
