---
title: "Runner Scaler Workflow"
linkTitle: "Runner Scaler"
weight: 90
---

Scales self-hosted CI runners on demand (zero idle). On a short schedule, the `PollAndDispatch` parent loads the per-repo config from Consul (`runners/config`) and scans each repo for queued `self-hosted` Actions jobs, then **reconciles by depth**: it buckets the queued jobs by `(repo, labels)`, counts the runners already pending/running, and starts one `HandleRunner` child per missing runner &mdash; clamped by an optional `maxConcurrent` cap. A bucket left short is simply topped back up on the next tick, so there is no per-job dedup or external state store. Each child dispatches one ephemeral Nomad runner &mdash; `app`-mode repos mint a registration token, `vault`-mode repos pass a Vault secret path the runner self-registers with &mdash; and a backstop timer reaps a runner that never picked its job up. **Hover over any step** for implementation details.

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
    '    START([PollAndDispatch\\nParent]):::workflow --> DEFAULTS[Apply Config\\nDefaults]:::workflow',
    '    DEFAULTS --> LOADCFG[Load Config\\nConsul runners/config]:::activity',
    '    LOADCFG --> ACQUIRE[Acquire Slot\\nsem = Concurrency]:::workflow',
    '    ACQUIRE --> LISTJOBS[List Queued Jobs\\nApp or PAT, per repo]:::activity',
    '    LISTJOBS --> JOBGATE{List\\nOK?}:::decision',
    '    JOBGATE -->|failed| SKIPREPO[Skip Repo\\nthis tick]:::error',
    '    JOBGATE -->|ok| MOREREPOS{More\\nRepos?}:::decision',
    '    SKIPREPO --> MOREREPOS',
    '    MOREREPOS -->|yes, slot frees| ACQUIRE',
    '    MOREREPOS -->|no, all scanned| COUNT[Count Active Runners\\nNomad, per bucket]:::activity',
    '    COUNT --> BUCKET[Bucket Jobs\\nby repo + labels]:::workflow',
    '    BUCKET --> RECONCILE{Shortfall?\\nqueued - active, capped}:::decision',
    '    RECONCILE -->|covered| DONE([Poll Complete\\nsummary]):::workflow',
    '    RECONCILE -->|each missing runner| STARTCHILD[Start Child\\nrunner-runid-seq]:::workflow',
    '    STARTCHILD --> DONE',
    '',
    '    STARTCHILD -.spawns.-> CSTART',
    '    CSTART([HandleRunner\\nChild]):::workflow --> DISPATCH[Dispatch Runner\\nmint token or Vault secret]:::activity',
    '    DISPATCH --> WAIT[Wait Runner Done\\nbackstop deadline]:::activity',
    '    WAIT --> REAP[Reap Runner\\nstop Nomad job]:::activity',
    '    REAP --> CDONE([Child\\nComplete]):::workflow',
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

  mermaid.render('runnerscaler-mermaid-svg', diagramSrc).then(function(result) {
    document.getElementById('ac-diagram').innerHTML = result.svg;
    wireUpInteractivity();
  });

  var nodeInfo = {
    START: {
      title: 'PollAndDispatch Parent',
      badge: 'workflow', badgeText: 'workflow entry',
      body: '<p>Fired by a Temporal Schedule every ~30s (outbound poll; no inbound webhook). Receives <code>PollConfig</code> (scan concurrency, reap backstop) and runs on the ci-runner-scaler worker / <code>ci-runner-scaler-task-queue</code>.</p>'
    },
    DEFAULTS: {
      title: 'Apply Config Defaults',
      badge: 'workflow', badgeText: 'workflow logic',
      body: '<p>Fills unset fields deterministically across replay. <code>Concurrency</code>: <code>4</code> &mdash; forced positive so the semaphore can\'t size to 0 and deadlock.</p>'
    },
    LOADCFG: {
      title: 'Load Config',
      badge: 'activity', badgeText: 'activity',
      body: '<p><code>LoadConfig</code> reads the per-repo JSON from Consul KV (<code>runners/config</code>): a map of <code>owner/repo</code> &rarr; <code>{mode, vaultPath, registerVaultPath, profiles[], maxConcurrent}</code>. Repos are then scanned in sorted order for deterministic replay. A missing or malformed key is non-retryable.</p>'
    },
    ACQUIRE: {
      title: 'Acquire Slot (semaphore)',
      badge: 'workflow', badgeText: 'bounded concurrency',
      body: '<p>Repos are scanned through a buffered channel sized to <code>Concurrency</code>, so a large fleet never bursts the GitHub API.</p>'
    },
    LISTJOBS: {
      title: 'List Queued Jobs (per repo)',
      badge: 'activity', badgeText: 'activity, per-repo',
      body: '<p><code>ListQueuedJobs</code> enumerates the repo\'s queued/in_progress workflow runs and keeps the jobs still <code>queued</code> with a <code>self-hosted</code> label. <code>app</code>-mode repos use the shared GitHub App installation client; <code>vault</code>-mode repos use a PAT read from the repo\'s <code>vaultPath</code>. 3 attempts with backoff.</p>'
    },
    JOBGATE: {
      title: 'List OK?',
      badge: 'decision', badgeText: 'per-repo outcome',
      body: '<p>A repo whose listing fails is skipped for this tick; the run continues with the other repos.</p>'
    },
    SKIPREPO: {
      title: 'Skip Repo (this tick)',
      badge: 'error', badgeText: 'non-fatal',
      body: '<p>The failure is logged and the repo is retried on the next scheduled tick.</p>'
    },
    MOREREPOS: {
      title: 'More Repos?',
      badge: 'decision', badgeText: 'scan loop',
      body: '<p>Each completed scan frees a slot for the next repo. Once every repo has been scanned, the parent moves from scanning to reconciliation.</p>'
    },
    COUNT: {
      title: 'Count Active Runners',
      badge: 'activity', badgeText: 'activity',
      body: '<p><code>CountActiveRunners</code> tallies the pending/running ephemeral runners per <code>(repo, labels)</code> bucket, across every dispatch job a profile names (not just the default) so a bucket served by a non-default job isn\'t miscounted as empty. Fatal to the tick if it fails &mdash; dispatching without it would double-provision.</p>'
    },
    BUCKET: {
      title: 'Bucket Queued Jobs',
      badge: 'workflow', badgeText: 'workflow logic',
      body: '<p>Queued jobs are grouped by <code>(repo, labels)</code>; each bucket\'s demand is its queued count. Buckets are processed in deterministic order.</p>'
    },
    RECONCILE: {
      title: 'Shortfall?',
      badge: 'decision', badgeText: 'reconcile by depth',
      body: '<p><code>needed = queued &minus; active</code>, clamped to the pool\'s <code>maxConcurrent</code> (the matched profile\'s cap, else the repo-wide cap). A covered or over-covered bucket dispatches nothing; overflow stays queued on GitHub until a runner frees.</p>'
    },
    STARTCHILD: {
      title: 'Start Child',
      badge: 'workflow', badgeText: 'child workflow',
      body: '<p>Starts <code>HandleRunner</code> with <code>WorkflowID = runner-&lt;parent-run-id&gt;-&lt;seq&gt;</code>, <code>ALLOW_DUPLICATE</code> reuse, and <code>ABANDON</code> parent-close. The parent waits for the child to <i>start</i> (not finish) so an abandoned child is never dropped. The first matching profile selects the runner job + image; a bare <code>[self-hosted]</code> job falls back to the default job.</p>'
    },
    DONE: {
      title: 'Poll Complete',
      badge: 'workflow', badgeText: 'result',
      body: '<p>Returns <code>PollResult</code> (repos scanned, queued, active, started). Children outlive this tick.</p>'
    },
    CSTART: {
      title: 'HandleRunner Child',
      badge: 'workflow', badgeText: 'child entry',
      body: '<p>One child backs one runner slot. Receives <code>RunnerSpec</code> (repo, labels, job, image, mint-vs-vault, reap backstop).</p>'
    },
    DISPATCH: {
      title: 'Dispatch Runner',
      badge: 'activity', badgeText: 'activity, NoRetry',
      body: '<p><code>DispatchRunner</code> dispatches one ephemeral parameterized Nomad job with <code>repo_url</code> / <code>labels</code> (and <code>runner_image</code>) meta. <code>app</code>-mode mints a registration token (<code>runner_token</code>, minted inside the activity so it never enters workflow history); <code>vault</code>-mode passes <code>runner_secret</code> &mdash; a Vault path the job reads to self-register, minting nothing. NoRetry: each call creates a runner, so a retried dispatch would double up.</p>'
    },
    WAIT: {
      title: 'Wait Runner Done',
      badge: 'activity', badgeText: 'activity, heartbeat',
      body: '<p><code>WaitRunnerDone</code> blocks (heartbeating) until the ephemeral runner\'s Nomad job completes, bounded by a backstop deadline sized as an upper bound on one CI job. On the happy path the runner takes its one job and exits well before it fires.</p>'
    },
    REAP: {
      title: 'Reap Runner',
      badge: 'activity', badgeText: 'activity, disconnected ctx',
      body: '<p><code>ReapRunner</code> stops and purges the dispatched Nomad job on a disconnected context (so a closing/cancelled workflow still cleans up). An already-gone job counts as success.</p>'
    },
    CDONE: {
      title: 'Child Complete',
      badge: 'workflow', badgeText: 'result',
      body: '<p>The runner ran its job and was reaped. GitHub auto-removes the offline ephemeral runner registration.</p>'
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
| <span style="color:#f85149">**Red**</span> | Skips (non-fatal) |
