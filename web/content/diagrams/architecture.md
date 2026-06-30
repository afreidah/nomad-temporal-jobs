---
title: "System Architecture"
linkTitle: "Architecture"
weight: -1
---

High-level architecture of the Temporal workers showing the schedule flow, worker domains, external services, and observability stack. **Hover over any component** for implementation details.

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

<script src="https://cdn.jsdelivr.net/npm/mermaid@11.8.0/dist/mermaid.min.js"></script>
<script>
(function() {
  var diagramSrc = [
    'flowchart TD',
    '    SCHED([Temporal Schedules\\nTerraform-managed]):::entry --> TEMPORAL[Temporal\\nServer]:::middleware',
    '',
    '    TEMPORAL --> BTQ[backup\\ntask-queue]:::middleware',
    '    TEMPORAL --> TTQ[trivy\\ntask-queue]:::middleware',
    '    TEMPORAL --> CTQ[cleanup\\ntask-queue]:::middleware',
    '    TEMPORAL --> CETQ[cert\\ntask-queue]:::middleware',
    '    TEMPORAL --> GTQ[github-token\\ntask-queue]:::middleware',
    '    TEMPORAL --> RSTQ[runner-scaler\\ntask-queue]:::middleware',
    '',
    '    BTQ --> BWORKER[Backup\\nWorker]:::handler',
    '    TTQ --> TWORKER[Trivy Scan\\nWorker]:::handler',
    '    CTQ --> CWORKER[Cleanup\\nWorker]:::handler',
    '    CETQ --> CEWORKER[Cert Acquirer\\nWorker]:::handler',
    '    GTQ --> GWORKER[GitHub Token\\nRenewer Worker]:::handler',
    '    RSTQ --> RSWORKER[Runner Scaler\\nWorker]:::handler',
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
    '    CWORKER --> SSH[SSH / SFTP\\n+ Docker tunnel]:::data',
    '    CWORKER --> PG',
    '',
    '    CEWORKER --> VAULT[Vault]:::data',
    '    CEWORKER --> ACME[ACME\\nLetsEncrypt]:::data',
    '    CEWORKER --> CF[Cloudflare\\nDNS]:::data',
    '',
    '    GWORKER --> VAULT',
    '    GWORKER --> CONSUL_API',
    '    GWORKER --> GITHUB[GitHub\\nApp API]:::data',
    '',
    '    RSWORKER --> VAULT',
    '    RSWORKER --> CONSUL_API',
    '    RSWORKER --> GITHUB',
    '    RSWORKER --> NOMAD_API',
    '',
    '    BWORKER --> TEMPO[Tempo\\nTracing]:::observability',
    '    BWORKER --> PROM[Prometheus\\nMetrics]:::observability',
    '    TWORKER --> TEMPO',
    '    TWORKER --> PROM',
    '    CWORKER --> TEMPO',
    '    CWORKER --> PROM',
    '    CEWORKER --> TEMPO',
    '    CEWORKER --> PROM',
    '    GWORKER --> TEMPO',
    '    GWORKER --> PROM',
    '    RSWORKER --> TEMPO',
    '    RSWORKER --> PROM',
    '',
    '    BWORKER --> LOKI[Loki\\nLogging]:::observability',
    '    TWORKER --> LOKI',
    '    CWORKER --> LOKI',
    '    CEWORKER --> LOKI',
    '    GWORKER --> LOKI',
    '    RSWORKER --> LOKI',
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
    SCHED: {
      title: 'Temporal Schedules',
      badge: 'entry', badgeText: 'scheduler',
      body: '<p>Temporal Schedules start each workflow &mdash; mostly on cron (backup, trivy scan, node cleanup, registry GC, aptly cleanup, postgres maintenance, cert acquirer, github token renewer), plus the runner scaler on a short interval (~30s). The server fires them; no trigger process is involved.</p><p>Defined as code in <code>infrastructure/terragrunt</code> (the <code>temporal-config</code> module via the <code>platacard/temporal</code> provider). Each schedule carries the workflow type, task queue, cron/interval, and a JSON <code>input</code> that deserializes into the workflow config struct.</p>'
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
      body: '<p>Temporal task queue shared by the four maintenance workflows &mdash; node cleanup, registry GC, aptly cleanup, and postgres maintenance &mdash; and their activities. Only the cleanup worker polls this queue.</p><p>Retry policy: 1s initial interval, 2.0 backoff, 1m max interval, 3 max attempts (the long-running registry GC and aptly cleanup steps run with 1 attempt).</p>'
    },
    CETQ: {
      title: 'cert-task-queue',
      badge: 'middleware', badgeText: 'task queue',
      body: '<p>Temporal task queue for the cert-acquirer workflow and activities. Only the cert-acquirer worker polls this queue.</p><p>Issuance uses few attempts with long backoff (DNS-01 propagation is slow and Let\'s Encrypt rate-limits duplicate issuance); the publish step retries quickly.</p>'
    },
    GTQ: {
      title: 'github-token-task-queue',
      badge: 'middleware', badgeText: 'task queue',
      body: '<p>Temporal task queue for the GitHub token-renewer workflow and activities. Only the github-token-renewer worker polls this queue.</p><p>3 attempts with exponential backoff; an unparseable <code>owner/repo</code> is non-retryable.</p>'
    },
    BWORKER: {
      title: 'Backup Worker',
      badge: 'handler', badgeText: 'worker',
      body: '<p>Runs three snapshot legs <b>concurrently</b> &mdash; Nomad Raft, Consul Raft, and PostgreSQL &mdash; joining before retention cleanup. The Postgres leg dumps cluster globals once then fans out a per-database <code>pg_dump</code> with bounded concurrency. Artifacts land on NFS and upload to S3.</p><p>Snapshot/globals/listing failures terminate the workflow. S3 upload failures are logged as warnings but do not block &mdash; local backups always succeed.</p><p>Quick activity timeouts: 5min / 15min. Long timeouts for per-database dumps and uploads: 30min / 60-90min.</p><p><a href="../backup-workflow/">Backup workflow diagram &rarr;</a></p>'
    },
    TWORKER: {
      title: 'Trivy Scan Worker',
      badge: 'handler', badgeText: 'worker',
      body: '<p>Discovers all running Docker images from Nomad allocations and scans them with bounded concurrency using a Trivy server.</p><p>Errors are classified: <b>transient</b> (connection refused, timeout) trigger retries; <b>permanent</b> (manifest unknown, not found) are logged and skipped.</p><p>Results saved individually to PostgreSQL to stay under the 2MB Temporal payload limit. Aggregate CVE counts logged at completion.</p><p><a href="../trivyscan-workflow/">Trivy scan workflow diagram &rarr;</a></p>'
    },
    CWORKER: {
      title: 'Cleanup Worker',
      badge: 'handler', badgeText: 'worker',
      body: '<p>Removes orphaned Nomad job data directories. Running jobs come from the central Nomad API; the per-node directory work is done over <b>SFTP</b> (list / measure / delete) &mdash; never a remote shell. Optional Docker prune runs through the Docker API. Nodes are processed sequentially.</p><p>Excludes system dirs (alloc, plugins, tmp, server, client). Grace-period filter skips recently modified directories. Dry-run mode enabled by default.</p><p>This worker also hosts the <b>registry GC</b> and <b>aptly cleanup</b> sagas (scale a job offline, run a one-shot container via the Docker API tunneled over SSH, always scale back via deferred compensation) and a <b>postgres-maintenance</b> workflow, all on the same task queue. The two sagas share the generic find / scale / wait / measure activities.</p><p><a href="../nodecleanup-workflow/">Node cleanup workflow diagram &rarr;</a><br><a href="../registry-gc-workflow/">Registry GC workflow diagram &rarr;</a><br><a href="../aptly-cleanup-workflow/">Aptly cleanup workflow diagram &rarr;</a><br><a href="../postgres-maintenance-workflow/">Postgres maintenance workflow diagram &rarr;</a></p>'
    },
    CEWORKER: {
      title: 'Cert Acquirer Worker',
      badge: 'handler', badgeText: 'worker',
      body: '<p>Issues the <code>*.munchbox.cc</code> wildcard certificate via ACME DNS-01 (Cloudflare, go-acme/lego) and publishes it to Vault for Traefik to read.</p><p>Issuance and publish are separate activities: the issued cert+key are written to a Vault staging path so a publish retry never re-runs ACME (Let\'s Encrypt rate limits), and the private key never transits workflow history.</p><p>Self-authenticates with its Nomad Workload Identity and pulls the Cloudflare token through Vault &mdash; no static secrets in the job.</p><p><a href="../cert-acquirer-workflow/">Cert acquirer workflow diagram &rarr;</a></p>'
    },
    RSTQ: {
      title: 'ci-runner-scaler-task-queue',
      badge: 'middleware', badgeText: 'task queue',
      body: '<p>Temporal task queue for the runner-scaler workflows (<code>PollAndDispatch</code> parent + <code>HandleQueuedJob</code> children) and their activities. Only the runner-scaler worker polls this queue.</p><p>Fired on a short interval (~30s) rather than cron. Child workflows are keyed <code>runner-&lt;repo&gt;-&lt;job_id&gt;</code> with a reject-duplicate ID policy, so Temporal itself dedups one runner per job.</p>'
    },
    RSWORKER: {
      title: 'Runner Scaler Worker',
      badge: 'handler', badgeText: 'worker',
      body: '<p>Scales self-hosted CI runners on demand (zero idle). Each tick reads the watched repos and runner profiles from Consul, lists each repo\'s queued <code>self-hosted</code> Actions jobs on GitHub, and starts one child per job &mdash; the reject-duplicate child ID is the entire state store (no external DB).</p><p>Each child mints a runner registration token and dispatches one ephemeral Nomad <code>ci-runner</code> carrying it (token minted inside the activity, so it never enters workflow history; dispatch runs NoRetry so it can\'t double up). A backstop timer reaps a runner that never picked its job up.</p><p>Self-authenticates with its Nomad Workload Identity and pulls the GitHub App key through Vault (reusing the token-renewer App, which also grants Administration + Actions). <a href="../runnerscaler-workflow/">Runner scaler workflow diagram &rarr;</a></p>'
    },
    GWORKER: {
      title: 'GitHub Token Renewer Worker',
      badge: 'handler', badgeText: 'worker',
      body: '<p>Keeps each managed repo\'s CI/release token secret continuously valid by minting a fresh GitHub App installation token every run &mdash; replacing hand-rotated Personal Access Tokens (which can\'t be API-minted).</p><p>Reads the repo list from Consul KV, then per repo mints a repo-scoped token (<code>contents</code> + <code>pull-requests: write</code>), seals it with a NaCl box against the repo\'s Actions public key, and writes it to the <code>RELEASE_PAT</code> secret via a separate <code>secrets: write</code> token &mdash; native <code>go-github</code>, no <code>gh</code> CLI.</p><p>Self-authenticates with its Nomad Workload Identity and pulls the App private key through Vault &mdash; no static secrets in the job.</p><p><a href="../ghtokenrenewer-workflow/">GitHub token renewer workflow diagram &rarr;</a></p>'
    },
    NOMAD_API: {
      title: 'Nomad API',
      badge: 'data', badgeText: 'external service',
      body: '<p>HashiCorp Nomad HTTP API. Used by three workers via the native Go client (no CLI):</p><p><b>Backup:</b> <code>Operator().Snapshot()</code> for the Raft snapshot, streamed to disk.<br><b>Trivy:</b> allocation list to discover running Docker images.<br><b>Cleanup:</b> node list, and each node\'s running allocations (<code>Nodes().Allocations</code>) to decide which data dirs are orphaned &mdash; plus job scaling for the registry/aptly sagas.</p><p>All calls go through the <code>shared.Nomad</code> service (OTel-instrumented HTTP transport); each worker consumes a narrow interface over it. Produces <code>peer.service: nomad</code> service graph edges.</p>'
    },
    CONSUL_API: {
      title: 'Consul API',
      badge: 'data', badgeText: 'external service',
      body: '<p>HashiCorp Consul HTTP API. The backup worker captures Raft state (which includes Vault data) via the native Go client <code>Snapshot().Save()</code>, streamed to disk &mdash; no <code>consul</code> CLI.</p><p>Snapshot stored locally on NFS mount, then uploaded to S3.</p>'
    },
    PG: {
      title: 'PostgreSQL',
      badge: 'data', badgeText: 'database',
      body: '<p>Used by three workers:</p><p><b>Backup:</b> dumps cluster globals once, lists databases via <code>database/sql</code>, then fans out a per-database <code>pg_dump</code> with bounded concurrency &mdash; each subprocess gzipped in-process (no <code>| gzip</code> shell pipe). Long timeout (30min/60min) for large databases.</p><p><b>Trivy:</b> Stores scan results transactionally &mdash; scan record + individual vulnerabilities. Deduplicates by vuln ID, truncates long descriptions to 1000 chars. Connection wrapped with <code>otelsql</code> for trace propagation.</p><p><b>Cleanup (postgres maintenance):</b> lists databases and runs an online <code>VACUUM (ANALYZE)</code> on each with bounded concurrency through the same shared client.</p>'
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
      title: 'SSH / SFTP + Docker tunnel',
      badge: 'data', badgeText: 'external service',
      body: '<p>The cleanup worker\'s SSH connection to each Nomad client node (and the registry/aptly hosts). Certificate auth with host-CA verification; the worker connects as <code>root</code> everywhere &mdash; the Vault SSH CA issues a root principal the Oracle hosts accept too, so there is no per-node user or sudo handling.</p><p>The connection carries only native operations: <b>SFTP</b> for file work (list/measure/delete data dirs) and a tunneled <b>Docker API</b> for the registry-GC / aptly one-shot containers and the docker prune. No remote shell commands or generated scripts.</p>'
    },
    VAULT: {
      title: 'Vault',
      badge: 'data', badgeText: 'external service',
      body: '<p>HashiCorp Vault. The cert worker authenticates with its Nomad Workload Identity token, reads the Cloudflare DNS token, persists the ACME account, and writes the issued wildcard to the path Traefik reads.</p><p>The newer self-authenticating workers source every other credential through this client, so no static service tokens are templated into the job.</p>'
    },
    ACME: {
      title: 'ACME (Lets Encrypt)',
      badge: 'data', badgeText: 'external service',
      body: '<p>The ACME directory (Let\'s Encrypt production by default; point at staging for testing). The cert worker runs the DNS-01 challenge via go-acme/lego. Rate-limit responses are classified non-retryable for the run so it stops hammering the endpoint.</p>'
    },
    CF: {
      title: 'Cloudflare DNS',
      badge: 'data', badgeText: 'external service',
      body: '<p>Cloudflare DNS API. The cert worker provisions the <code>_acme-challenge</code> TXT record for the DNS-01 challenge using a scoped API token read from Vault.</p>'
    },
    GITHUB: {
      title: 'GitHub App API',
      badge: 'data', badgeText: 'external service',
      body: '<p>GitHub REST API, reached as a GitHub App (one app installed across all managed repos) via the native <code>go-github</code> client &mdash; no <code>gh</code> CLI.</p><p>The token renewer mints short-lived, repo-scoped installation tokens and writes repository Actions secrets (sealed against the repo\'s public key). The runner scaler reuses the same App to discover queued self-hosted jobs and mint runner registration tokens. An App is the only way to mint PR-capable tokens programmatically. Produces <code>peer.service: github</code> service-graph edges.</p>'
    },
    TEMPO: {
      title: 'Tempo (Tracing)',
      badge: 'observability', badgeText: 'observability',
      body: '<p>Distributed tracing backend. All workers export spans via OTLP gRPC (default: <code>tempo.service.consul:4317</code>).</p><p>Client spans with <code>peer.service</code> attributes produce service graph edges in Grafana for every external call.</p>'
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
| <span style="color:#34d399">**Emerald**</span> | Entry points (schedules) |
| <span style="color:#14b8a6">**Teal**</span> | Middleware (Temporal, task queues) |
| <span style="color:#a78bfa">**Purple**</span> | Workers |
| <span style="color:#3fb950">**Green**</span> | External data stores & services |
| <span style="color:#f85149">**Red**</span> | Observability |
