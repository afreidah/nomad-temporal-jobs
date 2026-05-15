// -------------------------------------------------------------------------------
// Registry Garbage-Collect Activity - Docker Registry Blob Cleanup
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Runs `registry garbage-collect` against the storage volume of the cluster's
// container registry. Uses the Nomad API to scale the registry job to 0 for
// the duration of the GC run (the registry must be quiesced or concurrent
// pushes can drop blob references and corrupt storage), then scales back to 1.
//
// The GC itself runs as a one-shot docker container against the same bind
// mount as the live registry, so no copy of the data is ever made.
// -------------------------------------------------------------------------------

package activities

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"munchbox/temporal-workers/shared"

	"go.temporal.io/sdk/activity"
	"golang.org/x/crypto/ssh"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// RegistryGCConfig holds workflow-level configuration passed as input.
type RegistryGCConfig struct {
	// JobName identifies the registry's Nomad job. Defaults to "registry".
	JobName string `json:"job_name"`
	// RegistryDataDir is the host path bind-mounted into the registry
	// container as /var/lib/registry. Defaults to
	// "/mnt/gdrive/munchbox-data/registry".
	RegistryDataDir string `json:"registry_data_dir"`
	// RegistryImage is the docker image used for the one-shot GC run.
	// Should match the running registry's image. Defaults to "registry:3".
	RegistryImage string `json:"registry_image"`
	// DryRun runs garbage-collect with --dry-run, logging blobs that
	// would be deleted without actually freeing space. Default false.
	DryRun bool `json:"dry_run"`
	// DeleteUntagged tells GC to also remove manifests that aren't
	// referenced by any tag (and their blobs). Default true — without
	// it, image-pushed-then-retagged accumulates as "untagged" forever.
	DeleteUntagged bool `json:"delete_untagged"`
}

// RegistryGCResult holds the outcome of a single registry GC activity run.
type RegistryGCResult struct {
	NodeName       string `json:"node_name"`
	NodeAddr       string `json:"node_addr"`
	BlobsDeleted   int    `json:"blobs_deleted"`
	BytesReclaimed string `json:"bytes_reclaimed"`
	BeforeBytes    string `json:"before_bytes"`
	AfterBytes     string `json:"after_bytes"`
	DryRun         bool   `json:"dry_run"`
	Output         string `json:"output"`
}

// -------------------------------------------------------------------------
// ACTIVITIES
// -------------------------------------------------------------------------

// RegistryGarbageCollect locates the node currently running the registry
// alloc, scales the job to 0 to quiesce writes, runs the GC sequence
// against the bind-mounted storage, then scales the job back to 1. Returns
// before/after sizes and the parsed blob/byte counters from the GC tool.
func (a *Activities) RegistryGarbageCollect(ctx context.Context, config RegistryGCConfig) (RegistryGCResult, error) {
	logger := activity.GetLogger(ctx)

	// Defaults
	if config.JobName == "" {
		config.JobName = "registry"
	}
	if config.RegistryDataDir == "" {
		config.RegistryDataDir = "/mnt/gdrive/munchbox-data/registry"
	}
	if config.RegistryImage == "" {
		config.RegistryImage = "registry:3"
	}

	_, span := shared.StartClientSpan(ctx, "registry.garbage_collect",
		shared.PeerServiceAttr("registry"),
	)
	defer span.End()

	// --- Find the node hosting the registry alloc ---
	node, err := a.findRegistryNode(ctx, config.JobName)
	if err != nil {
		return RegistryGCResult{}, fmt.Errorf("locate registry node: %w", err)
	}
	logger.Info("Found registry node", "node", node.Name, "address", node.Address, "job", config.JobName)

	result := RegistryGCResult{
		NodeName: node.Name,
		NodeAddr: node.Address,
		DryRun:   config.DryRun,
	}

	// --- SSH in and run the GC sequence ---
	sshConfig, err := a.buildSSHConfig(node)
	if err != nil {
		return result, fmt.Errorf("build SSH config: %w", err)
	}

	client, err := ssh.Dial("tcp", fmt.Sprintf("%s:22", node.Address), sshConfig)
	if err != nil {
		return result, fmt.Errorf("SSH connect to %s: %w", node.Name, err)
	}
	defer client.Close()

	sudoPrefix := ""
	if node.IsOracle {
		sudoPrefix = "sudo "
	}

	script := buildRegistryGCScript(config, node.HTTPAddr, os.Getenv("NOMAD_TOKEN"), sudoPrefix)

	session, err := client.NewSession()
	if err != nil {
		return result, fmt.Errorf("SSH session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	logger.Info("Executing registry-gc script", "node", node.Name, "dry_run", config.DryRun)
	if err := session.Run(script); err != nil {
		result.Output = stdout.String() + "\n--- stderr ---\n" + stderr.String()
		return result, fmt.Errorf("registry-gc script failed: %w", err)
	}

	result.Output = stdout.String()
	parseRegistryGCOutput(&result, stdout.String())

	logger.Info("Registry GC complete",
		"node", node.Name,
		"blobs_deleted", result.BlobsDeleted,
		"bytes_reclaimed", result.BytesReclaimed,
		"before", result.BeforeBytes,
		"after", result.AfterBytes,
		"dry_run", config.DryRun)

	return result, nil
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// findRegistryNode queries the Nomad API for the running alloc of the
// registry job and returns the NodeInfo for SSH dialing. Errors if the job
// has no running alloc.
func (a *Activities) findRegistryNode(ctx context.Context, jobName string) (NodeInfo, error) {
	client, err := shared.NewNomadClient()
	if err != nil {
		return NodeInfo{}, fmt.Errorf("create Nomad client: %w", err)
	}

	allocs, _, err := client.Jobs().Allocations(jobName, false, nil)
	if err != nil {
		return NodeInfo{}, fmt.Errorf("list allocs for %q: %w", jobName, err)
	}

	for _, alloc := range allocs {
		if alloc.ClientStatus != "running" {
			continue
		}
		node, _, err := client.Nodes().Info(alloc.NodeID, nil)
		if err != nil {
			return NodeInfo{}, fmt.Errorf("get node info: %w", err)
		}
		addr := node.Attributes["unique.network.ip-address"]
		if addr == "" {
			addr = node.HTTPAddr
			if idx := strings.LastIndex(addr, ":"); idx != -1 {
				addr = addr[:idx]
			}
		}
		return NodeInfo{
			ID:       alloc.NodeID,
			Name:     node.Name,
			Address:  addr,
			HTTPAddr: node.HTTPAddr,
			IsOracle: strings.HasPrefix(node.Name, "oracle"),
		}, nil
	}

	return NodeInfo{}, fmt.Errorf("no running alloc for job %q", jobName)
}

// buildRegistryGCScript renders the bash script that runs on the registry
// host. Sequence: capture pre-size, scale job to 0, wait for allocs to
// terminate, run the registry GC docker container, scale job back to 1,
// capture post-size. The before/after sizes use `du -sh` against the bind
// mount.
func buildRegistryGCScript(config RegistryGCConfig, httpAddr, token, sudoPrefix string) string {
	dryRunFlag := ""
	if config.DryRun {
		dryRunFlag = "--dry-run"
	}
	deleteUntaggedFlag := ""
	if config.DeleteUntagged {
		deleteUntaggedFlag = "--delete-untagged"
	}

	return fmt.Sprintf(`#!/bin/bash
set -euo pipefail

JOB_NAME="%s"
DATA_DIR="%s"
IMAGE="%s"
NOMAD_HTTP_ADDR="%s"
NOMAD_TOKEN="%s"
DRY_RUN_FLAG="%s"
DELETE_UNTAGGED_FLAG="%s"

NOMAD_CA=""
for ca in /etc/nomad.d/tls/ca.crt /opt/nomad/tls/vault-intermediate-ca.pem; do
  if [ -f "$ca" ]; then
    NOMAD_CA="$ca"
    break
  fi
done
if [ -z "$NOMAD_CA" ]; then
  echo "ERROR: Could not find Nomad CA certificate"
  exit 1
fi

api() {
  %scurl -sf --cacert "$NOMAD_CA" -H "X-Nomad-Token: $NOMAD_TOKEN" "$@"
}

scale_job() {
  local count=$1
  echo "Scaling $JOB_NAME to count=$count"
  api -X POST "https://${NOMAD_HTTP_ADDR}/v1/job/${JOB_NAME}/scale" \
    -d "{\"Count\": $count, \"Reason\": \"registry-gc\"}" >/dev/null
}

wait_for_no_running_allocs() {
  local deadline=$(( $(date +%%s) + 180 ))
  while [ $(date +%%s) -lt $deadline ]; do
    local count
    count=$(api "https://${NOMAD_HTTP_ADDR}/v1/job/${JOB_NAME}/allocations" \
      | jq '[.[] | select(.ClientStatus == "running")] | length' 2>/dev/null || echo "?")
    if [ "$count" = "0" ]; then
      return 0
    fi
    echo "Waiting for $JOB_NAME allocs to drain (running=$count)..."
    sleep 3
  done
  echo "ERROR: timed out waiting for $JOB_NAME allocs to drain"
  return 1
}

wait_for_running_alloc() {
  local deadline=$(( $(date +%%s) + 300 ))
  while [ $(date +%%s) -lt $deadline ]; do
    local count
    count=$(api "https://${NOMAD_HTTP_ADDR}/v1/job/${JOB_NAME}/allocations" \
      | jq '[.[] | select(.ClientStatus == "running")] | length' 2>/dev/null || echo "?")
    if [ "$count" = "1" ]; then
      return 0
    fi
    echo "Waiting for $JOB_NAME alloc to come back (running=$count)..."
    sleep 3
  done
  echo "ERROR: timed out waiting for $JOB_NAME alloc to come back"
  return 1
}

before_bytes=$(%sdu -sb "$DATA_DIR" | cut -f1)
echo "BEFORE_BYTES=$before_bytes"
echo "BEFORE_HUMAN=$(%sdu -sh "$DATA_DIR" | cut -f1)"

scale_job 0
wait_for_no_running_allocs

# The registry image embeds /etc/distribution/config.yml as the default
# config the binary reads. The garbage-collect subcommand only needs the
# storage rootdirectory from that config, which the embedded default points
# at /var/lib/registry — same path as the live container's bind mount.
echo "=== Running registry garbage-collect ==="
%sdocker run --rm \
  -v "$DATA_DIR:/var/lib/registry" \
  "$IMAGE" \
  garbage-collect $DRY_RUN_FLAG $DELETE_UNTAGGED_FLAG /etc/distribution/config.yml 2>&1 | tee /tmp/registry-gc.out
echo "=== End of registry garbage-collect output ==="

scale_job 1
wait_for_running_alloc

after_bytes=$(%sdu -sb "$DATA_DIR" | cut -f1)
echo "AFTER_BYTES=$after_bytes"
echo "AFTER_HUMAN=$(%sdu -sh "$DATA_DIR" | cut -f1)"

# Parse blobs deleted from the GC output. Format:
#   "blob eligible for deletion: sha256:..." per blob
blobs_deleted=$(grep -c "^blob eligible for deletion:" /tmp/registry-gc.out 2>/dev/null || echo 0)
echo "BLOBS_DELETED=$blobs_deleted"

reclaimed=$(( before_bytes - after_bytes ))
echo "RECLAIMED_BYTES=$reclaimed"
echo "RESULT: blobs_deleted=$blobs_deleted reclaimed_bytes=$reclaimed before_bytes=$before_bytes after_bytes=$after_bytes"
`, config.JobName, config.RegistryDataDir, config.RegistryImage, httpAddr, token,
		dryRunFlag, deleteUntaggedFlag,
		sudoPrefix, sudoPrefix, sudoPrefix, sudoPrefix, sudoPrefix, sudoPrefix)
}

// parseRegistryGCOutput pulls the structured RESULT line and BEFORE/AFTER
// human-readable sizes out of the script's stdout and populates the result
// struct.
func parseRegistryGCOutput(result *RegistryGCResult, output string) {
	for _, line := range strings.Split(output, "\n") {
		switch {
		case strings.HasPrefix(line, "BEFORE_HUMAN="):
			result.BeforeBytes = strings.TrimPrefix(line, "BEFORE_HUMAN=")
		case strings.HasPrefix(line, "AFTER_HUMAN="):
			result.AfterBytes = strings.TrimPrefix(line, "AFTER_HUMAN=")
		case strings.HasPrefix(line, "RESULT:"):
			for _, part := range strings.Fields(line) {
				switch {
				case strings.HasPrefix(part, "blobs_deleted="):
					_, _ = fmt.Sscanf(part, "blobs_deleted=%d", &result.BlobsDeleted)
				case strings.HasPrefix(part, "reclaimed_bytes="):
					var n int64
					if _, err := fmt.Sscanf(part, "reclaimed_bytes=%d", &n); err == nil {
						result.BytesReclaimed = humanBytes(n)
					}
				}
			}
		}
	}
}

// humanBytes renders a byte count in a compact human-friendly form (KiB,
// MiB, GiB) matching the shape of `du -h`. Used for the BytesReclaimed
// field so log lines and result JSON read consistently with the BeforeBytes
// / AfterBytes values.
func humanBytes(n int64) string {
	const unit = 1024
	if n < 0 {
		return fmt.Sprintf("%dB", n)
	}
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	suffixes := []string{"KiB", "MiB", "GiB", "TiB"}
	if exp >= len(suffixes) {
		exp = len(suffixes) - 1
	}
	val := float64(n) / float64(div)
	if val >= 100 {
		return fmt.Sprintf("%.0f%s", val, suffixes[exp])
	}
	return fmt.Sprintf("%.1f%s", val, suffixes[exp])
}

