---
title: "Node Cleanup Workflow"
linkTitle: "Node Cleanup"
weight: 30
---

Sequential node cleanup orchestration showing discovery, SSH-based cleanup, and safety features. **Hover over any step** for implementation details.

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
    '    START([Cleanup\\nWorkflow]):::workflow --> DEFAULTS[Apply Config\\nDefaults]:::workflow',
    '    DEFAULTS --> DISCOVER[Get All Nomad\\nClient Nodes]:::activity',
    '    DISCOVER --> EMPTY{Nodes\\nFound?}:::decision',
    '    EMPTY -->|none| DONE',
    '    EMPTY -->|yes| LOOP[Process\\nNext Node]:::workflow',
    '    LOOP --> SSH[Cleanup Node\\nvia SSH]:::activity',
    '    SSH --> SCRIPT[Execute Cleanup\\nScript]:::activity',
    '    SCRIPT --> ENUM[Enumerate Job\\nDirectories]:::activity',
    '    ENUM --> RUNNING[Check Against\\nRunning Jobs]:::activity',
    '    RUNNING --> GRACE{Older Than\\nGrace Period?}:::decision',
    '    GRACE -->|yes| DRYRUN{Dry Run\\nMode?}:::decision',
    '    GRACE -->|no| SKIP_DIR[Skip\\nDirectory]:::workflow',
    '    DRYRUN -->|dry run| LOG_ONLY[Log Would\\nDelete]:::workflow',
    '    DRYRUN -->|live| DELETE[Remove\\nDirectory]:::activity',
    '    SKIP_DIR --> MORE',
    '    LOG_ONLY --> MORE',
    '    DELETE --> MORE',
    '    MORE --> DOCKER{Docker\\nPrune?}:::decision',
    '    DOCKER -->|yes| PRUNE[Docker System\\nPrune]:::activity',
    '    DOCKER -->|no| RESULT',
    '    PRUNE --> RESULT[Parse Cleanup\\nOutput]:::workflow',
    '    RESULT --> TRACK{Node\\nFailed?}:::decision',
    '    TRACK -->|yes| FAIL[Track Failed\\nNode]:::error',
    '    TRACK -->|no| NEXT',
    '    FAIL --> NEXT{More\\nNodes?}:::decision',
    '    NEXT -->|yes| LOOP',
    '    NEXT -->|no| REPORT[Report\\nTotals]:::workflow',
    '    REPORT --> DONE([Workflow\\nComplete]):::workflow',
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

  mermaid.render('cleanup-mermaid-svg', diagramSrc).then(function(result) {
    document.getElementById('ac-diagram').innerHTML = result.svg;
    wireUpInteractivity();
  });

  var nodeInfo = {
    START: {
      title: 'Cleanup Workflow',
      badge: 'workflow', badgeText: 'workflow entry',
      body: '<p>Orchestrates sequential cleanup of orphaned data directories across all Nomad client nodes.</p><p>Receives <code>CleanupConfig</code> with <code>DataDir</code>, <code>GraceDays</code>, <code>DryRun</code>, and <code>DockerPrune</code> settings from the trigger binary.</p>'
    },
    DEFAULTS: {
      title: 'Apply Config Defaults',
      badge: 'workflow', badgeText: 'workflow logic',
      body: '<p>Applies default configuration values if not provided:</p><p><code>DataDir</code>: <code>/opt/nomad/data</code><br><code>GraceDays</code>: 7<br><code>DryRun</code>: true (safe by default)<br><code>DockerPrune</code>: false</p><p>Configurable via environment variables on the trigger job.</p>'
    },
    DISCOVER: {
      title: 'Get All Nomad Client Nodes',
      badge: 'activity', badgeText: 'activity',
      body: '<p>Queries the Nomad API to list all client nodes with <code>ready</code> status. Extracts IP address or HTTPAddr for SSH connection.</p><p>Detects Oracle nodes by name prefix &mdash; these use <code>ubuntu</code> user with <code>sudo</code> instead of direct <code>root</code> access.</p><p>Returns <code>[]NodeInfo</code> with ID, Name, Address, and IsOracle flag.</p>'
    },
    EMPTY: {
      title: 'Nodes Found?',
      badge: 'decision', badgeText: 'check',
      body: '<p>If no ready nodes are found, the workflow completes immediately. This is unlikely but handled gracefully.</p>'
    },
    LOOP: {
      title: 'Process Next Node',
      badge: 'workflow', badgeText: 'sequential loop',
      body: '<p>Nodes are processed one at a time (not in parallel) to avoid overwhelming SSH connections and to make cleanup output easier to follow.</p><p>Activity timeout: 10 min start-to-close, 30 min schedule-to-close per node.</p>'
    },
    SSH: {
      title: 'Cleanup Node via SSH',
      badge: 'activity', badgeText: 'activity',
      body: '<p>Establishes an SSH connection to the target node using certificate-based authentication.</p><p>SSH config: configurable key path, cert path, and host CA path with sensible defaults. Oracle nodes use <code>ubuntu</code> user; all others use <code>root</code>.</p>'
    },
    SCRIPT: {
      title: 'Execute Cleanup Script',
      badge: 'activity', badgeText: 'generated script',
      body: '<p>A bash script is generated dynamically by <code>buildCleanupScript()</code> and executed over the SSH session.</p><p>The script runs on the remote node and handles all directory enumeration, job checking, and deletion logic locally.</p>'
    },
    ENUM: {
      title: 'Enumerate Job Directories',
      badge: 'activity', badgeText: 'remote execution',
      body: '<p>Scans the configured data directory (default: <code>/opt/nomad/data</code>) for job data directories.</p><p>Excludes system directories: <code>alloc</code>, <code>plugins</code>, <code>tmp</code>, <code>server</code>, <code>client</code>.</p>'
    },
    RUNNING: {
      title: 'Check Against Running Jobs',
      badge: 'activity', badgeText: 'remote execution',
      body: '<p>Queries the local Nomad agent API on the node to get the list of currently running jobs. Directories matching running job names are never removed.</p>'
    },
    GRACE: {
      title: 'Grace Period Check',
      badge: 'decision', badgeText: 'safety filter',
      body: '<p>Checks each orphaned directory\'s modification time against the grace period (default: 7 days).</p><p>Recently modified directories are skipped even if orphaned &mdash; they may belong to a job that was just stopped and could be restarted.</p>'
    },
    DRYRUN: {
      title: 'Dry Run Mode?',
      badge: 'decision', badgeText: 'safety gate',
      body: '<p>Dry run is <b>enabled by default</b>. In dry-run mode, the script reports what would be deleted without actually removing anything.</p><p>Set <code>DRY_RUN=false</code> on the trigger job to enable live deletion.</p>'
    },
    SKIP_DIR: {
      title: 'Skip Directory',
      badge: 'workflow', badgeText: 'skipped',
      body: '<p>Directory is within the grace period and is skipped. Counted in the <code>Skipped</code> total.</p>'
    },
    LOG_ONLY: {
      title: 'Log Would Delete',
      badge: 'workflow', badgeText: 'dry run',
      body: '<p>In dry-run mode, logs the directory that would be deleted. No actual deletion occurs. Counted in the <code>Orphaned</code> total.</p>'
    },
    DELETE: {
      title: 'Remove Directory',
      badge: 'activity', badgeText: 'destructive',
      body: '<p>Removes the orphaned job data directory from the node. This is the only destructive operation in the workflow.</p><p>Only runs when <code>DryRun=false</code> and the directory is older than the grace period.</p>'
    },
    MORE: {
      title: 'More Directories?',
      badge: 'decision', badgeText: 'dir loop',
      body: '<p>Continues processing remaining directories on the current node.</p>'
    },
    DOCKER: {
      title: 'Docker Prune?',
      badge: 'decision', badgeText: 'optional',
      body: '<p>If <code>DockerPrune=true</code>, runs Docker system prune after directory cleanup. Disabled by default.</p>'
    },
    PRUNE: {
      title: 'Docker System Prune',
      badge: 'activity', badgeText: 'optional activity',
      body: '<p>Executes <code>docker system prune -af</code> on the node to remove unused images, containers, and build cache.</p><p>Reports the amount of disk space freed in the <code>DockerSpaceFreed</code> field.</p>'
    },
    RESULT: {
      title: 'Parse Cleanup Output',
      badge: 'workflow', badgeText: 'parsing',
      body: '<p>Parses the cleanup script output to extract counts. Looks for a <code>RESULT:</code> line with format:</p><p><code>RESULT: scanned=X orphaned=Y deleted=Z skipped=W docker_freed=XXB</code></p><p>Maps to <code>CleanupResult</code> fields: Scanned, Orphaned, Deleted, Skipped, DockerSpaceFreed.</p>'
    },
    TRACK: {
      title: 'Node Failed?',
      badge: 'decision', badgeText: 'error check',
      body: '<p>Checks if the SSH session or cleanup script returned an error. Failed nodes are tracked but do not terminate the workflow.</p>'
    },
    FAIL: {
      title: 'Track Failed Node',
      badge: 'error', badgeText: 'failure tracked',
      body: '<p>Records the node name and error in the failed nodes list. The workflow continues with remaining nodes.</p><p>Failed nodes are reported in the final error message so operators can investigate.</p>'
    },
    NEXT: {
      title: 'More Nodes?',
      badge: 'decision', badgeText: 'node loop',
      body: '<p>Checks if there are remaining nodes to process. If yes, loops back to clean the next node.</p>'
    },
    REPORT: {
      title: 'Report Totals',
      badge: 'workflow', badgeText: 'summary',
      body: '<p>Aggregates cleanup results across all nodes: total scanned, orphaned, deleted, and skipped directories.</p><p>If any nodes failed, returns an error listing the failed node names.</p>'
    },
    DONE: {
      title: 'Workflow Complete',
      badge: 'workflow', badgeText: 'result',
      body: '<p>Returns <code>[]CleanupResult</code> with per-node breakdown. The trigger binary logs the outcome.</p><p>If any nodes failed, the workflow returns a non-nil error alongside the partial results.</p>'
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
| <span style="color:#14b8a6">**Teal**</span> | Decision points & safety gates |
| <span style="color:#f85149">**Red**</span> | Error handling |
