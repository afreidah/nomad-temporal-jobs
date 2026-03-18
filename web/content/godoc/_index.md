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
<p>OpenTelemetry tracing, Prometheus metrics, structured logging, and Nomad client factory.</p>
</div>
</a>

<a class="landing-card" href="backup-activities/">
<div>
<strong>backup/activities</strong>
<p>Nomad, Consul, and PostgreSQL snapshot activities with S3 upload and retention cleanup.</p>
</div>
</a>

<a class="landing-card" href="backup-workflows/">
<div>
<strong>backup/workflows</strong>
<p>Sequential backup orchestration with non-fatal S3 uploads and configurable retention.</p>
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
<p>Node discovery, SSH-based orphaned directory cleanup, and Docker pruning.</p>
</div>
</a>

<a class="landing-card" href="nodecleanup-workflows/">
<div>
<strong>nodecleanup/workflows</strong>
<p>Sequential node cleanup orchestration with dry-run and grace period defaults.</p>
</div>
</a>

</div>
