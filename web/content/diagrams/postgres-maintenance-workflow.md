---
title: "Postgres Maintenance Workflow"
linkTitle: "Postgres Maintenance"
weight: 60
---

Online PostgreSQL maintenance. The workflow enumerates every database in the cluster and runs `VACUUM (ANALYZE)` on each with bounded concurrency to reclaim bloat and refresh planner statistics. A per-database failure is recorded and the run continues; the workflow returns an error only after every database has been attempted. **Hover over any step** for implementation details.

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
  .ac-badge-error { background: #da363322; color: #f85149; border: 1px solid #f8514955; }
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
    '    START([PostgresMaintenance\\nWorkflow]):::workflow --> DEFAULTS[Apply Config\\nDefaults]:::workflow',
    '    DEFAULTS --> LIST[List\\nDatabases]:::activity',
    '    LIST --> EMPTY{Databases\\nFound?}:::decision',
    '    EMPTY -->|none| DONE',
    '    EMPTY -->|yes| ACQUIRE[Acquire Slot\\nsem = Concurrency]:::workflow',
    '    ACQUIRE --> VACUUM[VACUUM ANALYZE\\nper database]:::activity',
    '    VACUUM --> GATE{VACUUM\\nOK?}:::decision',
    '    GATE -->|ok| RECORD[Record\\nOutcome]:::workflow',
    '    GATE -->|failed| TRACK[Record Error\\ncontinue run]:::error',
    '    RECORD --> MORE{More\\nDatabases?}:::decision',
    '    TRACK --> MORE',
    '    MORE -->|yes, slot frees| ACQUIRE',
    '    MORE -->|no| JOIN[Join Fan-Out\\nWaitGroup]:::workflow',
    '    JOIN --> ANYFAIL{Any\\nFailed?}:::decision',
    '    ANYFAIL -->|yes| FAIL[Return Error\\nwith failures]:::error',
    '    ANYFAIL -->|no| DONE([Workflow\\nComplete]):::workflow',
    '    FAIL --> DONE',
    '',
    '    classDef workflow fill:#7c3aed,stroke:#a78bfa,color:#fff,font-weight:bold',
    '    classDef activity fill:#059669,stroke:#34d399,color:#fff',
    '    classDef decision fill:#0d9488,stroke:#14b8a6,color:#fff',
    '    classDef error fill:#da3633,stroke:#f85149,color:#fff'
  ].join('\n');

  mermaid.initialize({
    startOnLoad: false,
    theme: 'dark',
    flowchart: { nodeSpacing: 14, rankSpacing: 22, curve: 'basis', padding: 5, diagramPadding: 8, useMaxWidth: true }
  });

  mermaid.render('postgres-maintenance-mermaid-svg', diagramSrc).then(function(result) {
    document.getElementById('ac-diagram').innerHTML = result.svg;
    wireUpInteractivity();
  });

  var nodeInfo = {
    START: {
      title: 'PostgresMaintenance Workflow',
      badge: 'workflow', badgeText: 'workflow entry',
      body: '<p>Reclaims bloat and refreshes planner statistics across every database in the cluster.</p><p>Receives <code>PostgresMaintenanceConfig</code> with <code>Concurrency</code> from the schedule input. Runs on the cleanup worker / <code>cleanup-task-queue</code>.</p>'
    },
    DEFAULTS: {
      title: 'Apply Config Defaults',
      badge: 'workflow', badgeText: 'workflow logic',
      body: '<p>Fills unset fields so every activity sees a deterministic config across replay:</p><p><code>Concurrency</code>: <code>2</code> &mdash; a zero would size the semaphore at 0 and deadlock, so the default is forced positive.</p>'
    },
    LIST: {
      title: 'List Databases',
      badge: 'activity', badgeText: 'activity',
      body: '<p><code>ListPostgresDatabases</code> queries the primary for the non-template databases that accept connections, through the shared instrumented Postgres client (a client span to <code>postgres-primary</code> shows up in the service graph).</p>'
    },
    EMPTY: {
      title: 'Databases Found?',
      badge: 'decision', badgeText: 'empty check',
      body: '<p>If the cluster reports no user databases, the fan-out is skipped and the workflow completes successfully with an empty result.</p>'
    },
    ACQUIRE: {
      title: 'Acquire Slot (semaphore)',
      badge: 'workflow', badgeText: 'bounded concurrency',
      body: '<p>The fan-out is gated by a buffered channel sized to <code>Concurrency</code>. Each database\'s goroutine sends into the channel to acquire a slot before vacuuming and receives from it on completion, so at most <code>Concurrency</code> vacuums run at once and the maintenance burst never overwhelms the primary.</p>'
    },
    VACUUM: {
      title: 'VACUUM ANALYZE (per database)',
      badge: 'activity', badgeText: 'activity, per-db',
      body: '<p><code>VacuumAnalyzeDatabase</code> runs <code>VACUUM (ANALYZE)</code> against one database &mdash; online and lock-light (no <code>FULL</code>). One connection per database, since VACUUM operates on the database it is connected to. 3 attempts with exponential backoff.</p>'
    },
    GATE: {
      title: 'VACUUM OK?',
      badge: 'decision', badgeText: 'per-db outcome',
      body: '<p>A single database\'s failure does not abort the run. The outcome is recorded and the other databases still get vacuumed.</p>'
    },
    RECORD: {
      title: 'Record Outcome',
      badge: 'workflow', badgeText: 'workflow logic',
      body: '<p>Appends a <code>DatabaseMaintenance</code> entry (database name, no error) to the result slice at this database\'s index.</p>'
    },
    TRACK: {
      title: 'Record Error (continue)',
      badge: 'error', badgeText: 'non-fatal',
      body: '<p>Stores the database\'s error in its <code>DatabaseMaintenance</code> entry and in the per-index error slice, then keeps going. The run only fails <b>after</b> every database has been attempted.</p>'
    },
    MORE: {
      title: 'More Databases?',
      badge: 'decision', badgeText: 'fan-out loop',
      body: '<p>Each completed vacuum frees a semaphore slot for the next queued database, until all have run.</p>'
    },
    JOIN: {
      title: 'Join Fan-Out (WaitGroup)',
      badge: 'workflow', badgeText: 'barrier',
      body: '<p>A <code>workflow.WaitGroup</code> blocks until every per-database goroutine has finished, so the result is complete before the workflow evaluates success.</p>'
    },
    ANYFAIL: {
      title: 'Any Failed?',
      badge: 'decision', badgeText: 'aggregate',
      body: '<p>The per-index errors are joined with <code>errors.Join</code>. If any database failed, the workflow reports overall failure; otherwise it succeeds.</p>'
    },
    FAIL: {
      title: 'Return Error (with failures)',
      badge: 'error', badgeText: 'aggregate error',
      body: '<p>Sets <code>Success=false</code> and returns the joined error naming each failed database, alongside the full per-database result for diagnostics.</p>'
    },
    DONE: {
      title: 'Workflow Complete',
      badge: 'workflow', badgeText: 'result',
      body: '<p>Returns <code>PostgresMaintenanceResult</code> with a per-database outcome list and an overall <code>Success</code> flag.</p>'
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
| <span style="color:#14b8a6">**Teal**</span> | Decision points |
| <span style="color:#f85149">**Red**</span> | Error handling (non-fatal per database) |
