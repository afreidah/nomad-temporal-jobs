// -------------------------------------------------------------------------------
// Node Cleanup Workflow - Orphaned Data Directory Removal
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Orchestrates cleanup of orphaned Nomad job data directories across all
// client nodes. Discovers nodes via the Nomad API, then SSHes to each one
// to identify and optionally remove directories that no longer correspond
// to running allocations. Pure orchestration logic -- all I/O happens in
// activities.
// -------------------------------------------------------------------------------

package nodecleanup

import (
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/workflow"

	"munchbox/temporal-workers/maintenance/internal/nodes"
	"munchbox/temporal-workers/shared"
)

// --- Nil-typed activity stub for compile-time method references ---
var a *Activities

// Cleanup discovers all Nomad client nodes and performs orphaned data
// directory removal on each via SSH. Nodes are processed sequentially to
// avoid overwhelming the cluster. Returns results for all nodes and an
// error if any node fails.
func Cleanup(ctx workflow.Context, config CleanupConfig) ([]CleanupResult, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("Starting orphaned data cleanup workflow",
		"dataDir", config.DataDir,
		"graceDays", config.GraceDays,
		"dryRun", config.DryRun,
		"dockerPrune", config.DockerPrune)

	// Apply defaults
	if config.DataDir == "" {
		config.DataDir = "/opt/nomad/data"
	}
	if config.GraceDays == 0 {
		config.GraceDays = 7
	}

	ao := workflow.ActivityOptions{
		StartToCloseTimeout:    10 * time.Minute,
		ScheduleToCloseTimeout: 30 * time.Minute,
		RetryPolicy:            shared.StandardRetry(),
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	// CleanupNodeViaSSH heartbeats per directory, so a HeartbeatTimeout catches a
	// hung SSH/SFTP/Docker call instead of waiting out the full StartToClose.
	cleanupCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout:    10 * time.Minute,
		ScheduleToCloseTimeout: 30 * time.Minute,
		HeartbeatTimeout:       2 * time.Minute,
		RetryPolicy:            shared.StandardRetry(),
	})

	// --- Discover nodes ---
	var clientNodes []nodes.NodeInfo
	if err := workflow.ExecuteActivity(ctx, a.GetAllNomadClientNodes).Get(ctx, &clientNodes); err != nil {
		return nil, fmt.Errorf("failed to get Nomad nodes: %w", err)
	}
	logger.Info("Found Nomad client nodes", "count", len(clientNodes))

	// --- Clean each node sequentially ---
	var results []CleanupResult
	var failedNodes []string

	for _, node := range clientNodes {
		logger.Info("Cleaning up node", "node", node.Name, "address", node.Address)

		var result CleanupResult
		err := workflow.ExecuteActivity(cleanupCtx, a.CleanupNodeViaSSH, node, config).Get(ctx, &result)
		if err != nil {
			logger.Error("Failed to cleanup node", "node", node.Name, "error", err)
			result = CleanupResult{
				NodeName: node.Name,
				NodeAddr: node.Address,
				Errors:   []string{err.Error()},
			}
			failedNodes = append(failedNodes, node.Name)
		} else if len(result.Errors) > 0 {
			failedNodes = append(failedNodes, node.Name)
		}
		results = append(results, result)
	}

	// --- Summary ---
	totalOrphaned := 0
	totalDeleted := 0
	for _, r := range results {
		totalOrphaned += r.Orphaned
		totalDeleted += r.Deleted
	}

	logger.Info("Cleanup workflow complete",
		"nodes", len(results),
		"totalOrphaned", totalOrphaned,
		"totalDeleted", totalDeleted,
		"failedNodes", len(failedNodes),
		"dryRun", config.DryRun)

	if len(failedNodes) > 0 {
		return results, fmt.Errorf("cleanup failed on %d node(s): %s", len(failedNodes), strings.Join(failedNodes, ", "))
	}

	return results, nil
}
