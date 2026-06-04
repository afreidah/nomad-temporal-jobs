// -------------------------------------------------------------------------------
// Maintenance Saga Activities - Shared Job Scale / Wait / Find / Measure
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Generic, job-name-parameterized activities shared by the registry-GC and
// aptly-cleanup sagas: locate a job's node, scale a job, wait for its allocs
// to drain or come back, and measure a host directory. The scale/wait core
// lives in shared.ScaleNomadJob / shared.WaitNomadAllocCount; these wrappers
// add the Temporal span, heartbeat, logging, and error classification.
// -------------------------------------------------------------------------------

package activities

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"munchbox/temporal-workers/shared"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

// -------------------------------------------------------------------------
// FIND JOB NODE
// -------------------------------------------------------------------------

// FindJobNode queries the Nomad API for a running alloc of the named job and
// returns the NodeInfo for SSH dialing. Wraps a "no running alloc" condition
// as a non-retryable error so the workflow fails fast instead of retry-
// storming on a terminally-misconfigured cluster.
func (a *Activities) FindJobNode(ctx context.Context, jobName string) (NodeInfo, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Finding node for job", "job", jobName)

	_, span := shared.StartClientSpan(ctx, "nomad.find_job_node",
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
// MEASURE DATA DIR
// -------------------------------------------------------------------------

// MeasureDataDir returns the size in bytes of a directory on the given node.
// Used for before/after reporting. SSH-only because the path is host-side
// (e.g. /mnt/gdrive); the Nomad API doesn't expose disk usage.
func (a *Activities) MeasureDataDir(ctx context.Context, node NodeInfo, dataDir string) (int64, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Measuring data dir", "node", node.Name, "path", dataDir)

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
// SCALE JOB
// -------------------------------------------------------------------------

// ScaleJob scales the named Nomad job's task group to the target count.
// Idempotent — Nomad accepts the call when the job is already at the requested
// count. Used to scale down to 0 before a maintenance op and back to 1 in the
// deferred compensation. A "job not found" error is wrapped as non-retryable;
// transient API errors surface plain so Temporal retries per the activity's
// RetryPolicy.
func (a *Activities) ScaleJob(ctx context.Context, jobName, groupName string, count int) error {
	logger := activity.GetLogger(ctx)
	logger.Info("Scaling Nomad job", "job", jobName, "group", groupName, "count", count)

	_, span := shared.StartClientSpan(ctx, "nomad.scale_job",
		shared.PeerServiceAttr("nomad"),
	)
	defer span.End()

	client, err := shared.NewNomadClient()
	if err != nil {
		return fmt.Errorf("create Nomad client: %w", err)
	}
	reason := fmt.Sprintf("temporal workflow: scale to %d", count)
	if err := shared.ScaleNomadJob(client, jobName, groupName, count, reason); err != nil {
		if strings.Contains(err.Error(), "job not found") || strings.Contains(err.Error(), "404") {
			return temporal.NewNonRetryableApplicationError(err.Error(), "JobNotFound", err)
		}
		return err
	}
	return nil
}

// -------------------------------------------------------------------------
// WAIT FOR ALLOCS DRAINED / RUNNING
// -------------------------------------------------------------------------

// WaitJobDrained polls the Nomad API until the named job has zero running
// allocations. Heartbeats every poll. Bounded by the activity's
// StartToCloseTimeout (set on the workflow side); returns ctx.Err() when
// exceeded.
func (a *Activities) WaitJobDrained(ctx context.Context, jobName string) error {
	return a.waitAllocCount(ctx, jobName, 0, 3*time.Second, "drained")
}

// WaitJobRunning polls the Nomad API until the named job has at least one
// running allocation (i.e. the scale-up succeeded and a new alloc passed its
// start sequence). Bounded by the activity's StartToCloseTimeout.
func (a *Activities) WaitJobRunning(ctx context.Context, jobName string) error {
	return a.waitAllocCount(ctx, jobName, 1, 3*time.Second, "running")
}

// waitAllocCount wraps shared.WaitNomadAllocCount with activity heartbeat and
// logging. Target 0 succeeds when running drops to 0; >=1 succeeds when running
// is at least target.
func (a *Activities) waitAllocCount(ctx context.Context, jobName string, target int, interval time.Duration, label string) error {
	logger := activity.GetLogger(ctx)
	client, err := shared.NewNomadClient()
	if err != nil {
		return fmt.Errorf("create Nomad client: %w", err)
	}
	return shared.WaitNomadAllocCount(ctx, client, jobName, target, interval, func(running int) {
		activity.RecordHeartbeat(ctx, running)
		logger.Info("Waiting", "job", jobName, "label", label, "running", running, "target", target)
	})
}

// -------------------------------------------------------------------------
// FORMAT HELPER
// -------------------------------------------------------------------------

// HumanBytes renders a byte count in a compact human-friendly form (KiB, MiB,
// GiB) matching the shape of `du -h`. Exported so workflows can format
// before/after/reclaimed sizes consistently.
func HumanBytes(n int64) string {
	const unit = 1024
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
