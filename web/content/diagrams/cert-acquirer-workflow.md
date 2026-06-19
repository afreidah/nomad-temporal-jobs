---
title: "Cert Acquirer Workflow"
linkTitle: "Cert Acquirer"
weight: 70
---

Wildcard certificate acquisition via ACME DNS-01. Issuance and publish are **separate activities with separate retry policies** so a publish failure never re-runs ACME issuance (Let's Encrypt rate-limits duplicate issuance), and the private key never transits Temporal workflow history &mdash; it is staged in Vault between the two steps. **Hover over any step** for implementation details.

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
    '    START([CertAcquirer\\nWorkflow]):::workflow --> ISSUE[Issue Wildcard Cert\\nACME DNS-01]:::activity',
    '    ISSUE --> IGATE{Issued\\nOK?}:::decision',
    '    IGATE -->|rate-limited / fail| IFAIL[Return Error\\nnothing published]:::error',
    '    IGATE -->|ok, staged in Vault| PUBLISH[Publish Cert\\nto Vault]:::activity',
    '    PUBLISH --> PGATE{Published\\nOK?}:::decision',
    '    PGATE -->|fail| PRETRY[Temporal retries\\nup to 5x]:::workflow',
    '    PRETRY --> PUBLISH',
    '    PGATE -->|ok| DONE([Workflow\\nComplete]):::workflow',
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

  mermaid.render('cert-acquirer-mermaid-svg', diagramSrc).then(function(result) {
    document.getElementById('ac-diagram').innerHTML = result.svg;
    wireUpInteractivity();
  });

  var nodeInfo = {
    START: {
      title: 'CertAcquirer Workflow',
      badge: 'workflow', badgeText: 'workflow entry',
      body: '<p>Issues the <code>*.munchbox.cc</code> wildcard certificate and publishes it for Traefik to read.</p><p>Receives <code>IssueRequest</code> with <code>Domains</code> and <code>Email</code> from the schedule input. Runs on the cert-acquirer worker / <code>cert-task-queue</code>.</p>'
    },
    ISSUE: {
      title: 'Issue Wildcard Cert (ACME DNS-01)',
      badge: 'activity', badgeText: 'activity, 3 attempts',
      body: '<p><code>IssueWildcardCert</code> loads or creates the ACME account (persisted in Vault so registration happens once), then obtains the wildcard via Let\'s Encrypt DNS-01 using the Cloudflare token (pulled from Vault) and the <code>go-acme/lego</code> library. The issued cert + key are written to a <b>staging</b> Vault path.</p><p>Retry policy: 1m initial, 2x backoff, 5m max, 3 attempts. Let\'s Encrypt rate-limit errors are returned <b>non-retryable</b>.</p>'
    },
    IGATE: {
      title: 'Issued OK?',
      badge: 'decision', badgeText: 'gate',
      body: '<p>Publish is a separate activity, so issuance and publish fail independently. If issuance fails, nothing is published and the workflow returns the error.</p>'
    },
    IFAIL: {
      title: 'Return Error (nothing published)',
      badge: 'error', badgeText: 'early exit',
      body: '<p>Issuance failed (or hit a rate limit). The workflow returns without publishing; the previous live certificate stays in place.</p>'
    },
    PUBLISH: {
      title: 'Publish Cert to Vault',
      badge: 'activity', badgeText: 'activity, 5 attempts',
      body: '<p><code>PublishWildcardCert</code> reads the staged cert + key from Vault and writes them to the Traefik-readable path. Splitting issue and publish means a publish retry <b>never</b> re-runs ACME issuance &mdash; avoiding Let\'s Encrypt duplicate-issuance rate limits &mdash; and the private key never transits Temporal workflow history.</p><p>Retry policy: 1s initial, 2x backoff, 30s max, 5 attempts &mdash; cheap to retry since it is a local Vault write.</p>'
    },
    PGATE: {
      title: 'Published OK?',
      badge: 'decision', badgeText: 'gate',
      body: '<p>A transient Vault error retries the publish (up to 5 attempts) without touching the issued certificate.</p>'
    },
    PRETRY: {
      title: 'Temporal Retries (up to 5x)',
      badge: 'workflow', badgeText: 'durable retry',
      body: '<p>Temporal re-runs only the publish activity per its retry policy. The already-issued cert in the Vault staging path is reused, so no new ACME order is created.</p>'
    },
    DONE: {
      title: 'Workflow Complete',
      badge: 'workflow', badgeText: 'result',
      body: '<p>The wildcard certificate is published to Vault and ready for Traefik. Returns nil on success.</p>'
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
