---
title: "Trivy Scan Workflow"
linkTitle: "Trivy Scan"
weight: 20
---

Parallel image scanning orchestration showing discovery, batched scans, error classification, and result persistence. **Hover over any step** for implementation details.

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

<script src="https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.min.js"></script>
<script>
(function() {
  var diagramSrc = [
    'flowchart TD',
    '    START([Scan\\nWorkflow]):::workflow --> DISCOVER[Get Running\\nImages]:::activity',
    '    DISCOVER --> EMPTY{Images\\nFound?}:::decision',
    '    EMPTY -->|none| DONE',
    '    EMPTY -->|yes| BATCH[Create Batches\\nof 10]:::workflow',
    '    BATCH --> LOOP[Process\\nNext Batch]:::workflow',
    '    LOOP --> PARALLEL[Scan Images\\nin Parallel]:::activity',
    '    PARALLEL --> COLLECT[Collect\\nResults]:::workflow',
    '    COLLECT --> CLASSIFY{Error\\nType?}:::decision',
    '    CLASSIFY -->|success| SAVE[Save Scan\\nResult]:::activity',
    '    CLASSIFY -->|transient| RETRY[Temporal\\nRetries]:::error',
    '    CLASSIFY -->|permanent| SKIP[Log & Skip\\nImage]:::error',
    '    RETRY --> SAVE',
    '    SKIP --> MORE',
    '    SAVE --> MORE{More\\nBatches?}:::decision',
    '    MORE -->|yes| LOOP',
    '    MORE -->|no| SUMMARY[Log Aggregate\\nCVE Counts]:::workflow',
    '    SUMMARY --> DONE([Workflow\\nComplete]):::workflow',
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

  mermaid.render('trivy-mermaid-svg', diagramSrc).then(function(result) {
    document.getElementById('ac-diagram').innerHTML = result.svg;
    wireUpInteractivity();
  });

  var nodeInfo = {
    START: {
      title: 'Scan Workflow',
      badge: 'workflow', badgeText: 'workflow entry',
      body: '<p>Orchestrates parallel vulnerability scanning of all running container images. No input parameters &mdash; discovers images dynamically.</p><p>Returns aggregate vulnerability counts (critical, high, medium, low) and per-image scan status.</p>'
    },
    DISCOVER: {
      title: 'Get Running Images',
      badge: 'activity', badgeText: 'activity',
      body: '<p>Queries the Nomad API (<code>/v1/allocations</code>) to discover all running allocations, then extracts unique Docker image names from task configurations.</p><p>Quick timeout: 5 min start-to-close, 15 min schedule-to-close.</p>'
    },
    EMPTY: {
      title: 'Images Found?',
      badge: 'decision', badgeText: 'check',
      body: '<p>If no running images are discovered, the workflow completes immediately with zero counts. This can happen if Nomad has no running allocations.</p>'
    },
    BATCH: {
      title: 'Create Batches of 10',
      badge: 'workflow', badgeText: 'workflow logic',
      body: '<p>Splits the discovered image list into batches of 10 for parallel processing. Batch size balances parallelism against resource pressure on the Trivy server.</p>'
    },
    LOOP: {
      title: 'Process Next Batch',
      badge: 'workflow', badgeText: 'batch loop',
      body: '<p>Iterates through each batch sequentially. Within each batch, images are scanned in parallel using Temporal\'s async activity pattern.</p>'
    },
    PARALLEL: {
      title: 'Scan Images in Parallel',
      badge: 'activity', badgeText: 'activity',
      body: '<p>Launches parallel <code>ScanImage</code> activities for each image in the current batch. Each scan runs <code>trivy image --server --format json --timeout 10m</code>.</p><p>Scan timeout: 30 min start-to-close, 60 min schedule-to-close per image.</p><p>Returns <code>ScanResult</code> with vulnerability counts by severity and the raw vulnerability list.</p>'
    },
    COLLECT: {
      title: 'Collect Results',
      badge: 'workflow', badgeText: 'workflow logic',
      body: '<p>Gathers results from all parallel scan activities in the batch. Each result is processed individually for error classification and persistence.</p>'
    },
    CLASSIFY: {
      title: 'Error Classification',
      badge: 'decision', badgeText: 'error handling',
      body: '<p>Classifies scan errors into two categories:</p><p><b>Transient:</b> connection refused, timeout, temporary network issues &rarr; eligible for Temporal retry.<br><b>Permanent:</b> manifest unknown, image not found, unsupported media type &rarr; logged and skipped.</p><p>This prevents permanent errors from exhausting retry budgets while ensuring transient failures are retried.</p>'
    },
    SAVE: {
      title: 'Save Scan Result',
      badge: 'activity', badgeText: 'activity',
      body: '<p>Stores each scan result individually in PostgreSQL (not batched) to stay under Temporal\'s 2MB payload size limit.</p><p>Inserts a scan record and individual vulnerability rows transactionally. Deduplicates by vulnerability ID. Long descriptions truncated to 1000 characters.</p><p>Quick timeout: 5 min start-to-close, 15 min schedule-to-close.</p>'
    },
    RETRY: {
      title: 'Temporal Retries',
      badge: 'error', badgeText: 'retry',
      body: '<p>Transient scan errors trigger Temporal\'s built-in retry mechanism:</p><p>Initial interval: 1s<br>Backoff coefficient: 2.0<br>Max interval: 1 min<br>Max attempts: 3</p><p>After all retries are exhausted, the error is treated as a failure and the image is skipped.</p>'
    },
    SKIP: {
      title: 'Log & Skip Image',
      badge: 'error', badgeText: 'skip',
      body: '<p>Permanent errors (manifest unknown, image not found) are logged with the image name and error details, then the workflow moves on to the next image.</p><p>This prevents a single bad image reference from blocking the entire scan run.</p>'
    },
    MORE: {
      title: 'More Batches?',
      badge: 'decision', badgeText: 'loop check',
      body: '<p>Checks if there are remaining batches to process. If yes, loops back to scan the next batch. If no, proceeds to summary.</p>'
    },
    SUMMARY: {
      title: 'Log Aggregate CVE Counts',
      badge: 'workflow', badgeText: 'summary',
      body: '<p>Aggregates vulnerability counts across all scanned images and logs totals by severity level.</p><p>Emits a warning if any critical or high severity CVEs were found. The counts are returned as part of the workflow result for the trigger binary to log.</p>'
    },
    DONE: {
      title: 'Workflow Complete',
      badge: 'workflow', badgeText: 'result',
      body: '<p>Returns the aggregate scan results. The trigger binary logs the outcome and exits.</p>'
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
