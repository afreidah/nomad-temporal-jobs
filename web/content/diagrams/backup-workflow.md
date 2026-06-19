---
title: "Backup Workflow"
linkTitle: "Backup"
weight: 10
---

Concurrent backup orchestration: three independent legs (Nomad, Consul, PostgreSQL) run in parallel, joining before retention cleanup. The PostgreSQL leg fans per-database dumps out with bounded concurrency. **Hover over any step** for implementation details.

<style>
  #ac-diagram { margin: 1rem 0; }

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
  .ac-badge-workflow { background: #7c3aed22; color: #a78bfa; border: 1px solid #a78bfa55; }
  .ac-badge-activity { background: #05966922; color: #34d399; border: 1px solid #34d39955; }
  .ac-badge-decision { background: #0d948822; color: #14b8a6; border: 1px solid #14b8a655; }
  #ac-tooltip p { font-size: 0.75rem; line-height: 1.4; color: #c9d1d9; margin-bottom: 0.35rem; }
  #ac-tooltip code { background: #21262d; padding: 1px 4px; border-radius: 3px; font-size: 0.7rem; color: #34d399; }

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
    '    START([Backup\\nWorkflow]):::workflow --> DEFAULTS[Apply Config\\nDefaults]:::workflow',
    '    DEFAULTS --> NOMAD_SNAP[Take Nomad\\nSnapshot]:::activity',
    '    DEFAULTS --> CONSUL_SNAP[Take Consul\\nSnapshot]:::activity',
    '    DEFAULTS --> PG_GLOBALS[Dump Postgres\\nGlobals]:::activity',
    '    NOMAD_SNAP --> NOMAD_S3{Upload to\\nS3?}:::decision',
    '    NOMAD_S3 -->|non-fatal| JOIN',
    '    CONSUL_SNAP --> CONSUL_S3{Upload to\\nS3?}:::decision',
    '    CONSUL_S3 -->|non-fatal| JOIN',
    '    PG_GLOBALS --> PG_GLOBALS_S3{Upload to\\nS3?}:::decision',
    '    PG_GLOBALS_S3 -->|non-fatal| PG_LIST[List\\nDatabases]:::activity',
    '    PG_LIST --> PG_DUMP[Dump Each DB\\nbounded concurrency]:::activity',
    '    PG_DUMP --> PG_DB_S3{Upload Each\\nto S3?}:::decision',
    '    PG_DB_S3 -->|non-fatal| JOIN',
    '    JOIN[Join Legs]:::workflow --> LOCAL_CLEAN[Cleanup Old\\nLocal Backups]:::activity',
    '    LOCAL_CLEAN --> S3_CLEAN[Cleanup Old\\nS3 Backups]:::activity',
    '    S3_CLEAN --> DONE([Workflow\\nComplete]):::workflow',
    '',
    '    classDef workflow fill:#7c3aed,stroke:#a78bfa,color:#fff,font-weight:bold',
    '    classDef activity fill:#059669,stroke:#34d399,color:#fff',
    '    classDef decision fill:#0d9488,stroke:#14b8a6,color:#fff'
  ].join('\n');

  mermaid.initialize({
    startOnLoad: false,
    theme: 'dark',
    flowchart: { nodeSpacing: 14, rankSpacing: 22, curve: 'basis', padding: 5, diagramPadding: 8, useMaxWidth: true }
  });

  mermaid.render('backup-mermaid-svg', diagramSrc).then(function(result) {
    document.getElementById('ac-diagram').innerHTML = result.svg;
    wireUpInteractivity();
  });

  var nodeInfo = {
    START: {
      title: 'Backup Workflow',
      badge: 'workflow', badgeText: 'workflow entry',
      body: '<p>Pure orchestration workflow &mdash; all I/O happens in activities. Receives <code>BackupConfig</code> with <code>LocalDays</code>, <code>S3Days</code>, and <code>DumpConcurrency</code> from the schedule input.</p><p>Returns <code>BackupResult</code> with snapshot paths, the per-database list, S3 keys, timestamp, and success status.</p>'
    },
    DEFAULTS: {
      title: 'Apply Config Defaults',
      badge: 'workflow', badgeText: 'workflow logic',
      body: '<p>Applies defaults for any unset config value:</p><p><code>LocalDays</code>: 7 (local NFS backups)<br><code>S3Days</code>: 30 (offsite S3 backups)<br><code>DumpConcurrency</code>: 4 (parallel per-database dumps)</p><p>Configurable via <code>LOCAL_RETENTION_DAYS</code>, <code>S3_RETENTION_DAYS</code>, and <code>PG_DUMP_CONCURRENCY</code> on the trigger job. The three legs below then fan out and run concurrently.</p>'
    },
    NOMAD_SNAP: {
      title: 'Take Nomad Snapshot',
      badge: 'activity', badgeText: 'activity',
      body: '<p>Captures Nomad Raft state via the <b>native Go API</b> (<code>Operator().Snapshot()</code> through the shared instrumented client), streamed straight to disk &mdash; no <code>nomad</code> CLI is bundled.</p><p>Output stored on NFS mount at the configured backup directory with a timestamped filename.</p><p>Quick timeout: 5 min start-to-close, 15 min schedule-to-close. Failure terminates the workflow.</p>'
    },
    NOMAD_S3: {
      title: 'Upload Nomad Snapshot to S3',
      badge: 'decision', badgeText: 'non-fatal',
      body: '<p>Uploads the Nomad snapshot to S3 under <code>backups/nomad/</code> prefix.</p><p><b>Non-fatal:</b> if the upload fails, a warning is logged and the workflow continues. Local backup is preserved regardless.</p><p>Smart quota handling: on <code>QuotaExceeded</code>, evicts the oldest object and retries up to 3 times.</p>'
    },
    CONSUL_SNAP: {
      title: 'Take Consul Snapshot',
      badge: 'activity', badgeText: 'activity',
      body: '<p>Captures Consul Raft state (includes Vault data) via the <b>native Go API</b> (<code>Snapshot().Save()</code>), streamed straight to disk &mdash; no <code>consul</code> CLI is bundled.</p><p>Output stored on NFS mount with a timestamped filename. Quick timeout: 5 min start-to-close, 15 min schedule-to-close.</p><p>Failure terminates the workflow.</p>'
    },
    CONSUL_S3: {
      title: 'Upload Consul Snapshot to S3',
      badge: 'decision', badgeText: 'non-fatal',
      body: '<p>Uploads the Consul snapshot to S3 under <code>backups/consul/</code> prefix.</p><p><b>Non-fatal:</b> same behavior as Nomad S3 upload. Warning on failure, workflow continues.</p>'
    },
    PG_GLOBALS: {
      title: 'Dump Postgres Globals',
      badge: 'activity', badgeText: 'activity',
      body: '<p>Runs <code>pg_dumpall --globals-only</code> to capture cluster-wide roles, tablespaces, and grants &mdash; objects a per-database <code>pg_dump</code> omits. The subprocess output is gzipped <b>in-process</b> (Go <code>compress/gzip</code>), so there is no <code>| gzip</code> shell pipe or bash.</p><p>Quick timeout: 5 min start-to-close, 15 min schedule-to-close. Failure terminates the PostgreSQL leg.</p>'
    },
    PG_GLOBALS_S3: {
      title: 'Upload Globals to S3',
      badge: 'decision', badgeText: 'non-fatal',
      body: '<p>Uploads the globals dump to S3 under <code>backups/postgres/</code> prefix.</p><p><b>Non-fatal:</b> warning on failure, the leg continues to database enumeration.</p>'
    },
    PG_LIST: {
      title: 'List Databases',
      badge: 'activity', badgeText: 'activity',
      body: '<p>Enumerates the cluster\'s user databases by querying the catalog directly through the shared instrumented client (<code>database/sql</code>) &mdash; not the <code>psql</code> CLI. Quick timeout: 5 min start-to-close, 15 min schedule-to-close.</p><p>Failure terminates the PostgreSQL leg.</p>'
    },
    PG_DUMP: {
      title: 'Dump Each Database',
      badge: 'activity', badgeText: 'fan-out',
      body: '<p>Dumps each database to its own file via a <code>pg_dump</code> subprocess whose output is gzipped <b>in-process</b> (no shell pipe), fanning out with bounded concurrency (<code>DumpConcurrency</code>, default 4).</p><p>Long timeout: 30 min start-to-close, 60 min schedule-to-close, heartbeating every 2 min &mdash; large databases can run long.</p><p>A dump failure fails the leg <i>after</i> every database has been attempted.</p>'
    },
    PG_DB_S3: {
      title: 'Upload Each Database to S3',
      badge: 'decision', badgeText: 'non-fatal',
      body: '<p>Uploads each database dump to S3 under <code>backups/postgres/</code> prefix as part of the same bounded-concurrency fan-out.</p><p><b>Non-fatal:</b> a failed upload is logged; the local dump is preserved and the leg continues.</p>'
    },
    JOIN: {
      title: 'Join Legs',
      badge: 'workflow', badgeText: 'workflow logic',
      body: '<p>Waits for all three legs to finish. Any leg returning a fatal error terminates the workflow before cleanup.</p><p>S3 upload failures are not fatal and do not block the join.</p>'
    },
    LOCAL_CLEAN: {
      title: 'Cleanup Old Local Backups',
      badge: 'activity', badgeText: 'activity',
      body: '<p>Removes local backup files older than <code>LocalDays</code> (default: 7) from the NFS mount.</p><p>Scans the backup directory, checks file modification time, and deletes expired files. Non-fatal: warning on failure.</p>'
    },
    S3_CLEAN: {
      title: 'Cleanup Old S3 Backups',
      badge: 'activity', badgeText: 'activity',
      body: '<p>Lists and deletes S3 objects older than <code>S3Days</code> (default: 30) from all backup prefixes.</p><p>Uses the S3 ListObjects API with the backup prefix, checks each object\'s LastModified timestamp. Non-fatal: warning on failure.</p>'
    },
    DONE: {
      title: 'Workflow Complete',
      badge: 'workflow', badgeText: 'result',
      body: '<p>Returns <code>BackupResult</code> containing:</p><p>Nomad and Consul snapshot paths, the Postgres globals path, the per-database list (each with local path and S3 key), S3 keys for successful uploads, timestamp, and success boolean.</p>'
    }
  };

  var tooltip = document.getElementById('ac-tooltip');
  var mouseX = 0, mouseY = 0;
  var pinned = false;
  document.addEventListener('mousemove', function(e) {
    mouseX = e.clientX; mouseY = e.clientY;
    if (tooltip.style.display === 'block' && !pinned) positionTooltip();
  });

  function positionTooltip() {
    var pad = 12;
    var x = mouseX + pad, y = mouseY + pad;
    if (x + tooltip.offsetWidth > window.innerWidth - pad) x = mouseX - tooltip.offsetWidth - pad;
    if (y + tooltip.offsetHeight > window.innerHeight - pad) y = mouseY - tooltip.offsetHeight - pad;
    tooltip.style.left = x + 'px'; tooltip.style.top = y + 'px';
  }

  function showInfo(id) {
    var info = nodeInfo[id];
    if (!info) { tooltip.style.display = 'none'; pinned = false; return; }
    tooltip.innerHTML = '<h3>' + info.title + '</h3><span class="ac-badge ac-badge-' + info.badge + '">' + info.badgeText + '</span>' + info.body;
    pinned = false; tooltip.style.display = 'block'; positionTooltip();
    if (tooltip.querySelector('a')) pinned = true;
  }

  var hideTimer = null, hoveringTooltip = false, hoveringNode = false;
  tooltip.addEventListener('mouseenter', function() { hoveringTooltip = true; clearTimeout(hideTimer); });
  tooltip.addEventListener('mouseleave', function() {
    hoveringTooltip = false;
    hideTimer = setTimeout(function() { if (!hoveringNode && !hoveringTooltip) clearInfo(); }, 100);
  });

  function clearInfo() {
    tooltip.style.display = 'none'; pinned = false;
    var svg = document.querySelector('#ac-diagram svg');
    if (svg) { svg.classList.remove('highlighting'); svg.querySelectorAll('.highlight').forEach(function(el) { el.classList.remove('highlight'); }); }
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
        hoveringNode = true; clearTimeout(hideTimer);
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
        hideTimer = setTimeout(function() { if (!hoveringNode && !hoveringTooltip) clearInfo(); }, 100);
      });
    });
  }
})();
</script>

## Legend

| Color | Meaning |
|-------|---------|
| <span style="color:#a78bfa">**Purple**</span> | Workflow logic |
| <span style="color:#34d399">**Emerald**</span> | Activities (I/O operations) |
| <span style="color:#14b8a6">**Teal**</span> | Decision points (non-fatal S3 uploads) |
