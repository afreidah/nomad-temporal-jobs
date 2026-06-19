// -------------------------------------------------------------------------------
// Aptly Cleanup Workflow - Saga-Style Pool Cleanup
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Scales the aptly job to 0 so the server releases its single-writer leveldb
// lock, runs `aptly db cleanup` in a one-shot container against the pool
// volume, then scales back to 1. The scale-back is deferred with a
// disconnected context so it always fires -- even if cleanup fails, an
// activity times out, or the workflow is cancelled -- so aptly is never
// stranded at count=0. Shares the find/scale/wait/measure saga activities
// (from the shared nodes package) and retry policies with the registry-GC saga.
// -------------------------------------------------------------------------------

package aptlycleanup

import (
	"errors"
	"fmt"
	"time"

	"go.temporal.io/sdk/workflow"

	"munchbox/temporal-workers/maintenance/internal/nodes"
	"munchbox/temporal-workers/shared"
)

// Nil-typed activity stubs for compile-time method references: saga steps come
// from the shared nodes package, the cleanup step from this package's Activities.
var (
	saga *nodes.SagaActivities
	acts *Activities
)

// retryAlwaysRetryable is the standard Nomad-API-blip retry policy for the
// short, idempotent activities. retryNeverRetryable is used by the long-running
// cleanup activity: a partial cleanup shouldn't be retried — the deferred
// scale-back is what guarantees aptly comes back online.
var (
	retryAlwaysRetryable = shared.StandardRetry()
	retryNeverRetryable  = shared.NoRetry()
)

// AptlyCleanup orchestrates the scale-down / cleanup / scale-up saga.
func AptlyCleanup(ctx workflow.Context, config AptlyCleanupConfig) (result *AptlyCleanupResult, err error) {
	logger := workflow.GetLogger(ctx)
	config.ApplyDefaults()
	result = &AptlyCleanupResult{}
	logger.Info("Starting aptly cleanup saga",
		"job", config.JobName, "group", config.GroupName, "image", config.Image, "data_dir", config.DataDir)

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
	cleanupOpts := workflow.ActivityOptions{
		StartToCloseTimeout:    20 * time.Minute,
		ScheduleToCloseTimeout: 40 * time.Minute,
		HeartbeatTimeout:       2 * time.Minute,
		RetryPolicy:            retryNeverRetryable,
	}

	fastCtx := workflow.WithActivityOptions(ctx, fastOpts)
	pollCtx := workflow.WithActivityOptions(ctx, pollOpts)
	cleanupCtx := workflow.WithActivityOptions(ctx, cleanupOpts)

	// --- Find the node hosting aptly ---
	var node nodes.NodeInfo
	if err = workflow.ExecuteActivity(fastCtx, saga.FindJobNode, config.JobName).Get(ctx, &node); err != nil {
		return result, fmt.Errorf("find aptly node: %w", err)
	}
	result.Node = node.Name

	// --- Measure pool (before) ---
	var beforeBytes int64
	if err = workflow.ExecuteActivity(fastCtx, saga.MeasureDataDir, node, config.DataDir).Get(ctx, &beforeBytes); err != nil {
		return result, fmt.Errorf("measure aptly data dir (before): %w", err)
	}
	result.BeforeBytes = nodes.HumanBytes(beforeBytes)

	// --- Scale aptly down to 0 ---
	if err = workflow.ExecuteActivity(fastCtx, saga.ScaleJob, config.JobName, config.GroupName, 0).Get(ctx, nil); err != nil {
		return result, fmt.Errorf("scale aptly to 0: %w", err)
	}

	// --- Compensation: ALWAYS scale back to 1 ---
	defer func() {
		cleanCtx, _ := workflow.NewDisconnectedContext(ctx)
		fastCleanCtx := workflow.WithActivityOptions(cleanCtx, fastOpts)
		pollCleanCtx := workflow.WithActivityOptions(cleanCtx, pollOpts)

		scaleErr := workflow.ExecuteActivity(fastCleanCtx, saga.ScaleJob, config.JobName, config.GroupName, 1).Get(fastCleanCtx, nil)
		if scaleErr != nil {
			logger.Error("CRITICAL: failed to scale aptly back to 1; manual recovery required",
				"job", config.JobName, "error", scaleErr)
			err = errors.Join(err, fmt.Errorf("compensation scale-up failed: %w", scaleErr))
			return
		}
		if waitErr := workflow.ExecuteActivity(pollCleanCtx, saga.WaitJobRunning, config.JobName).Get(pollCleanCtx, nil); waitErr != nil {
			logger.Error("aptly scaled but did not become running in time; check Nomad",
				"job", config.JobName, "error", waitErr)
			err = errors.Join(err, fmt.Errorf("compensation wait-running failed: %w", waitErr))
		}
	}()

	// --- Wait for aptly allocs to drain ---
	if err = workflow.ExecuteActivity(pollCtx, saga.WaitJobDrained, config.JobName).Get(ctx, nil); err != nil {
		return result, fmt.Errorf("wait for aptly allocs to drain: %w", err)
	}

	// --- Run the one-shot db cleanup ---
	var output string
	if err = workflow.ExecuteActivity(cleanupCtx, acts.RunAptlyDBCleanup, node, config.Image, config.DataDir).Get(ctx, &output); err != nil {
		return result, fmt.Errorf("aptly db cleanup: %w", err)
	}
	result.Output = output

	// --- Measure pool (after) ---
	var afterBytes int64
	if err = workflow.ExecuteActivity(fastCtx, saga.MeasureDataDir, node, config.DataDir).Get(ctx, &afterBytes); err != nil {
		return result, fmt.Errorf("measure aptly data dir (after): %w", err)
	}
	result.AfterBytes = nodes.HumanBytes(afterBytes)
	if reclaimed := beforeBytes - afterBytes; reclaimed > 0 {
		result.BytesReclaimed = nodes.HumanBytes(reclaimed)
	} else {
		result.BytesReclaimed = "0B"
	}

	logger.Info("Aptly cleanup complete",
		"node", result.Node, "before", result.BeforeBytes, "after", result.AfterBytes, "reclaimed", result.BytesReclaimed)
	return result, nil
}
