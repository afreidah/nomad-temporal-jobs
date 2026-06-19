// -------------------------------------------------------------------------------
// Registry GC Workflow - Saga-Style Docker Registry Garbage-Collection
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Decomposes the GC sequence into per-step activities and orchestrates them
// as a saga: scale-down to 0, run GC, scale-back to 1. The scale-back is
// registered via `defer` with `workflow.NewDisconnectedContext` so it
// always runs — even if GC fails, the activity times out, or the parent
// workflow is cancelled. This guarantees the registry never gets stranded
// at count=0 because the workflow died mid-sequence.
//
// The find / scale / wait / measure steps are the generic saga activities from
// the shared nodes package; only RunRegistryGarbageCollect is registry-specific.
//
// Step-by-step retry policies (set on the activity options):
//   - FindJobNode, ScaleJob, MeasureDataDir:
//     3 attempts, exponential backoff. Transient Nomad API blips deserve
//     retry.
//   - WaitJob{Drained,Running}: bounded by StartToCloseTimeout
//     (5 min). Internal poll loop heartbeats; Temporal kills the activity
//     if it stalls.
//   - RunRegistryGarbageCollect: MaxAttempts=1. Don't restart a partially
//     finished GC; let the deferred scale-back put the registry online and
//     surface the failure to the operator.
// -------------------------------------------------------------------------------

package registrygc

import (
	"errors"
	"fmt"
	"time"

	"go.temporal.io/sdk/workflow"

	"munchbox/temporal-workers/maintenance/internal/nodes"
	"munchbox/temporal-workers/shared"
)

// Nil-typed activity stubs for compile-time method references: saga steps come
// from the shared nodes package, the GC step from this package's Activities.
var (
	saga *nodes.SagaActivities
	acts *Activities
)

// retryAlwaysRetryable is the standard Nomad-API-blip retry policy used by the
// short, idempotent activities. retryNeverRetryable is used by the long-running
// GC activity: a partial GC shouldn't be retried — the deferred scale-back is
// what guarantees the registry comes back online.
var (
	retryAlwaysRetryable = shared.StandardRetry()
	retryNeverRetryable  = shared.NoRetry()
)

// RegistryGC orchestrates the saga. The named return value `err` is what
// the deferred scale-back compensation inspects to decide whether to log
// the registry-down condition loudly. The deferred scale-back ALWAYS
// fires once we've successfully scaled down — it does not gate on `err`,
// because Nomad's scale endpoint is idempotent and re-issuing
// `count=1` when the job is already at 1 is a safe no-op on the happy
// path.
func RegistryGC(ctx workflow.Context, config RegistryGCConfig) (result RegistryGCResult, err error) {
	logger := workflow.GetLogger(ctx)
	config.ApplyDefaults()
	result.DryRun = config.DryRun
	logger.Info("Starting registry garbage-collect saga",
		"job_name", config.JobName,
		"group_name", config.GroupName,
		"data_dir", config.RegistryDataDir,
		"image", config.RegistryImage,
		"dry_run", config.DryRun,
		"delete_untagged", config.DeleteUntagged)

	// -----------------------------------------------------------------
	// Activity option presets
	// -----------------------------------------------------------------

	fastOpts := workflow.ActivityOptions{
		StartToCloseTimeout:    1 * time.Minute,
		ScheduleToCloseTimeout: 5 * time.Minute,
		RetryPolicy:            retryAlwaysRetryable,
	}
	pollOpts := workflow.ActivityOptions{
		StartToCloseTimeout:    5 * time.Minute,
		ScheduleToCloseTimeout: 10 * time.Minute,
		HeartbeatTimeout:       30 * time.Second,
		RetryPolicy:            retryAlwaysRetryable,
	}
	gcOpts := workflow.ActivityOptions{
		StartToCloseTimeout:    30 * time.Minute,
		ScheduleToCloseTimeout: 60 * time.Minute,
		HeartbeatTimeout:       2 * time.Minute,
		RetryPolicy:            retryNeverRetryable,
	}

	fastCtx := workflow.WithActivityOptions(ctx, fastOpts)
	pollCtx := workflow.WithActivityOptions(ctx, pollOpts)
	gcCtx := workflow.WithActivityOptions(ctx, gcOpts)

	// -----------------------------------------------------------------
	// Find the node hosting the registry
	// -----------------------------------------------------------------

	var node nodes.NodeInfo
	if err = workflow.ExecuteActivity(fastCtx, saga.FindJobNode, config.JobName).Get(ctx, &node); err != nil {
		return result, fmt.Errorf("find registry node: %w", err)
	}
	result.NodeName = node.Name
	result.NodeAddr = node.Address

	// -----------------------------------------------------------------
	// Measure registry data dir (before)
	// -----------------------------------------------------------------

	var beforeBytes int64
	if err = workflow.ExecuteActivity(fastCtx, saga.MeasureDataDir, node, config.RegistryDataDir).Get(ctx, &beforeBytes); err != nil {
		return result, fmt.Errorf("measure registry data dir (before): %w", err)
	}
	result.BeforeBytes = nodes.HumanBytes(beforeBytes)

	// -----------------------------------------------------------------
	// Scale registry down to 0
	// -----------------------------------------------------------------

	if err = workflow.ExecuteActivity(fastCtx, saga.ScaleJob, config.JobName, config.GroupName, 0).Get(ctx, nil); err != nil {
		// Scale-down failed; nothing to compensate. Return without
		// registering the deferred scale-back.
		return result, fmt.Errorf("scale registry to 0: %w", err)
	}

	// -----------------------------------------------------------------
	// Compensation: ALWAYS scale back to 1 (saga-style)
	//
	// Uses workflow.NewDisconnectedContext so this fires even when the
	// parent ctx has been cancelled (e.g. workflow timeout, parent
	// cancel). The named return value `err` is captured by the closure;
	// we don't gate on it because the scale-back must run in BOTH the
	// success and failure paths (registry is at count=0 either way
	// once the scale-down activity above succeeds).
	// -----------------------------------------------------------------

	defer func() {
		cleanupCtx, _ := workflow.NewDisconnectedContext(ctx)
		fastCleanupCtx := workflow.WithActivityOptions(cleanupCtx, fastOpts)
		pollCleanupCtx := workflow.WithActivityOptions(cleanupCtx, pollOpts)

		scaleErr := workflow.ExecuteActivity(fastCleanupCtx, saga.ScaleJob, config.JobName, config.GroupName, 1).
			Get(fastCleanupCtx, nil)
		if scaleErr != nil {
			logger.Error("CRITICAL: failed to scale registry back to 1; manual recovery required",
				"job", config.JobName, "error", scaleErr)
			err = errors.Join(err, fmt.Errorf("compensation scale-up failed: %w", scaleErr))
			return
		}

		waitErr := workflow.ExecuteActivity(pollCleanupCtx, saga.WaitJobRunning, config.JobName).
			Get(pollCleanupCtx, nil)
		if waitErr != nil {
			logger.Error("registry scaled but did not become running in time; check Nomad",
				"job", config.JobName, "error", waitErr)
			err = errors.Join(err, fmt.Errorf("compensation wait-running failed: %w", waitErr))
		}
	}()

	// -----------------------------------------------------------------
	// Wait for registry allocs to drain
	// -----------------------------------------------------------------

	if err = workflow.ExecuteActivity(pollCtx, saga.WaitJobDrained, config.JobName).Get(ctx, nil); err != nil {
		return result, fmt.Errorf("wait for registry allocs to drain: %w", err)
	}

	// -----------------------------------------------------------------
	// Run registry garbage-collect
	// -----------------------------------------------------------------

	var gcRun RegistryGCRunResult
	if err = workflow.ExecuteActivity(gcCtx, acts.RunRegistryGarbageCollect, node, config).Get(ctx, &gcRun); err != nil {
		return result, fmt.Errorf("run registry garbage-collect: %w", err)
	}
	result.BlobsDeleted = gcRun.BlobsDeleted

	// -----------------------------------------------------------------
	// Measure registry data dir (after)
	// -----------------------------------------------------------------

	var afterBytes int64
	if err = workflow.ExecuteActivity(fastCtx, saga.MeasureDataDir, node, config.RegistryDataDir).Get(ctx, &afterBytes); err != nil {
		return result, fmt.Errorf("measure registry data dir (after): %w", err)
	}
	result.AfterBytes = nodes.HumanBytes(afterBytes)
	if reclaimed := beforeBytes - afterBytes; reclaimed > 0 {
		result.BytesReclaimed = nodes.HumanBytes(reclaimed)
	} else {
		result.BytesReclaimed = "0B"
	}

	logger.Info("Registry garbage-collect complete",
		"node", result.NodeName,
		"blobs_deleted", result.BlobsDeleted,
		"bytes_reclaimed", result.BytesReclaimed,
		"before", result.BeforeBytes,
		"after", result.AfterBytes,
		"dry_run", result.DryRun)

	return result, nil
}
