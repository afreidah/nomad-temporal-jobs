---
title: "Registry GC Workflow"
linkTitle: "Registry GC"
weight: 40
---

Saga-style Docker registry garbage collection. The registry is scaled offline, garbage-collected over SSH, and **always** scaled back online via a deferred compensation &mdash; even if GC fails, an activity times out, or the workflow is cancelled. **Hover over any step** for implementation details.

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
  .ac-badge-compensation { background: #bb800922; color: #d29922; border: 1px solid #d2992255; }
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

<script src="https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.min.js"></script>
<script>
(function() {
  var diagramSrc = [
    'flowchart TD',
    '    START([RegistryGC\\nWorkflow]):::workflow --> DEFAULTS[Apply Config\\nDefaults]:::workflow',
    '    DEFAULTS --> FIND[Find Registry\\nNode]:::activity',
    '    FIND --> MEASURE1[Measure Data Dir\\nbefore]:::activity',
    '    MEASURE1 --> SCALE0[Scale Registry\\nto 0]:::activity',
    '    SCALE0 --> SGATE{Scale-down\\nOK?}:::decision',
    '    SGATE -->|no| ABORT[Return Error\\nno compensation]:::error',
    '    SGATE -->|yes| DEFER[Register Deferred\\nScale-Back]:::compensation',
    '    DEFER --> DRAIN[Wait Allocs\\nDrained]:::activity',
    '    DRAIN --> GC[Run Garbage-\\nCollect via SSH]:::activity',
    '    GC --> MEASURE2[Measure Data Dir\\nafter]:::activity',
    '    MEASURE2 --> REPORT[Compute Bytes\\nReclaimed]:::workflow',
    '    REPORT --> COMP',
    '    DRAIN -.->|failure| COMP',
    '    GC -.->|failure / timeout / cancel| COMP',
    '    COMP[Compensation:\\nScale Back to 1]:::compensation --> WAITRUN[Wait Alloc\\nRunning]:::activity',
    '    WAITRUN --> CGATE{Scale-back\\nOK?}:::decision',
    '    CGATE -->|no| CRIT[Log CRITICAL\\nmanual recovery]:::error',
    '    CGATE -->|yes| DONE',
    '    CRIT --> DONE([Workflow\\nComplete]):::workflow',
    '',
    '    classDef workflow fill:#7c3aed,stroke:#a78bfa,color:#fff,font-weight:bold',
    '    classDef activity fill:#059669,stroke:#34d399,color:#fff',
    '    classDef decision fill:#0d9488,stroke:#14b8a6,color:#fff',
    '    classDef compensation fill:#bb8009,stroke:#d29922,color:#fff',
    '    classDef error fill:#da3633,stroke:#f85149,color:#fff'
  ].join('\n');

  mermaid.initialize({
    startOnLoad: false,
    theme: 'dark',
    flowchart: { nodeSpacing: 14, rankSpacing: 22, curve: 'basis', padding: 5, diagramPadding: 8, useMaxWidth: true }
  });

  mermaid.render('registry-gc-mermaid-svg', diagramSrc).then(function(result) {
    document.getElementById('ac-diagram').innerHTML = result.svg;
    wireUpInteractivity();
  });

  var nodeInfo = {
    START: {
      title: 'RegistryGC Workflow',
      badge: 'workflow', badgeText: 'workflow entry',
      body: '<p>Orchestrates a saga that garbage-collects the Docker registry while guaranteeing it never stays offline.</p><p>Receives <code>RegistryGCConfig</code> with <code>JobName</code>, <code>GroupName</code>, <code>RegistryDataDir</code>, <code>RegistryImage</code>, <code>DryRun</code>, and <code>DeleteUntagged</code> from the schedule input. Runs on the cleanup worker / <code>cleanup-task-queue</code>.</p>'
    },
    DEFAULTS: {
      title: 'Apply Config Defaults',
      badge: 'workflow', badgeText: 'workflow logic',
      body: '<p>Fills unset fields so every activity sees a deterministic config across replay:</p><p><code>JobName</code>: <code>registry</code><br><code>GroupName</code>: = JobName<br><code>RegistryDataDir</code>: <code>/mnt/gdrive/munchbox-data/registry</code><br><code>RegistryImage</code>: <code>registry:3</code></p>'
    },
    FIND: {
      title: 'Find Registry Node',
      badge: 'activity', badgeText: 'activity',
      body: '<p><code>FindRegistryNode</code> queries the Nomad API for the running alloc of the registry job and returns the <code>NodeInfo</code> for SSH dialing.</p><p>3 attempts with exponential backoff. A "no running alloc" condition is wrapped as a <b>non-retryable</b> error so the workflow fails fast instead of retry-storming.</p>'
    },
    MEASURE1: {
      title: 'Measure Data Dir (before)',
      badge: 'activity', badgeText: 'activity',
      body: '<p><code>MeasureRegistryDataDir</code> runs <code>du -sb</code> over SSH against the registry\'s bind-mounted storage. SSH-only because the path is host-side and the Nomad API does not expose disk usage.</p><p>The result is reported as the "before" size and used to compute bytes reclaimed.</p>'
    },
    SCALE0: {
      title: 'Scale Registry to 0',
      badge: 'activity', badgeText: 'activity',
      body: '<p><code>ScaleRegistry</code> POSTs to <code>/v1/job/{name}/scale</code> to take the registry offline so its storage is quiescent for GC.</p><p>Idempotent &mdash; Nomad accepts the call even if already at the target count. A "job not found" error is wrapped as non-retryable.</p>'
    },
    SGATE: {
      title: 'Scale-down OK?',
      badge: 'decision', badgeText: 'saga gate',
      body: '<p>The deferred scale-back compensation is registered <b>only after</b> a successful scale-down. If scale-down fails, there is nothing to compensate &mdash; the workflow returns the error without registering the defer.</p>'
    },
    ABORT: {
      title: 'Return Error (no compensation)',
      badge: 'error', badgeText: 'early exit',
      body: '<p>Scale-down failed before the registry went offline, so no compensation is needed. The workflow returns the error immediately.</p>'
    },
    DEFER: {
      title: 'Register Deferred Scale-Back',
      badge: 'compensation', badgeText: 'saga setup',
      body: '<p>A <code>defer</code> closure is registered using <code>workflow.NewDisconnectedContext</code>. This is the saga compensation: it scales the registry back to 1 and waits for it to become running.</p><p>The disconnected context ensures it fires even when the parent context is cancelled (workflow timeout, parent cancel).</p>'
    },
    DRAIN: {
      title: 'Wait Allocs Drained',
      badge: 'activity', badgeText: 'poll + heartbeat',
      body: '<p><code>WaitRegistryAllocsDrained</code> polls the Nomad API until the registry job has zero running allocations. Heartbeats every poll; bounded by the activity\'s start-to-close timeout.</p>'
    },
    GC: {
      title: 'Run Garbage-Collect via SSH',
      badge: 'activity', badgeText: 'long-running, 1 attempt',
      body: '<p><code>RunRegistryGarbageCollect</code> SSHes to the registry host and runs <code>docker run ... registry garbage-collect</code> against the bind-mounted storage, honoring <code>--dry-run</code> and <code>--delete-untagged</code>.</p><p>The long-running step heartbeats periodically. Configured with <b>MaxAttempts=1</b> &mdash; a partial GC is not retried; the deferred scale-back brings the registry back online and the failure surfaces.</p>'
    },
    MEASURE2: {
      title: 'Measure Data Dir (after)',
      badge: 'activity', badgeText: 'activity',
      body: '<p>A second <code>MeasureRegistryDataDir</code> captures the post-GC size. The workflow subtracts it from the "before" size to report bytes reclaimed (rendered in human-friendly KiB/MiB/GiB).</p>'
    },
    REPORT: {
      title: 'Compute Bytes Reclaimed',
      badge: 'workflow', badgeText: 'summary',
      body: '<p>Folds the activity results into <code>RegistryGCResult</code>: node name/address, blobs deleted, before/after sizes, and bytes reclaimed.</p>'
    },
    COMP: {
      title: 'Compensation: Scale Back to 1',
      badge: 'compensation', badgeText: 'always runs',
      body: '<p>The deferred closure scales the registry job back to 1. It runs on <b>both</b> the success and failure paths once scale-down succeeded &mdash; scaling is idempotent, so re-issuing <code>count=1</code> on the happy path is a safe no-op.</p>'
    },
    WAITRUN: {
      title: 'Wait Alloc Running',
      badge: 'activity', badgeText: 'poll + heartbeat',
      body: '<p><code>WaitRegistryAllocRunning</code> polls the Nomad API until at least one registry alloc is running again, confirming the scale-back succeeded and the registry is back online.</p>'
    },
    CGATE: {
      title: 'Scale-back OK?',
      badge: 'decision', badgeText: 'recovery check',
      body: '<p>If the scale-back or wait-running step fails, the registry may still be offline.</p>'
    },
    CRIT: {
      title: 'Log CRITICAL, manual recovery',
      badge: 'error', badgeText: 'operator alert',
      body: '<p>Logs a <code>CRITICAL</code> message that the registry could not be scaled back and joins the compensation error into the workflow result so it surfaces to the operator for manual recovery.</p>'
    },
    DONE: {
      title: 'Workflow Complete',
      badge: 'workflow', badgeText: 'result',
      body: '<p>Returns <code>RegistryGCResult</code>. On the happy path the registry is back online and storage has been reclaimed; on failure the deferred compensation has still attempted to restore the registry.</p>'
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
| <span style="color:#d29922">**Amber**</span> | Saga compensation (always runs) |
| <span style="color:#f85149">**Red**</span> | Error handling |
