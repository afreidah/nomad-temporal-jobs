---
title: "Backup Workflow"
linkTitle: "Backup"
weight: 10
---

Sequential backup orchestration showing the snapshot, upload, and retention cleanup flow. **Hover over any step** for implementation details.

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

<script src="https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.min.js"></script>
<script>
(function() {
  var diagramSrc = [
    'flowchart TD',
    '    START([Backup\\nWorkflow]):::workflow --> DEFAULTS[Apply Retention\\nDefaults]:::workflow',
    '    DEFAULTS --> NOMAD_SNAP[Take Nomad\\nSnapshot]:::activity',
    '    NOMAD_SNAP --> NOMAD_S3{Upload to\\nS3?}:::decision',
    '    NOMAD_S3 -->|success| CONSUL_SNAP[Take Consul\\nSnapshot]:::activity',
    '    NOMAD_S3 -->|failure| WARN1[Log Warning\\nContinue]:::decision',
    '    WARN1 --> CONSUL_SNAP',
    '    CONSUL_SNAP --> CONSUL_S3{Upload to\\nS3?}:::decision',
    '    CONSUL_S3 -->|success| PG_DUMP[Take PostgreSQL\\nBackup]:::activity',
    '    CONSUL_S3 -->|failure| WARN2[Log Warning\\nContinue]:::decision',
    '    WARN2 --> PG_DUMP',
    '    PG_DUMP --> PG_S3{Upload to\\nS3?}:::decision',
    '    PG_S3 -->|success| REG_SNAP[Take Registry\\nBackup]:::activity',
    '    PG_S3 -->|failure| WARN3[Log Warning\\nContinue]:::decision',
    '    WARN3 --> REG_SNAP',
    '    REG_SNAP --> LOCAL_CLEAN[Cleanup Old\\nLocal Backups]:::activity',
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
      body: '<p>Pure orchestration workflow &mdash; all I/O happens in activities. Receives <code>RetentionConfig</code> with <code>LocalDays</code> and <code>S3Days</code> from the trigger binary.</p><p>Returns <code>BackupResult</code> with all snapshot paths, S3 keys, timestamp, and success status.</p>'
    },
    DEFAULTS: {
      title: 'Apply Retention Defaults',
      badge: 'workflow', badgeText: 'workflow logic',
      body: '<p>Applies default retention values if not provided:</p><p><code>LocalDays</code>: 7 (local NFS backups)<br><code>S3Days</code>: 30 (offsite S3 backups)</p><p>Configurable via <code>LOCAL_RETENTION_DAYS</code> and <code>S3_RETENTION_DAYS</code> environment variables on the trigger job.</p>'
    },
    NOMAD_SNAP: {
      title: 'Take Nomad Snapshot',
      badge: 'activity', badgeText: 'activity',
      body: '<p>Executes <code>nomad operator snapshot save</code> to capture Nomad Raft state.</p><p>Output stored on NFS mount at the configured backup directory with a timestamped filename.</p><p>Quick timeout: 5 min start-to-close, 15 min schedule-to-close. Failure terminates the workflow.</p>'
    },
    NOMAD_S3: {
      title: 'Upload Nomad Snapshot to S3',
      badge: 'decision', badgeText: 'non-fatal',
      body: '<p>Uploads the Nomad snapshot to S3 under <code>backups/nomad/</code> prefix.</p><p><b>Non-fatal:</b> if the upload fails, a warning is logged and the workflow continues. Local backup is preserved regardless.</p><p>Smart quota handling: on <code>QuotaExceeded</code>, evicts the oldest object and retries up to 3 times.</p>'
    },
    WARN1: {
      title: 'S3 Upload Warning',
      badge: 'decision', badgeText: 'fallback',
      body: '<p>S3 upload failed but the workflow continues. The Nomad snapshot is safe on local NFS storage.</p><p>Warning logged with the error details for operator review.</p>'
    },
    CONSUL_SNAP: {
      title: 'Take Consul Snapshot',
      badge: 'activity', badgeText: 'activity',
      body: '<p>Executes <code>consul snapshot save</code> to capture Consul Raft state.</p><p>Output stored on NFS mount with a timestamped filename. Quick timeout: 5 min start-to-close, 15 min schedule-to-close.</p><p>Failure terminates the workflow.</p>'
    },
    CONSUL_S3: {
      title: 'Upload Consul Snapshot to S3',
      badge: 'decision', badgeText: 'non-fatal',
      body: '<p>Uploads the Consul snapshot to S3 under <code>backups/consul/</code> prefix.</p><p><b>Non-fatal:</b> same behavior as Nomad S3 upload. Warning on failure, workflow continues.</p>'
    },
    WARN2: {
      title: 'S3 Upload Warning',
      badge: 'decision', badgeText: 'fallback',
      body: '<p>Consul S3 upload failed. Local snapshot preserved on NFS.</p>'
    },
    PG_DUMP: {
      title: 'Take PostgreSQL Backup',
      badge: 'activity', badgeText: 'activity',
      body: '<p>Executes <code>pg_dumpall | gzip</code> for a full compressed database backup.</p><p>Long timeout: 30 min start-to-close, 60 min schedule-to-close &mdash; PostgreSQL dumps can be large.</p><p>Output stored on NFS mount. Failure terminates the workflow.</p>'
    },
    PG_S3: {
      title: 'Upload PostgreSQL Backup to S3',
      badge: 'decision', badgeText: 'non-fatal',
      body: '<p>Uploads the compressed PostgreSQL dump to S3 under <code>backups/postgres/</code> prefix.</p><p><b>Non-fatal:</b> same pattern. Local backup preserved on failure.</p>'
    },
    WARN3: {
      title: 'S3 Upload Warning',
      badge: 'decision', badgeText: 'fallback',
      body: '<p>PostgreSQL S3 upload failed. Local dump preserved on NFS.</p>'
    },
    REG_SNAP: {
      title: 'Take Registry Backup',
      badge: 'activity', badgeText: 'activity',
      body: '<p>Creates a compressed tar archive (<code>tar -czf</code>) of the container registry data directory.</p><p>Quick timeout: 5 min start-to-close, 15 min schedule-to-close. Failure terminates the workflow.</p>'
    },
    LOCAL_CLEAN: {
      title: 'Cleanup Old Local Backups',
      badge: 'activity', badgeText: 'activity',
      body: '<p>Removes local backup files older than <code>LocalDays</code> (default: 7) from the NFS mount.</p><p>Scans the backup directory, checks file modification time, and deletes expired files.</p>'
    },
    S3_CLEAN: {
      title: 'Cleanup Old S3 Backups',
      badge: 'activity', badgeText: 'activity',
      body: '<p>Lists and deletes S3 objects older than <code>S3Days</code> (default: 30) from all backup prefixes.</p><p>Uses the S3 ListObjects API with the backup prefix, checks each object\'s LastModified timestamp.</p>'
    },
    DONE: {
      title: 'Workflow Complete',
      badge: 'workflow', badgeText: 'result',
      body: '<p>Returns <code>BackupResult</code> containing:</p><p>All snapshot file paths (Nomad, Consul, PostgreSQL, Registry), S3 keys for successful uploads, timestamp, and success boolean.</p><p>The trigger binary logs the result and exits.</p>'
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
