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
<p>All workers, Temporal server, trigger binary, and external services.</p>
</div>
</a>

<a class="landing-card" href="backup-workflow/">
<div>
<strong>Backup Workflow</strong>
<p>Sequential snapshot and upload flow with non-fatal S3 uploads.</p>
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
<p>Node discovery, SSH-based cleanup with dry-run and grace period safety.</p>
</div>
</a>

</div>
