// -------------------------------------------------------------------------------
// Registry Garbage-Collect Activities - Saga-Style Decomposition
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// The registry GC workflow is decomposed into six small focused activities so
// Temporal can apply per-step retry policies, surface each step in the
// workflow history, and (crucially) so the workflow can use a saga-style
// `defer` with `workflow.NewDisconnectedContext` to guarantee the
// scale-back-to-1 compensation runs even if GC fails or the workflow is
// cancelled mid-flight.
//
//   1. FindRegistryNode             - locate the node hosting the registry alloc
//   2. MeasureRegistryDataDir       - du -sb over SSH; before/after sizes
//   3. ScaleRegistry                - POST /v1/job/{name}/scale (idempotent)
//   4. WaitRegistryAllocsDrained    - poll until 0 running allocs
//   5. WaitRegistryAllocRunning     - poll until >=1 running alloc
//   6. RunRegistryGarbageCollect    - long-running; ssh + docker run
//
// Activities 3-5 talk to the Nomad API directly via the shared Nomad client
// (no SSH). Activities 2 and 6 SSH to the registry host.
// -------------------------------------------------------------------------------

package activities

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"munchbox/temporal-workers/shared"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"golang.org/x/crypto/ssh"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// RegistryGCConfig holds workflow-level configuration passed as input.
type RegistryGCConfig struct {
	// JobName identifies the registry's Nomad job. Defaults to "registry".
	JobName string `json:"job_name"`
	// GroupName is the task group inside the job to scale. Defaults to the
	// JobName (matches the convention used by the munchbox-service pack).
	GroupName string `json:"group_name"`
	// RegistryDataDir is the host path bind-mounted into the registry
	// container as /var/lib/registry. Defaults to
	// "/mnt/gdrive/munchbox-data/registry".
	RegistryDataDir string `json:"registry_data_dir"`
	// RegistryImage is the docker image used for the one-shot GC run.
	// Should match the running registry's image. Defaults to "registry:3".
	RegistryImage string `json:"registry_image"`
	// DryRun runs garbage-collect with --dry-run, logging blobs that
	// would be deleted without actually freeing space.
	DryRun bool `json:"dry_run"`
	// DeleteUntagged tells GC to also remove manifests not referenced by
	// any tag (and the blobs they reference). Default true — without it
	// every tag overwrite (e.g. CI re-pushing :latest) leaves an
	// orphaned manifest forever.
	DeleteUntagged bool `json:"delete_untagged"`
}

// ApplyDefaults fills in unset fields with their defaults. Called by the
// workflow before any activities run so every activity sees a fully
// populated config and the values are deterministic across replay.
func (c *RegistryGCConfig) ApplyDefaults() {
	if c.JobName == "" {
		c.JobName = "registry"
	}
	if c.GroupName == "" {
		c.GroupName = c.JobName
	}
	if c.RegistryDataDir == "" {
		c.RegistryDataDir = "/mnt/gdrive/munchbox-data/registry"
	}
	if c.RegistryImage == "" {
		c.RegistryImage = "registry:3"
	}
}

// RegistryGCResult holds the workflow-level outcome reported back to the
// trigger / caller.
type RegistryGCResult struct {
	NodeName       string `json:"node_name"`
	NodeAddr       string `json:"node_addr"`
	BlobsDeleted   int    `json:"blobs_deleted"`
	BytesReclaimed string `json:"bytes_reclaimed"`
	BeforeBytes    string `json:"before_bytes"`
	AfterBytes     string `json:"after_bytes"`
	DryRun         bool   `json:"dry_run"`
}

// RegistryGCRunResult is the small struct returned by
// RunRegistryGarbageCollect. The workflow folds it into RegistryGCResult.
type RegistryGCRunResult struct {
	BlobsDeleted int    `json:"blobs_deleted"`
	Output       string `json:"output"`
}

// -------------------------------------------------------------------------
// ACTIVITY 1: FIND REGISTRY NODE
// -------------------------------------------------------------------------

// FindRegistryNode queries the Nomad API for the running alloc of the
// registry job and returns the NodeInfo for SSH dialing. Wraps a
// "no running alloc" condition as a non-retryable error so the workflow
// fails fast instead of retry-storming on a terminally-misconfigured
// cluster.
func (a *Activities) FindRegistryNode(ctx context.Context, jobName string) (NodeInfo, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Finding registry node", "job", jobName)

	_, span := shared.StartClientSpan(ctx, "nomad.find_registry_node",
		shared.PeerServiceAttr("nomad"),
	)
	defer span.End()

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

	return NodeInfo{}, temporal.NewNonRetryableApplicationError(
		fmt.Sprintf("no running alloc for job %q", jobName),
		"NoRunningAlloc",
		nil,
	)
}

// -------------------------------------------------------------------------
// ACTIVITY 2: MEASURE REGISTRY DATA DIR
// -------------------------------------------------------------------------

// MeasureRegistryDataDir returns the size in bytes of the registry's
// bind-mounted storage directory on the given node. Used for before/after
// reporting. SSH-only because /mnt/gdrive is host-side; the Nomad API
// doesn't expose disk usage.
func (a *Activities) MeasureRegistryDataDir(ctx context.Context, node NodeInfo, dataDir string) (int64, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Measuring registry data dir", "node", node.Name, "path", dataDir)

	sudoPrefix := ""
	if node.IsOracle {
		sudoPrefix = "sudo "
	}
	out, err := a.runSSHCommand(node, fmt.Sprintf("%sdu -sb %s | cut -f1", sudoPrefix, shellQuote(dataDir)))
	if err != nil {
		return 0, fmt.Errorf("du on %s: %w", node.Name, err)
	}
	n, parseErr := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if parseErr != nil {
		return 0, fmt.Errorf("parse du output %q: %w", out, parseErr)
	}
	return n, nil
}

// -------------------------------------------------------------------------
// ACTIVITY 3: SCALE REGISTRY
// -------------------------------------------------------------------------

// ScaleRegistry scales the named Nomad job's task group to the target
// count. Idempotent — Nomad accepts the call when the job is already at
// the requested count and returns success. Used both to scale down to 0
// before GC and to scale back to 1 in the deferred compensation. A
// "job not found" error is wrapped as non-retryable; transient API errors
// surface plain so Temporal retries per the activity's RetryPolicy.
func (a *Activities) ScaleRegistry(ctx context.Context, jobName, groupName string, count int) error {
	logger := activity.GetLogger(ctx)
	logger.Info("Scaling registry job", "job", jobName, "group", groupName, "count", count)

	_, span := shared.StartClientSpan(ctx, "nomad.scale_registry",
		shared.PeerServiceAttr("nomad"),
	)
	defer span.End()

	client, err := shared.NewNomadClient()
	if err != nil {
		return fmt.Errorf("create Nomad client: %w", err)
	}
	c := count
	msg := fmt.Sprintf("registry-gc workflow: scale to %d", count)
	if _, _, err := client.Jobs().Scale(jobName, groupName, &c, msg, false, nil, nil); err != nil {
		if strings.Contains(err.Error(), "job not found") || strings.Contains(err.Error(), "404") {
			return temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("scale %s/%s to %d: %v", jobName, groupName, count, err),
				"JobNotFound",
				err,
			)
		}
		return fmt.Errorf("scale %s/%s to %d: %w", jobName, groupName, count, err)
	}
	return nil
}

// -------------------------------------------------------------------------
// ACTIVITY 4: WAIT FOR ALLOCS DRAINED
// -------------------------------------------------------------------------

// WaitRegistryAllocsDrained polls the Nomad API until the named job has
// zero running allocations. Heartbeats every poll. Bounded by the
// activity's StartToCloseTimeout (set on the workflow side); returns
// ctx.Err() when exceeded.
func (a *Activities) WaitRegistryAllocsDrained(ctx context.Context, jobName string) error {
	return a.waitAllocCount(ctx, jobName, 0, 3*time.Second, "drained")
}

// -------------------------------------------------------------------------
// ACTIVITY 5: WAIT FOR ALLOC RUNNING
// -------------------------------------------------------------------------

// WaitRegistryAllocRunning polls the Nomad API until the named job has at
// least one running allocation (i.e. the scale-up succeeded and a new
// alloc passed its start sequence). Bounded by the activity's
// StartToCloseTimeout.
func (a *Activities) WaitRegistryAllocRunning(ctx context.Context, jobName string) error {
	return a.waitAllocCount(ctx, jobName, 1, 3*time.Second, "running")
}

// waitAllocCount is the shared poll loop for the wait activities. Target
// 0 succeeds when running drops to 0; >=1 succeeds when running is at
// least target.
func (a *Activities) waitAllocCount(ctx context.Context, jobName string, target int, interval time.Duration, label string) error {
	logger := activity.GetLogger(ctx)
	client, err := shared.NewNomadClient()
	if err != nil {
		return fmt.Errorf("create Nomad client: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		allocs, _, err := client.Jobs().Allocations(jobName, false, nil)
		if err != nil {
			logger.Warn("alloc list failed; will retry", "job", jobName, "error", err)
		} else {
			running := 0
			for _, al := range allocs {
				if al.ClientStatus == "running" {
					running++
				}
			}
			activity.RecordHeartbeat(ctx, running)
			if (target == 0 && running == 0) || (target > 0 && running >= target) {
				logger.Info("Wait condition met", "job", jobName, "label", label, "running", running)
				return nil
			}
			logger.Info("Waiting", "job", jobName, "label", label, "running", running, "target", target)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// -------------------------------------------------------------------------
// ACTIVITY 6: RUN GARBAGE-COLLECT
// -------------------------------------------------------------------------

// RunRegistryGarbageCollect SSHes to the registry host and runs the
// docker garbage-collect command against the bind-mounted storage. This
// is the long-running step (multi-GB registries take minutes); it
// heartbeats periodically and reports the count of "blob eligible for
// deletion" lines emitted by the registry tool.
//
// Configured with MaxAttempts=1 by the workflow — a partial GC run
// shouldn't be repeated; let the deferred scale-back put the registry
// online instead and surface the failure.
func (a *Activities) RunRegistryGarbageCollect(ctx context.Context, node NodeInfo, config RegistryGCConfig) (RegistryGCRunResult, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Running registry garbage-collect",
		"node", node.Name, "image", config.RegistryImage,
		"dry_run", config.DryRun, "delete_untagged", config.DeleteUntagged)

	sudoPrefix := ""
	if node.IsOracle {
		sudoPrefix = "sudo "
	}

	dryRunFlag := ""
	if config.DryRun {
		dryRunFlag = "--dry-run"
	}
	deleteUntaggedFlag := ""
	if config.DeleteUntagged {
		deleteUntaggedFlag = "--delete-untagged"
	}

	// The registry image embeds /etc/distribution/config.yml as the
	// default config its binary reads. The garbage-collect subcommand
	// only needs the storage rootdirectory from that config, which the
	// embedded default points at /var/lib/registry — same path as the
	// live container's bind mount.
	cmd := fmt.Sprintf(
		`%sdocker run --rm -v %s:/var/lib/registry %s garbage-collect %s %s /etc/distribution/config.yml`,
		sudoPrefix,
		shellQuote(config.RegistryDataDir),
		shellQuote(config.RegistryImage),
		dryRunFlag,
		deleteUntaggedFlag,
	)

	out, err := a.runSSHCommandWithHeartbeat(ctx, node, cmd, 30*time.Second)
	if err != nil {
		return RegistryGCRunResult{Output: out}, fmt.Errorf("docker run garbage-collect on %s: %w", node.Name, err)
	}

	return RegistryGCRunResult{
		BlobsDeleted: parseBlobsDeleted(out),
		Output:       out,
	}, nil
}

// -------------------------------------------------------------------------
// SSH HELPERS (private; used only by the saga activities)
// -------------------------------------------------------------------------

// runSSHCommand opens an SSH session to the node, runs cmd, and returns
// stdout. Used by short measurement commands.
func (a *Activities) runSSHCommand(node NodeInfo, cmd string) (string, error) {
	cfg, err := a.buildSSHConfig(node)
	if err != nil {
		return "", err
	}
	cli, err := ssh.Dial("tcp", node.Address+":22", cfg)
	if err != nil {
		return "", fmt.Errorf("ssh dial %s: %w", node.Name, err)
	}
	defer cli.Close()

	session, err := cli.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	if err := session.Run(cmd); err != nil {
		return stdout.String(), fmt.Errorf("ssh run %q: %w (stderr: %s)", cmd, err, stderr.String())
	}
	return stdout.String(), nil
}

// runSSHCommandWithHeartbeat runs an SSH command that may take a long
// time (registry GC) and emits an activity.RecordHeartbeat every
// `interval` so the activity's HeartbeatTimeout can detect stalls.
// Returns combined stdout+stderr captured up to that point.
func (a *Activities) runSSHCommandWithHeartbeat(ctx context.Context, node NodeInfo, cmd string, interval time.Duration) (string, error) {
	cfg, err := a.buildSSHConfig(node)
	if err != nil {
		return "", err
	}
	cli, err := ssh.Dial("tcp", node.Address+":22", cfg)
	if err != nil {
		return "", fmt.Errorf("ssh dial %s: %w", node.Name, err)
	}
	defer cli.Close()

	session, err := cli.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	var combined bytes.Buffer
	session.Stdout = &combined
	session.Stderr = &combined

	if err := session.Start(cmd); err != nil {
		return "", fmt.Errorf("ssh start %q: %w", cmd, err)
	}

	done := make(chan error, 1)
	go func() { done <- session.Wait() }()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case err := <-done:
			if err != nil {
				return combined.String(), fmt.Errorf("ssh run %q: %w (output: %s)", cmd, err, combined.String())
			}
			return combined.String(), nil
		case <-ticker.C:
			activity.RecordHeartbeat(ctx, combined.Len())
		case <-ctx.Done():
			_ = session.Signal(ssh.SIGTERM)
			return combined.String(), ctx.Err()
		}
	}
}

// -------------------------------------------------------------------------
// PARSE / FORMAT HELPERS
// -------------------------------------------------------------------------

// parseBlobsDeleted counts "blob eligible for deletion:" lines emitted by
// the registry garbage-collect tool.
func parseBlobsDeleted(output string) int {
	count := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "blob eligible for deletion:") {
			count++
		}
	}
	return count
}

// HumanBytes renders a byte count in a compact human-friendly form (KiB,
// MiB, GiB) matching the shape of `du -h`. Exported so the workflow can
// format the before/after/reclaimed sizes consistently.
func HumanBytes(n int64) string {
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

// shellQuote single-quotes a string for safe inclusion in a remote bash
// command. Embedded single quotes are escaped via the canonical
// `'\''` sequence.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
