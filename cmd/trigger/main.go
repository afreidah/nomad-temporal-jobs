// -------------------------------------------------------------------------------
// Workflow Trigger - Temporal Workflow Dispatcher
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Short-lived process that starts a Temporal workflow and waits for completion.
// Invoked by Nomad periodic batch jobs to trigger backup, trivy scan, or
// cleanup workflows on schedule. Initializes OTel tracing so the workflow
// start appears as a root span in the trace chain.
// -------------------------------------------------------------------------------

package main

import (
	"context"
	"log"
	"os"
	"time"

	backupact "munchbox/temporal-workers/backup/activities"
	backupwf "munchbox/temporal-workers/backup/workflows"
	cleanupact "munchbox/temporal-workers/nodecleanup/activities"
	cleanupwf "munchbox/temporal-workers/nodecleanup/workflows"
	"munchbox/temporal-workers/shared"
	trivywf "munchbox/temporal-workers/trivyscan/workflows"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
)

func main() {
	ctx := context.Background()

	workflowName := envOrDefault("WORKFLOW_NAME", "backup")

	// --- Tracing ---
	shutdownTracer := shared.InitTracer(ctx, "temporal-trigger-"+workflowName)
	defer func() { _ = shutdownTracer(ctx) }()

	// --- Temporal client ---
	temporalAddr := envOrDefault("TEMPORAL_ADDRESS", "localhost:7233")

	tracingInterceptor, err := opentelemetry.NewTracingInterceptor(opentelemetry.TracerOptions{})
	if err != nil {
		log.Printf("Warning: failed to create tracing interceptor: %v", err)
	}

	clientOpts := client.Options{HostPort: temporalAddr}
	if tracingInterceptor != nil {
		clientOpts.Interceptors = []interceptor.ClientInterceptor{tracingInterceptor}
	}

	c, err := client.Dial(clientOpts)
	if err != nil {
		log.Fatalln("Unable to create Temporal client:", err)
	}
	defer c.Close()

	workflowID := workflowName + "-" + time.Now().Format("2006-01-02-15-04-05")

	switch workflowName {
	case "backup":
		runBackup(ctx, c, workflowID)
	case "trivy":
		runTrivy(ctx, c, workflowID)
	case "cleanup":
		runCleanup(ctx, c, workflowID)
	default:
		log.Fatalf("Unknown workflow: %s (supported: backup, trivy, cleanup)\n", workflowName)
	}
}

// runBackup starts the backup workflow with retention config and waits for
// completion.
func runBackup(ctx context.Context, c client.Client, workflowID string) {
	retention := backupact.RetentionConfig{
		LocalDays: envInt("LOCAL_RETENTION_DAYS", 7),
		S3Days:    envInt("S3_RETENTION_DAYS", 30),
	}

	we, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: "backup-task-queue",
	}, backupwf.Backup, retention)
	if err != nil {
		log.Fatalln("Unable to start backup workflow:", err)
	}

	log.Printf("Started backup workflow: %s (RunID: %s)", we.GetID(), we.GetRunID())

	var result backupact.BackupResult
	if err := we.Get(ctx, &result); err != nil {
		log.Fatalf("Backup workflow failed: %v\n", err)
	}

	log.Println("Backup complete!")
	log.Printf("  Nomad:    %s", result.NomadSnapshot)
	log.Printf("  Consul:   %s", result.ConsulSnapshot)
	log.Printf("  Postgres: %s", result.PostgresBackup)
	log.Printf("  Registry: %s", result.RegistryBackup)
}

// runTrivy starts the trivy scan workflow and waits for completion.
func runTrivy(ctx context.Context, c client.Client, workflowID string) {
	we, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: "trivy-task-queue",
	}, trivywf.Scan)
	if err != nil {
		log.Fatalln("Unable to start trivy scan workflow:", err)
	}

	log.Printf("Started trivy scan workflow: %s (RunID: %s)", we.GetID(), we.GetRunID())

	if err := we.Get(ctx, nil); err != nil {
		log.Fatalf("Trivy scan workflow failed: %v\n", err)
	}

	log.Println("Trivy scan complete!")
}

// runCleanup starts the node cleanup workflow and waits for completion.
func runCleanup(ctx context.Context, c client.Client, workflowID string) {
	config := cleanupact.CleanupConfig{
		DataDir:     envOrDefault("CLEANUP_DATA_DIR", "/opt/nomad/data"),
		GraceDays:   envInt("GRACE_DAYS", 7),
		DryRun:      envOrDefault("DRY_RUN", "true") != "false",
		DockerPrune: envOrDefault("DOCKER_PRUNE", "false") == "true",
	}

	we, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: "cleanup-task-queue",
	}, cleanupwf.Cleanup, config)
	if err != nil {
		log.Fatalln("Unable to start cleanup workflow:", err)
	}

	log.Printf("Started cleanup workflow: %s (RunID: %s)", we.GetID(), we.GetRunID())
	log.Printf("Config: DataDir=%s, GraceDays=%d, DryRun=%v, DockerPrune=%v",
		config.DataDir, config.GraceDays, config.DryRun, config.DockerPrune)

	var results []cleanupact.CleanupResult
	if err := we.Get(ctx, &results); err != nil {
		log.Fatalf("Cleanup workflow failed: %v\n", err)
	}

	log.Println("Cleanup complete!")
	for _, r := range results {
		log.Printf("  Node %s: scanned=%d, orphaned=%d, deleted=%d, skipped=%d, docker_freed=%s",
			r.NodeName, r.Scanned, r.Orphaned, r.Deleted, r.Skipped, r.DockerSpaceFreed)
		if len(r.Errors) > 0 {
			log.Printf("    Errors: %v", r.Errors)
		}
	}
}

// envOrDefault reads an environment variable, returning fallback if unset
// or empty.
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envInt reads an integer environment variable, returning fallback if unset,
// empty, or not a valid positive integer.
func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return fallback
		}
		n = n*10 + int(c-'0')
	}
	if n <= 0 {
		return fallback
	}
	return n
}
