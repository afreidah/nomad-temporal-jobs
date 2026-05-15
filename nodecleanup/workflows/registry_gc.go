// -------------------------------------------------------------------------------
// Registry GC Workflow - Docker Registry Garbage-Collection Orchestration
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Single-step workflow that runs the registry garbage-collect activity
// against the cluster's container registry. Quiesces the registry job to
// 0 replicas for the duration of GC so concurrent pushes can't drop blob
// references. Runs on the same cleanup-task-queue / cleanup-worker as the
// node-cleanup workflow — they share SSH infra and Nomad client setup.
// -------------------------------------------------------------------------------

package workflows

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"munchbox/temporal-workers/nodecleanup/activities"
)

// RegistryGC orchestrates a single registry garbage-collection run. The
// activity does the heavy lifting (Nomad scale, SSH, docker run); the
// workflow's only job is to set sensible timeouts and surface the result.
func RegistryGC(ctx workflow.Context, config activities.RegistryGCConfig) (activities.RegistryGCResult, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("Starting registry garbage-collect workflow",
		"job_name", config.JobName,
		"data_dir", config.RegistryDataDir,
		"image", config.RegistryImage,
		"dry_run", config.DryRun,
		"delete_untagged", config.DeleteUntagged)

	// GC of a multi-GB registry can take 10+ minutes; pad the activity
	// timeouts well past that. The Nomad scale-down + scale-up adds
	// another ~30–60 s on top, but stays well inside this budget.
	ao := workflow.ActivityOptions{
		StartToCloseTimeout:    30 * time.Minute,
		ScheduleToCloseTimeout: 60 * time.Minute,
		HeartbeatTimeout:       0,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    5 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    time.Minute,
			// Conservative: GC failures usually need human eyes (e.g.
			// "registry didn't come back up"), not blind retries.
			MaximumAttempts: 2,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	var result activities.RegistryGCResult
	if err := workflow.ExecuteActivity(ctx, a.RegistryGarbageCollect, config).Get(ctx, &result); err != nil {
		return result, fmt.Errorf("registry garbage-collect activity failed: %w", err)
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
