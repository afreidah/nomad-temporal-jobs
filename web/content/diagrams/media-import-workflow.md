---
title: "Media Import Workflow"
linkTitle: "Media Import"
weight: 100
---

Reconcile orchestration showing completed-download discovery, per-folder Sonarr-then-Radarr manual import with duplicate/downgrade skipping and multi-season-pack override, and a single Jellyfin scan. **Hover over any step** for implementation details.

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
    '    START([Reconcile<br/>Workflow]):::workflow --> LIST[List Completed<br/>Torrents]:::activity',
    '    LIST --> EMPTY{Any<br/>Complete?}:::decision',
    '    EMPTY -->|none| DONE([Workflow<br/>Complete]):::workflow',
    '    EMPTY -->|yes| LOOP[Reconcile Each<br/>Folder]:::workflow',
    '    LOOP --> SONARR[Sonarr<br/>Manual Import]:::activity',
    '    SONARR --> MATCH{Series<br/>Matched?}:::decision',
    '    MATCH -->|no| RADARR[Radarr<br/>Manual Import]:::activity',
    '    MATCH -->|yes| MORE',
    '    RADARR --> MORE{More<br/>Folders?}:::decision',
    '    MORE -->|yes| LOOP',
    '    MORE -->|no| ANY{Anything<br/>Imported?}:::decision',
    '    ANY -->|yes| REFRESH[Jellyfin<br/>Scan]:::activity',
    '    ANY -->|no| SUMMARY',
    '    REFRESH --> SUMMARY[Log Summary<br/>+ Flagged]:::workflow',
    '    SUMMARY --> DONE',
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

  mermaid.render('media-import-mermaid-svg', diagramSrc).then(function(result) {
    document.getElementById('ac-diagram').innerHTML = result.svg;
    wireUpInteractivity();
  });

  var nodeInfo = {
    START: {
      title: 'Reconcile Workflow',
      badge: 'workflow', badgeText: 'workflow entry',
      body: '<p>Imports torrents grabbed in Deluge <b>outside</b> of Sonarr/Radarr into the library so Jellyfin can see them. Input is <code>ReconcileConfig</code> (<code>concurrency</code>, <code>dry_run</code>).</p><p>Pure orchestration &mdash; all I/O happens in the activities; the workflow only fans out over the completed folders and aggregates.</p>'
    },
    LIST: {
      title: 'List Completed Torrents',
      badge: 'activity', badgeText: 'activity',
      body: '<p>Calls the Deluge WebUI JSON-RPC (<code>auth.login</code> then <code>core.get_torrents_status</code>) and returns the names of every torrent at <b>100% progress</b>.</p><p>In-progress torrents are excluded so a partially-downloaded file is never hardlinked into the library. Quick timeout: 5 min start-to-close, 15 min schedule-to-close.</p>'
    },
    EMPTY: {
      title: 'Any Complete?',
      badge: 'decision', badgeText: 'check',
      body: '<p>If Deluge reports no completed torrents, the workflow completes immediately with nothing to do.</p>'
    },
    LOOP: {
      title: 'Reconcile Each Folder',
      badge: 'workflow', badgeText: 'bounded fan-out',
      body: '<p>Fans out over the completed folders with a bounded-concurrency semaphore (<code>concurrency</code>, default 4). Each folder is reconciled independently; a per-folder failure is logged and flagged, and does not fail the run.</p>'
    },
    SONARR: {
      title: 'Sonarr Manual Import',
      badge: 'activity', badgeText: 'activity',
      body: '<p>GETs Sonarr\'s <code>/manualimport</code> decisions for the folder, then force-imports the genuinely-missing episodes via a <code>ManualImport</code> command with <code>importMode=Copy</code> (hardlink, keep seeding).</p><p><b>Skips</b> files rejected as <code>Not an upgrade</code> (already owned at equal/better quality). <b>Overrides</b> the multi-season-pack guard (<code>episode unexpected</code>) with explicit episode IDs. Sets <code>NoMatch</code> when Sonarr recognizes no series, so the workflow can try Radarr. Honors <code>dry_run</code> (counts, no POST).</p>'
    },
    MATCH: {
      title: 'Series Matched?',
      badge: 'decision', badgeText: 'routing',
      body: '<p>If Sonarr matched a series, the result is kept as-is. If not (<code>NoMatch</code>), the folder is a movie candidate and falls through to Radarr.</p>'
    },
    RADARR: {
      title: 'Radarr Manual Import',
      badge: 'activity', badgeText: 'activity',
      body: '<p>Same GET-decisions / <code>ManualImport</code>-command flow as Sonarr, keyed on the matched movie. Skips duplicates/downgrades. If Radarr also matches nothing, the folder is flagged as needing a human (unknown title or unmappable release name).</p>'
    },
    MORE: {
      title: 'More Folders?',
      badge: 'decision', badgeText: 'loop check',
      body: '<p>The bounded fan-out continues until every completed folder has been reconciled, then the workflow aggregates the per-folder results.</p>'
    },
    ANY: {
      title: 'Anything Imported?',
      badge: 'decision', badgeText: 'check',
      body: '<p>The Jellyfin scan only fires if at least one episode/movie was actually hardlinked in (and not on a <code>dry_run</code>) &mdash; no point rescanning the library when nothing changed.</p>'
    },
    REFRESH: {
      title: 'Jellyfin Scan',
      badge: 'activity', badgeText: 'activity',
      body: '<p>A single <code>POST /Library/Refresh</code> (authenticated with the Jellyfin API key) triggers one library scan for the whole run, so freshly-imported media surfaces without waiting for Jellyfin\'s scheduled scan. A refresh failure is a warning, not a workflow failure.</p>'
    },
    SUMMARY: {
      title: 'Log Summary + Flagged',
      badge: 'workflow', badgeText: 'summary',
      body: '<p>Logs totals (imported, skipped) and a <b>needs-a-human</b> list &mdash; folders with no series/movie match anywhere or release names too garbled to map. That list is the only manual work left; everything else reconciled automatically.</p>'
    },
    DONE: {
      title: 'Workflow Complete',
      badge: 'workflow', badgeText: 'result',
      body: '<p>Returns once every completed torrent has been reconciled and the scan (if any) has been requested.</p>'
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
| <span style="color:#f85149">**Red**</span> | Error handling |
