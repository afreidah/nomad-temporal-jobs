---
title: "Temporal workflows for a Nomad cluster"
description: "The recurring work a Nomad, Consul and Vault cluster needs, written as Temporal workflows: Raft snapshots, image scanning, storage reclamation, certificate and token renewal, and on-demand CI runners."
archetype: "home"
---

<div style="text-align: center; margin-bottom: 1rem;">
  <img src="/images/logo.png" alt="nomad-temporal-jobs" style="max-width: 350px; height: auto;" class="nolightbox">
</div>

<div style="text-align: center; margin-top: 1.5rem;">

{{% button style="primary" href="diagrams/architecture/" %}}Architecture{{% /button %}}
{{% button style="primary" href="godoc/" %}}Go API{{% /button %}}
{{% button style="primary" href="https://github.com/afreidah/nomad-temporal-jobs" %}}GitHub{{% /button %}}

</div>

---

<h1 class="hero-heading" style="color: #34d399;">Temporal workflows for a Nomad cluster</h1>

<p class="hero-lead">
The recurring work a Nomad, Consul and Vault cluster needs &mdash; Raft snapshots, image
scanning, storage reclamation, certificate and token renewal &mdash; written as Temporal
workflows. Seven workers reach the cluster through native Go clients and never a remote
shell, so a run that fails part-way resumes where it stopped, and one that takes a service
offline to do its work always brings it back.
</p>

---

## What runs

<div class="hero-bullets">

- **Backups** (daily) &mdash; Nomad and Consul Raft snapshots plus per-database PostgreSQL dumps, the three legs concurrent, joined before retention cleanup, replicated to S3
- **Vulnerability scanning** (daily) &mdash; every running image discovered from Nomad, scanned through Trivy under bounded concurrency, CVEs persisted to PostgreSQL
- **Node cleanup** (daily) &mdash; orphaned job data directories removed across every ready client over SFTP, honoring a grace period, with an optional Docker prune
- **Registry GC** (weekly) &mdash; scales the registry offline, garbage-collects, and always scales it back
- **Aptly cleanup** (weekly) &mdash; the same saga, releasing the single-writer leveldb lock before `aptly db cleanup`
- **PostgreSQL maintenance** (weekly) &mdash; online `VACUUM (ANALYZE)` across every database, bounded so the burst never overwhelms the primary
- **Certificate renewal** (weekly) &mdash; ACME DNS-01 wildcard issued and published to the Vault path Traefik reads
- **Token renewal** (weekly) &mdash; a fresh GitHub App installation token and a per-repo SonarCloud token written into each repo's Actions secrets, replacing hand-rotated PATs
- **Media reconciliation** &mdash; completed Deluge downloads reconciled into the Sonarr/Radarr library
- **CI runner scaling** (continuous) &mdash; queued self-hosted jobs reconciled against active runners, dispatching one ephemeral Nomad runner per shortfall and reaping it after

</div>

---

## How it's built

<div class="hero-bullets">

- **Workflows do no I/O.** Every workflow is pure orchestration; all side effects live in activities, so the deterministic replay Temporal depends on stays intact.
- **Native clients, not shells.** SSH/SFTP, Docker over an SSH tunnel, Nomad, Consul, Vault, PostgreSQL, S3, GitHub and SonarCloud each have an instrumented Go client. A worker shells out only where no Go client exists.
- **Compensation that always fires.** The registry and aptly sagas scale a service offline to do their work and restore it from a deferred step on a disconnected context &mdash; so the scale-back runs even when the job fails or the workflow is cancelled.
- **Failure is per item, not per run.** A database, repo, or image that fails is recorded and the run continues; the workflow reports the failure once everything has been attempted.
- **One shared runtime.** `RunWorker` wires OTel tracing, structured logging, Prometheus metrics and a traced Temporal client, so a worker's `main()` only declares its identity and registers its workflows.

</div>
