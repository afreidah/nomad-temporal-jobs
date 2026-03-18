---
title: "System Architecture"
linkTitle: "Architecture"
weight: -1
---

High-level architecture of the Temporal workers showing the trigger flow, worker domains, external services, and observability stack. **Hover over any component** for implementation details.

<style>
  #ac-diagram { margin: 1rem 0; }

  /* floating tooltip */
  #ac-tooltip {
    position: fixed; z-index: 9999;
    max-width: 380px; padding: 0.7rem 0.85rem;
    background: #161b22; border: 1px solid #30363d; border-radius: 6px;
    box-shadow: 0 4px 16px rgba(0,0,0,0.4);
    display: none;
  }
  #ac-tooltip a { color: #34d399; text-decoration: none; }
  #ac-tooltip a:hover { text-decoration: underline; }
  #ac-tooltip h3 { color: #34d399; font-size: 0.85rem; margin: 0 0 0.25rem 0; }
  #ac-tooltip .ac-badge {
    display: inline-block; padding: 1px 7px; border-radius: 4px;
    font-size: 0.6rem; font-weight: 600; margin-bottom: 0.4rem; text-transform: uppercase;
  }
  .ac-badge-entry { background: #05966922; color: #34d399; border: 1px solid #34d39955; }
  .ac-badge-middleware { background: #0d948822; color: #14b8a6; border: 1px solid #14b8a655; }
  .ac-badge-handler { background: #7c3aed22; color: #a78bfa; border: 1px solid #a78bfa55; }
  .ac-badge-data { background: #23863622; color: #3fb950; border: 1px solid #3fb95055; }
  .ac-badge-observability { background: #da363322; color: #f85149; border: 1px solid #f8514955; }
  #ac-tooltip p { font-size: 0.75rem; line-height: 1.4; color: #c9d1d9; margin-bottom: 0.35rem; }
  #ac-tooltip code { background: #21262d; padding: 1px 4px; border-radius: 3px; font-size: 0.7rem; color: #34d399; }

  /* path highlighting */
  #ac-diagram .node, #ac-diagram .edgePath, #ac-diagram .edgeLabel { transition: opacity 0.15s, filter 0.15s; }
  #ac-diagram svg.highlighting .node, #ac-diagram svg.highlighting .edgePath, #ac-diagram svg.highlighting .edgeLabel { opacity: 0.12; }
  #ac-diagram svg.highlighting .node.highlight, #ac-diagram svg.highlighting .edgePath.highlight, #ac-diagram svg.highlighting .edgeLabel.highlight { opacity: 1; filter: drop-shadow(0 0 6px rgba(52,211,153,0.5)); }
  #ac-diagram .node { cursor: pointer; }
</style>

<div id="ac-diagram"></div>
<div id="ac-tooltip"></div>

<script src="https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.min.js"></script>
<script>
(function() {
  var diagramSrc = [
    'flowchart TD',
    '    CRON([Nomad Periodic\\nJobs]):::entry --> TRIGGER[Workflow\\nTrigger Binary]:::entry',
    '    TRIGGER --> TEMPORAL[Temporal\\nServer]:::middleware',
    '',
    '    TEMPORAL --> BTQ[backup\\ntask-queue]:::middleware',
    '    TEMPORAL --> TTQ[trivy\\ntask-queue]:::middleware',
    '    TEMPORAL --> CTQ[cleanup\\ntask-queue]:::middleware',
    '',
    '    BTQ --> BWORKER[Backup\\nWorker]:::handler',
    '    TTQ --> TWORKER[Trivy Scan\\nWorker]:::handler',
    '    CTQ --> CWORKER[Cleanup\\nWorker]:::handler',
    '',
    '    BWORKER --> NOMAD_API[Nomad API]:::data',
    '    BWORKER --> CONSUL_API[Consul API]:::data',
    '    BWORKER --> PG[(PostgreSQL)]:::data',
    '    BWORKER --> S3[S3 Storage]:::data',
    '',
    '    TWORKER --> NOMAD_API',
    '    TWORKER --> TRIVY[Trivy Server]:::data',
    '    TWORKER --> PG',
    '',
    '    CWORKER --> NOMAD_API',
    '    CWORKER --> SSH[SSH\\nNomad Nodes]:::data',
    '',
    '    BWORKER --> TEMPO[Tempo\\nTracing]:::observability',
    '    BWORKER --> PROM[Prometheus\\nMetrics]:::observability',
    '    TWORKER --> TEMPO',
    '    TWORKER --> PROM',
    '    CWORKER --> TEMPO',
    '    CWORKER --> PROM',
    '    TRIGGER --> TEMPO',
    '',
    '    BWORKER --> LOKI[Loki\\nLogging]:::observability',
    '    TWORKER --> LOKI',
    '    CWORKER --> LOKI',
    '',
    '    classDef entry fill:#059669,stroke:#34d399,color:#fff,font-weight:bold',
    '    classDef middleware fill:#0d9488,stroke:#14b8a6,color:#fff',
    '    classDef handler fill:#7c3aed,stroke:#a78bfa,color:#fff',
    '    classDef data fill:#238636,stroke:#3fb950,color:#fff',
    '    classDef observability fill:#da3633,stroke:#f85149,color:#fff'
  ].join('\n');

  mermaid.initialize({
    startOnLoad: false,
    theme: 'dark',
    flowchart: { nodeSpacing: 14, rankSpacing: 22, curve: 'basis', padding: 5, diagramPadding: 8, useMaxWidth: true }
  });

  mermaid.render('arch-mermaid-svg', diagramSrc).then(function(result) {
    document.getElementById('ac-diagram').innerHTML = result.svg;
    wireUpInteractivity();
  });

  var nodeInfo = {
    CRON: {
      title: 'Nomad Periodic Jobs',
      badge: 'entry', badgeText: 'scheduler',
      body: '<p>Three Nomad periodic batch jobs trigger workflows on schedule:</p><p><b>Backup:</b> daily at 2 AM<br><b>Trivy scan:</b> daily at 3 AM<br><b>Node cleanup:</b> daily at 5 AM</p><p>Each job runs the shared trigger binary with <code>WORKFLOW_NAME</code> set to the target workflow.</p>'
    },
    TRIGGER: {
      title: 'Workflow Trigger Binary',
      badge: 'entry', badgeText: 'dispatcher',
      body: '<p>Single Go binary (<code>cmd/trigger/main.go</code>) that dispatches all workflows. Initializes OTel tracing, connects to Temporal, and starts the workflow selected by <code>WORKFLOW_NAME</code> env var.</p><p>Supports: <code>backup</code>, <code>trivy</code>, <code>cleanup</code>. Passes configuration via environment variables (<code>LOCAL_RETENTION_DAYS</code>, <code>S3_RETENTION_DAYS</code>, <code>GRACE_DAYS</code>, <code>DRY_RUN</code>, etc.).</p><p>Waits for completion and logs the result before exiting.</p>'
    },
    TEMPORAL: {
      title: 'Temporal Server',
      badge: 'middleware', badgeText: 'orchestration',
      body: '<p>Central workflow orchestration engine. Routes workflow tasks to the correct worker via task queues. Handles durable execution, retry scheduling, and workflow history.</p><p>Workers connect with Temporal SDK v1.41.0 and OpenTelemetry interceptor for distributed tracing across workflow and activity boundaries.</p>'
    },
    BTQ: {
      title: 'backup-task-queue',
      badge: 'middleware', badgeText: 'task queue',
      body: '<p>Temporal task queue for backup workflow and activities. Only the backup worker polls this queue.</p><p>Retry policy: 1s initial interval, 2.0 backoff, 1m max interval, 3 max attempts.</p>'
    },
    TTQ: {
      title: 'trivy-task-queue',
      badge: 'middleware', badgeText: 'task queue',
      body: '<p>Temporal task queue for trivy scan workflow and activities. Only the trivy scan worker polls this queue.</p><p>Retry policy: 1s initial interval, 2.0 backoff, 1m max interval, 3 max attempts.</p>'
    },
    CTQ: {
      title: 'cleanup-task-queue',
      badge: 'middleware', badgeText: 'task queue',
      body: '<p>Temporal task queue for node cleanup workflow and activities. Only the cleanup worker polls this queue.</p><p>Retry policy: 1s initial interval, 2.0 backoff, 1m max interval, 3 max attempts.</p>'
    },
    BWORKER: {
      title: 'Backup Worker',
      badge: 'handler', badgeText: 'worker',
      body: '<p>Orchestrates sequential infrastructure backups: Nomad Raft snapshot &rarr; Consul Raft snapshot &rarr; PostgreSQL pg_dumpall &rarr; S3 uploads &rarr; retention cleanup.</p><p>Snapshot failures terminate the workflow. S3 upload failures are logged as warnings but do not block &mdash; local backups always succeed.</p><p>Quick activity timeouts: 5min start-to-close / 15min schedule-to-close. Long timeouts for PostgreSQL: 30min / 60min.</p><p><a href="../backup-workflow/">Backup workflow diagram &rarr;</a></p>'
    },
    TWORKER: {
      title: 'Trivy Scan Worker',
      badge: 'handler', badgeText: 'worker',
      body: '<p>Discovers all running Docker images from Nomad allocations and scans them in parallel batches of 10 using a Trivy server.</p><p>Errors are classified: <b>transient</b> (connection refused, timeout) trigger retries; <b>permanent</b> (manifest unknown, not found) are logged and skipped.</p><p>Results saved individually to PostgreSQL to stay under the 2MB Temporal payload limit. Aggregate CVE counts logged at completion.</p><p><a href="../trivyscan-workflow/">Trivy scan workflow diagram &rarr;</a></p>'
    },
    CWORKER: {
      title: 'Cleanup Worker',
      badge: 'handler', badgeText: 'worker',
      body: '<p>Connects to each Nomad client node via SSH and removes orphaned job data directories. Nodes processed sequentially.</p><p>Excludes system dirs (alloc, plugins, tmp, server, client). Grace period filter skips recently modified directories. Optional Docker image prune.</p><p>Dry-run mode enabled by default for safe preview. Activity timeout: 10min start-to-close / 30min schedule-to-close per node.</p><p><a href="../nodecleanup-workflow/">Node cleanup workflow diagram &rarr;</a></p>'
    },
    NOMAD_API: {
      title: 'Nomad API',
      badge: 'data', badgeText: 'external service',
      body: '<p>HashiCorp Nomad HTTP API. Used by all three workers:</p><p><b>Backup:</b> <code>nomad operator snapshot save</code> for Raft snapshots.<br><b>Trivy:</b> <code>/v1/allocations</code> to discover running Docker images.<br><b>Cleanup:</b> <code>/v1/nodes</code> to list client nodes; local agent API on each node for running job enumeration.</p><p>All calls wrapped with OTel-instrumented HTTP transport via <code>shared.NewNomadClient()</code>. Produces <code>peer.service: nomad</code> service graph edges.</p>'
    },
    CONSUL_API: {
      title: 'Consul API',
      badge: 'data', badgeText: 'external service',
      body: '<p>HashiCorp Consul HTTP API. Used by the backup worker for <code>consul snapshot save</code> to capture Raft state.</p><p>Snapshot stored locally on NFS mount, then uploaded to S3.</p>'
    },
    PG: {
      title: 'PostgreSQL',
      badge: 'data', badgeText: 'database',
      body: '<p>Used by two workers:</p><p><b>Backup:</b> <code>pg_dumpall | gzip</code> for full database backup. Long timeout (30min/60min) for large databases.</p><p><b>Trivy:</b> Stores scan results transactionally &mdash; scan record + individual vulnerabilities. Deduplicates by vuln ID, truncates long descriptions to 1000 chars. Connection wrapped with <code>otelsql</code> for trace propagation.</p>'
    },
    S3: {
      title: 'S3 Storage',
      badge: 'data', badgeText: 'external service',
      body: '<p>Offsite backup storage via AWS SDK v2. Uploads are non-fatal &mdash; failures logged as warnings.</p><p>Smart quota handling: on <code>QuotaExceeded</code> errors, automatically evicts the oldest object and retries (up to 3 eviction attempts).</p><p>S3 retention cleanup deletes objects older than the configured retention period (default: 30 days).</p>'
    },
    TRIVY: {
      title: 'Trivy Server',
      badge: 'data', badgeText: 'external service',
      body: '<p>Aqua Trivy vulnerability scanner in server mode. Scans container images via <code>trivy image --server --format json --timeout 10m</code>.</p><p>Returns JSON results with vulnerability counts by severity (critical, high, medium, low). Error classification: manifest unknown and image not found are permanent; connection errors and timeouts are transient.</p>'
    },
    SSH: {
      title: 'SSH (Nomad Nodes)',
      badge: 'data', badgeText: 'external service',
      body: '<p>SSH connections to each Nomad client node for cleanup operations. Uses certificate-based authentication with configurable key, cert, and host CA paths.</p><p>Oracle nodes detected by name prefix &mdash; uses <code>ubuntu</code> user with <code>sudo</code> instead of <code>root</code>.</p><p>Executes a generated bash script that enumerates directories, checks grace periods, and optionally prunes Docker images.</p>'
    },
    TEMPO: {
      title: 'Tempo (Tracing)',
      badge: 'observability', badgeText: 'observability',
      body: '<p>Distributed tracing backend. All workers export spans via OTLP gRPC (default: <code>tempo.service.consul:4317</code>).</p><p>Client spans with <code>peer.service</code> attributes produce service graph edges in Grafana for every external call. The trigger binary also traces its workflow dispatch call.</p>'
    },
    PROM: {
      title: 'Prometheus (Metrics)',
      badge: 'observability', badgeText: 'observability',
      body: '<p>Each worker exposes Temporal SDK metrics on <code>:9090/metrics</code> via a Tally-Prometheus bridge.</p><p>Metrics include workflow/activity latency, task queue depth, retry counts, and failure rates. Scraped by Prometheus for dashboards and alerting.</p>'
    },
    LOKI: {
      title: 'Loki (Logging)',
      badge: 'observability', badgeText: 'observability',
      body: '<p>All workers emit JSON structured logs to stdout via Go <code>log/slog</code>. Collected by Grafana Alloy and shipped to Loki.</p><p>A custom <code>tlog.Logger</code> adapter bridges Temporal SDK logging into slog, ensuring all workflow and activity logs are captured with consistent structure.</p>'
    }
  };

  var tooltip = document.getElementById('ac-tooltip');
  var mouseX = 0, mouseY = 0;

  var pinned = false;
  document.addEventListener('mousemove', function(e) {
    mouseX = e.clientX;
    mouseY = e.clientY;
    if (tooltip.style.display === 'block' && !pinned) positionTooltip();
  });

  function positionTooltip() {
    var pad = 12;
    var x = mouseX + pad, y = mouseY + pad;
    if (x + tooltip.offsetWidth > window.innerWidth - pad) x = mouseX - tooltip.offsetWidth - pad;
    if (y + tooltip.offsetHeight > window.innerHeight - pad) y = mouseY - tooltip.offsetHeight - pad;
    tooltip.style.left = x + 'px';
    tooltip.style.top = y + 'px';
  }

  function showInfo(id) {
    var info = nodeInfo[id];
    if (!info) { tooltip.style.display = 'none'; pinned = false; return; }
    tooltip.innerHTML = '<h3>' + info.title + '</h3><span class="ac-badge ac-badge-' + info.badge + '">' + info.badgeText + '</span>' + info.body;
    pinned = false;
    tooltip.style.display = 'block';
    positionTooltip();
    if (tooltip.querySelector('a')) pinned = true;
  }

  var hideTimer = null;
  var hoveringTooltip = false;
  var hoveringNode = false;

  tooltip.addEventListener('mouseenter', function() { hoveringTooltip = true; clearTimeout(hideTimer); });
  tooltip.addEventListener('mouseleave', function() {
    hoveringTooltip = false;
    hideTimer = setTimeout(function() {
      if (!hoveringNode && !hoveringTooltip) clearInfo();
    }, 100);
  });

  function clearInfo() {
    tooltip.style.display = 'none';
    pinned = false;
    var svg = document.querySelector('#ac-diagram svg');
    if (svg) {
      svg.classList.remove('highlighting');
      svg.querySelectorAll('.highlight').forEach(function(el) { el.classList.remove('highlight'); });
    }
  }

  function wireUpInteractivity() {
    var svg = document.querySelector('#ac-diagram svg');
    if (!svg) return;

    var adj = {}, edgeMap = {};
    svg.querySelectorAll('.edgePath').forEach(function(ep, i) {
      var cls = ep.getAttribute('class') || '';
      var m = cls.match(/LS-(\S+)/), m2 = cls.match(/LE-(\S+)/);
      if (!m || !m2) return;
      var from = m[1], to = m2[1];
      edgeMap[i] = { from: from, to: to, path: ep, label: svg.querySelectorAll('.edgeLabel')[i] };
      (adj[from] = adj[from] || []).push(i);
    });

    function bfs(startId, adjacency, getNext) {
      var visited = new Set([startId]), edges = new Set(), queue = [startId];
      while (queue.length) {
        var cur = queue.shift();
        (adjacency[cur] || []).forEach(function(ei) {
          edges.add(ei);
          var next = getNext(edgeMap[ei]);
          if (!visited.has(next)) { visited.add(next); queue.push(next); }
        });
      }
      return { nodes: visited, edges: edges };
    }

    var radj = {};
    Object.keys(edgeMap).forEach(function(i) {
      var e = edgeMap[i];
      (radj[e.to] = radj[e.to] || []).push(Number(i));
    });

    svg.querySelectorAll('.node').forEach(function(node) {
      var id = node.id.replace(/^flowchart-/, '').replace(/-\d+$/, '');

      node.addEventListener('mouseenter', function() {
        hoveringNode = true;
        clearTimeout(hideTimer);
        svg.classList.add('highlighting');
        var fwd = bfs(id, adj, function(e) { return e.to; });
        var bwd = bfs(id, radj, function(e) { return e.from; });
        var allNodes = new Set([...fwd.nodes, ...bwd.nodes]);
        var allEdges = new Set([...fwd.edges, ...bwd.edges]);

        svg.querySelectorAll('.node').forEach(function(n) {
          var nid = n.id.replace(/^flowchart-/, '').replace(/-\d+$/, '');
          n.classList.toggle('highlight', allNodes.has(nid));
        });
        Object.keys(edgeMap).forEach(function(i) {
          var hl = allEdges.has(Number(i));
          edgeMap[i].path.classList.toggle('highlight', hl);
          if (edgeMap[i].label) edgeMap[i].label.classList.toggle('highlight', hl);
        });
        showInfo(id);
      });

      node.addEventListener('mouseleave', function() {
        hoveringNode = false;
        hideTimer = setTimeout(function() {
          if (!hoveringNode && !hoveringTooltip) clearInfo();
        }, 100);
      });
    });
  }
})();
</script>

## Legend

| Color | Meaning |
|-------|---------|
| <span style="color:#34d399">**Emerald**</span> | Entry points (schedulers, triggers) |
| <span style="color:#14b8a6">**Teal**</span> | Middleware (Temporal, task queues) |
| <span style="color:#a78bfa">**Purple**</span> | Workers |
| <span style="color:#3fb950">**Green**</span> | External data stores & services |
| <span style="color:#f85149">**Red**</span> | Observability |
