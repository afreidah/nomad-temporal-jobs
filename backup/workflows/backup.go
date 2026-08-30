// -------------------------------------------------------------------------------
// Backup Workflow - Scheduled Infrastructure Backup Orchestration
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Snapshots Nomad Raft state, Consul Raft state (includes Vault), and
// PostgreSQL. The three legs are independent and run concurrently, joining
// before retention cleanup. The PostgreSQL leg dumps each database to its own
// file plus a globals dump for cluster-wide roles/grants, fanning the
// per-database dumps out with bounded concurrency. Pure orchestration --
// all I/O happens in activities.
// -------------------------------------------------------------------------------

package workflows

import (
	"errors"
	"fmt"
	"time"

	"go.temporal.io/sdk/workflow"

	"munchbox/temporal-workers/backup/activities"
	"munchbox/temporal-workers/shared"
)

// --- Nil-typed activity stub for compile-time method references ---
var a *activities.Activities

// quickOpts covers fast operations: snapshots, database listing, the globals
// dump, and retention cleanup. longOpts covers per-database pg_dump, which can
// run long for large databases and therefore heartbeats.
var (
	quickOpts = shared.QuickActivityOptions()
	longOpts  = shared.LongActivityOptions()
)

// uploadOpts covers S3 uploads. The largest dumps run hundreds of MB and take
// many minutes even over multipart, so uploads get a more generous total
// window than the long profile -- the quick profile was killing big uploads
// at the 5-minute StartToClose timeout.
var uploadOpts = workflow.ActivityOptions{
	StartToCloseTimeout:    30 * time.Minute,
	ScheduleToCloseTimeout: 90 * time.Minute,
	HeartbeatTimeout:       2 * time.Minute,
	RetryPolicy:            shared.StandardRetry(),
}

// Backup types name both the S3 prefix a leg uploads under and the value of
// the "backup" object tag, so retention rules in s3-orchestrator line up with
// the layout on disk.
const (
	backupTypeNomad    = "nomad"
	backupTypeConsul   = "consul"
	backupTypePostgres = "postgres"

	s3PrefixRoot     = "backups/"
	s3PrefixPostgres = s3PrefixRoot + backupTypePostgres
)

// Object tag keys. s3-orchestrator expires objects by tag, so these are what
// its lifecycle rules match on.
const (
	tagBackup   = "backup"
	tagDatabase = "database"
)

// backupTags returns the tags every upload of a given backup type carries.
func backupTags(backupType string) map[string]string {
	return map[string]string{tagBackup: backupType}
}

// Backup runs the three snapshot legs concurrently, then retention cleanup
// once they all finish.
func Backup(ctx workflow.Context, config activities.BackupConfig) (*activities.BackupResult, error) {
	logger := workflow.GetLogger(ctx)
	config.ApplyDefaults()
	logger.Info("Starting backup workflow",
		"local_days", config.LocalDays,
		"s3_days", config.S3Days,
		"dump_concurrency", config.DumpConcurrency)

	result := &activities.BackupResult{
		Timestamp: workflow.Now(ctx),
	}

	var nomadErr, consulErr, pgErr error
	wg := workflow.NewWaitGroup(ctx)
	wg.Add(3)

	workflow.Go(ctx, func(gctx workflow.Context) {
		defer wg.Done()
		nomadErr = snapshotLeg(gctx, a.TakeNomadSnapshot, backupTypeNomad, &result.NomadSnapshot, &result.NomadS3Key)
	})
	workflow.Go(ctx, func(gctx workflow.Context) {
		defer wg.Done()
		consulErr = snapshotLeg(gctx, a.TakeConsulSnapshot, backupTypeConsul, &result.ConsulSnapshot, &result.ConsulS3Key)
	})
	workflow.Go(ctx, func(gctx workflow.Context) {
		defer wg.Done()
		pgErr = postgresLeg(gctx, config, result)
	})

	wg.Wait(ctx)

	// Snapshot failures are fatal; uploads and cleanup are not.
	if err := errors.Join(nomadErr, consulErr, pgErr); err != nil {
		result.Error = err.Error()
		return result, err
	}

	quickCtx := workflow.WithActivityOptions(ctx, quickOpts)

	logger.Info("Cleaning up old local backups", "retention_days", config.LocalDays)
	if err := workflow.ExecuteActivity(quickCtx, a.CleanupOldBackups, config.LocalDays).Get(ctx, nil); err != nil {
		logger.Warn("Local cleanup failed", "error", err)
	}

	logger.Info("Cleaning up old S3 backups", "retention_days", config.S3Days)
	if err := workflow.ExecuteActivity(quickCtx, a.CleanupOldS3Backups, config.S3Days).Get(ctx, nil); err != nil {
		logger.Warn("S3 cleanup failed", "error", err)
	}

	result.Success = true
	logger.Info("Backup workflow complete",
		"nomad", result.NomadSnapshot,
		"consul", result.ConsulSnapshot,
		"postgres_globals", result.PostgresGlobals,
		"postgres_databases", len(result.PostgresDatabases))

	return result, nil
}

// snapshotLeg takes one snapshot and uploads it under the backup type's prefix,
// tagged with that type. The snapshot is fatal; the upload is non-fatal.
// pathOut/keyOut are written in place.
func snapshotLeg(ctx workflow.Context, snapshotFn any, backupType string, pathOut, keyOut *string) error {
	logger := workflow.GetLogger(ctx)
	cctx := workflow.WithActivityOptions(ctx, quickOpts)
	uctx := workflow.WithActivityOptions(ctx, uploadOpts)

	var path string
	if err := workflow.ExecuteActivity(cctx, snapshotFn).Get(cctx, &path); err != nil {
		return fmt.Errorf("%s snapshot: %w", backupType, err)
	}
	*pathOut = path

	s3Prefix := s3PrefixRoot + backupType
	upload := activities.UploadRequest{
		LocalPath: path,
		KeyPrefix: s3Prefix,
		Tags:      backupTags(backupType),
	}

	var key string
	if err := workflow.ExecuteActivity(uctx, a.UploadToS3, upload).Get(uctx, &key); err != nil {
		logger.Warn("S3 upload failed", "prefix", s3Prefix, "error", err)
	} else {
		*keyOut = key
	}
	return nil
}

// postgresLeg dumps cluster globals, lists the databases, and dumps + uploads
// each one with bounded concurrency. Globals and the listing are fatal;
// uploads are not; a database dump failure fails the leg after every database
// has been attempted.
func postgresLeg(ctx workflow.Context, config activities.BackupConfig, result *activities.BackupResult) error {
	logger := workflow.GetLogger(ctx)
	quickCtx := workflow.WithActivityOptions(ctx, quickOpts)
	uploadCtx := workflow.WithActivityOptions(ctx, uploadOpts)

	var globalsPath string
	if err := workflow.ExecuteActivity(quickCtx, a.BackupPostgresGlobals).Get(quickCtx, &globalsPath); err != nil {
		return fmt.Errorf("postgres globals: %w", err)
	}
	result.PostgresGlobals = globalsPath

	globalsUpload := activities.UploadRequest{
		LocalPath: globalsPath,
		KeyPrefix: s3PrefixPostgres,
		Tags:      backupTags(backupTypePostgres),
	}

	var globalsKey string
	if err := workflow.ExecuteActivity(uploadCtx, a.UploadToS3, globalsUpload).Get(uploadCtx, &globalsKey); err != nil {
		logger.Warn("Postgres globals S3 upload failed", "error", err)
	} else {
		result.PostgresGlobalsS3Key = globalsKey
	}

	var dbs []string
	if err := workflow.ExecuteActivity(quickCtx, a.ListPostgresDatabases).Get(quickCtx, &dbs); err != nil {
		return fmt.Errorf("list postgres databases: %w", err)
	}
	logger.Info("Backing up databases", "count", len(dbs), "concurrency", config.DumpConcurrency)

	backups := make([]activities.DatabaseBackup, len(dbs))
	dumpErrs := make([]error, len(dbs))

	sem := workflow.NewBufferedChannel(ctx, config.DumpConcurrency)
	inner := workflow.NewWaitGroup(ctx)
	for i, db := range dbs {
		inner.Add(1)
		workflow.Go(ctx, func(gctx workflow.Context) {
			defer inner.Done()
			sem.Send(gctx, nil) // acquire a slot
			defer sem.Receive(gctx, nil)

			lCtx := workflow.WithActivityOptions(gctx, longOpts)
			uCtx := workflow.WithActivityOptions(gctx, uploadOpts)

			entry := activities.DatabaseBackup{Database: db}
			var path string
			if err := workflow.ExecuteActivity(lCtx, a.BackupPostgresDatabase, db).Get(lCtx, &path); err != nil {
				dumpErrs[i] = fmt.Errorf("dump %q: %w", db, err)
				backups[i] = entry
				return
			}
			entry.LocalPath = path

			// Each database uploads under its own subdir: backups/postgres/<db>/,
			// and carries a database tag so it can have its own retention window.
			// The tag value is sanitized like the prefix, which also keeps it
			// inside the character set S3 allows in a tag value.
			safe := activities.SanitizeDBName(db)
			dbUpload := activities.UploadRequest{
				LocalPath: path,
				KeyPrefix: s3PrefixPostgres + "/" + safe,
				Tags:      map[string]string{tagBackup: backupTypePostgres, tagDatabase: safe},
			}

			var key string
			if err := workflow.ExecuteActivity(uCtx, a.UploadToS3, dbUpload).Get(uCtx, &key); err != nil {
				logger.Warn("Postgres database S3 upload failed", "database", db, "error", err)
			} else {
				entry.S3Key = key
			}
			backups[i] = entry
		})
	}
	inner.Wait(ctx)
	result.PostgresDatabases = backups

	if err := errors.Join(dumpErrs...); err != nil {
		return fmt.Errorf("one or more database dumps failed: %w", err)
	}
	return nil
}
