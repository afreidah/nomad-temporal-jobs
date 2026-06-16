---
title: "nomad-temporal-jobs diagrams"
linkTitle: "Diagrams"
chapter: true
weight: 20
---

<div class="landing-subheader">Interactive architecture and workflow diagrams for the Temporal workers.</div>

<div class="landing-grid">

<a class="landing-card" href="architecture/">
<div>
<strong>Architecture Overview</strong>
<p>All workers, Temporal server, and external services.</p>
</div>
</a>

<a class="landing-card" href="backup-workflow/">
<div>
<strong>Backup Workflow</strong>
<p>Concurrent snapshot legs with a per-database PostgreSQL fan-out and non-fatal S3 uploads.</p>
</div>
</a>

<a class="landing-card" href="trivyscan-workflow/">
<div>
<strong>Trivy Scan Workflow</strong>
<p>Image discovery, batched parallel scanning, and result persistence.</p>
</div>
</a>

<a class="landing-card" href="nodecleanup-workflow/">
<div>
<strong>Node Cleanup Workflow</strong>
<p>Nomad-API job discovery and SFTP-based directory cleanup with dry-run and grace-period safety.</p>
</div>
</a>

<a class="landing-card" href="registry-gc-workflow/">
<div>
<strong>Registry GC Workflow</strong>
<p>Saga-style scale-down, garbage-collect, and guaranteed scale-back compensation.</p>
</div>
</a>

</div>
