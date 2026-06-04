// -------------------------------------------------------------------------------
// Postgres Maintenance Workflow - VACUUM (ANALYZE) per Database
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Lists the cluster's databases and runs VACUUM (ANALYZE) on each with bounded
// concurrency. A per-database failure is recorded and the run continues; the
// workflow returns an error if any database failed. Pure orchestration -- all
// I/O happens in activities.
// -------------------------------------------------------------------------------

package workflows

import (
	"errors"
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"munchbox/temporal-workers/nodecleanup/activities"
)

// PostgresMaintenance vacuums every database with bounded concurrency.
func PostgresMaintenance(ctx workflow.Context, config activities.PostgresMaintenanceConfig) (*activities.PostgresMaintenanceResult, error) {
	logger := workflow.GetLogger(ctx)
	config.ApplyDefaults()
	logger.Info("Starting postgres maintenance", "concurrency", config.Concurrency)

	retry := &temporal.RetryPolicy{
		InitialInterval:    time.Second,
		BackoffCoefficient: 2.0,
		MaximumInterval:    time.Minute,
		MaximumAttempts:    3,
	}
	quickCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout:    5 * time.Minute,
		ScheduleToCloseTimeout: 15 * time.Minute,
		RetryPolicy:            retry,
	})
	vacuumOpts := workflow.ActivityOptions{
		StartToCloseTimeout:    30 * time.Minute,
		ScheduleToCloseTimeout: 60 * time.Minute,
		RetryPolicy:            retry,
	}

	var dbs []string
	if err := workflow.ExecuteActivity(quickCtx, a.ListPostgresDatabases).Get(quickCtx, &dbs); err != nil {
		return nil, fmt.Errorf("list databases: %w", err)
	}
	logger.Info("Vacuuming databases", "count", len(dbs), "concurrency", config.Concurrency)

	result := &activities.PostgresMaintenanceResult{
		Databases: make([]activities.DatabaseMaintenance, len(dbs)),
	}
	errs := make([]error, len(dbs))

	sem := workflow.NewBufferedChannel(ctx, config.Concurrency)
	wg := workflow.NewWaitGroup(ctx)
	for i, dbname := range dbs {
		wg.Add(1)
		workflow.Go(ctx, func(gctx workflow.Context) {
			defer wg.Done()
			sem.Send(gctx, nil) // acquire a slot
			defer sem.Receive(gctx, nil)

			vctx := workflow.WithActivityOptions(gctx, vacuumOpts)
			entry := activities.DatabaseMaintenance{Database: dbname}
			if err := workflow.ExecuteActivity(vctx, a.VacuumAnalyzeDatabase, dbname).Get(vctx, nil); err != nil {
				logger.Warn("VACUUM failed", "database", dbname, "error", err)
				entry.Error = err.Error()
				errs[i] = fmt.Errorf("%q: %w", dbname, err)
			}
			result.Databases[i] = entry
		})
	}
	wg.Wait(ctx)

	if err := errors.Join(errs...); err != nil {
		result.Success = false
		return result, fmt.Errorf("one or more vacuums failed: %w", err)
	}

	result.Success = true
	logger.Info("Postgres maintenance complete", "databases", len(result.Databases))
	return result, nil
}
