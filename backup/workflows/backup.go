// -------------------------------------------------------------------------------
// Backup Workflow - Scheduled Infrastructure Backup Orchestration
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Orchestrates sequential snapshots of Nomad, Consul (includes Vault),
// PostgreSQL, and the container registry. Each snapshot is uploaded to S3
// for off-site redundancy, followed by retention-based cleanup of both
// local and S3 backups. Pure orchestration logic -- all I/O happens in
// activities.
// -------------------------------------------------------------------------------

package workflows

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"munchbox/temporal-workers/backup/activities"
)

// --- Nil-typed activity stub for compile-time method references ---
var a *activities.Activities

// Backup orchestrates the full infrastructure backup sequence. Snapshots
// execute sequentially since each depends on cluster stability; S3 uploads
// are non-fatal. Retention config is passed as workflow input to avoid
// non-deterministic environment reads during replay.
func Backup(ctx workflow.Context, retention activities.RetentionConfig) (*activities.BackupResult, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("Starting backup workflow")

	// --- Apply retention defaults ---
	if retention.LocalDays <= 0 {
		retention.LocalDays = 7
	}
	if retention.S3Days <= 0 {
		retention.S3Days = 30
	}

	// --- Activity options for quick snapshots (Nomad, Consul) ---
	quickOpts := workflow.ActivityOptions{
		StartToCloseTimeout:    5 * time.Minute,
		ScheduleToCloseTimeout: 15 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    3,
		},
	}

	// --- Activity options for large backups (PostgreSQL, Registry) ---
	longOpts := workflow.ActivityOptions{
		StartToCloseTimeout:    30 * time.Minute,
		ScheduleToCloseTimeout: 60 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    3,
		},
	}

	quickCtx := workflow.WithActivityOptions(ctx, quickOpts)
	longCtx := workflow.WithActivityOptions(ctx, longOpts)

	result := &activities.BackupResult{
		Timestamp: workflow.Now(ctx),
	}

	// --- Nomad snapshot ---
	logger.Info("Taking Nomad snapshot")
	var nomadPath string
	if err := workflow.ExecuteActivity(quickCtx, a.TakeNomadSnapshot).Get(ctx, &nomadPath); err != nil {
		result.Error = "Nomad backup failed: " + err.Error()
		return result, err
	}
	result.NomadSnapshot = nomadPath

	// S3 upload (non-fatal)
	var nomadS3Key string
	if err := workflow.ExecuteActivity(quickCtx, a.UploadToS3, nomadPath, "backups/nomad").Get(ctx, &nomadS3Key); err != nil {
		logger.Warn("Nomad S3 upload failed", "error", err)
	} else {
		result.NomadS3Key = nomadS3Key
	}

	// --- Consul snapshot (includes Vault data) ---
	logger.Info("Taking Consul snapshot")
	var consulPath string
	if err := workflow.ExecuteActivity(quickCtx, a.TakeConsulSnapshot).Get(ctx, &consulPath); err != nil {
		result.Error = "Consul backup failed: " + err.Error()
		return result, err
	}
	result.ConsulSnapshot = consulPath

	var consulS3Key string
	if err := workflow.ExecuteActivity(quickCtx, a.UploadToS3, consulPath, "backups/consul").Get(ctx, &consulS3Key); err != nil {
		logger.Warn("Consul S3 upload failed", "error", err)
	} else {
		result.ConsulS3Key = consulS3Key
	}

	// --- PostgreSQL backup ---
	logger.Info("Taking PostgreSQL backup")
	var pgPath string
	if err := workflow.ExecuteActivity(longCtx, a.TakePostgresBackup).Get(ctx, &pgPath); err != nil {
		result.Error = "PostgreSQL backup failed: " + err.Error()
		return result, err
	}
	result.PostgresBackup = pgPath

	var pgS3Key string
	if err := workflow.ExecuteActivity(longCtx, a.UploadToS3, pgPath, "backups/postgres").Get(ctx, &pgS3Key); err != nil {
		logger.Warn("PostgreSQL S3 upload failed", "error", err)
	} else {
		result.PostgresS3Key = pgS3Key
	}

	// Registry backup disabled -- 48GB of Docker layers, too large for
	// local storage and S3. Container images are already stored in the
	// registry and can be rebuilt from source.

	// --- Cleanup old local backups ---
	logger.Info("Cleaning up old local backups", "retention_days", retention.LocalDays)
	if err := workflow.ExecuteActivity(quickCtx, a.CleanupOldBackups, retention.LocalDays).Get(ctx, nil); err != nil {
		logger.Warn("Local cleanup failed", "error", err)
	}

	// --- Cleanup old S3 backups ---
	logger.Info("Cleaning up old S3 backups", "retention_days", retention.S3Days)
	if err := workflow.ExecuteActivity(quickCtx, a.CleanupOldS3Backups, retention.S3Days).Get(ctx, nil); err != nil {
		logger.Warn("S3 cleanup failed", "error", err)
	}

	result.Success = true
	logger.Info("Backup workflow complete",
		"nomad", result.NomadSnapshot,
		"consul", result.ConsulSnapshot,
		"postgres", result.PostgresBackup,
		"registry", result.RegistryBackup)

	return result, nil
}

// formatErr builds a prefixed error message for the BackupResult.Error field.
func formatErr(prefix string, err error) string {
	return fmt.Sprintf("%s: %s", prefix, err.Error())
}
