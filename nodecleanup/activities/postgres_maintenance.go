// -------------------------------------------------------------------------------
// Postgres Maintenance Activities - VACUUM (ANALYZE) per Database
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Enumerates the cluster's databases and runs an online VACUUM (ANALYZE) on
// each to reclaim bloat and refresh planner statistics. Connects through the
// shared instrumented Postgres client; one connection per database since
// VACUUM operates on the database it is connected to.
// -------------------------------------------------------------------------------

package activities

import (
	"context"

	"go.temporal.io/sdk/activity"

	"munchbox/temporal-workers/shared"
)

// PostgresMaintenanceConfig is the workflow input.
type PostgresMaintenanceConfig struct {
	// Concurrency bounds how many databases are vacuumed in parallel so the
	// maintenance burst doesn't overwhelm the primary. Default 2.
	Concurrency int `json:"concurrency"`
}

// ApplyDefaults fills any unset field with its fleet-wide default.
func (c *PostgresMaintenanceConfig) ApplyDefaults() {
	if c.Concurrency <= 0 {
		c.Concurrency = 2
	}
}

// DatabaseMaintenance records the VACUUM outcome for one database.
type DatabaseMaintenance struct {
	Database string `json:"database"`
	Error    string `json:"error,omitempty"`
}

// PostgresMaintenanceResult summarizes a maintenance run.
type PostgresMaintenanceResult struct {
	Databases []DatabaseMaintenance `json:"databases"`
	Success   bool                  `json:"success"`
}

// ListPostgresDatabases returns the non-template databases that accept
// connections, queried from the primary.
func (a *Activities) ListPostgresDatabases(ctx context.Context) ([]string, error) {
	logger := activity.GetLogger(ctx)

	ctx, span := shared.StartPeerSpan(ctx, "postgres-primary", "postgres.list_databases")
	defer span.End()

	dbs, err := a.pg.ListDatabases(ctx)
	if err != nil {
		return nil, err
	}

	logger.Info("Discovered PostgreSQL databases", "count", len(dbs))
	return dbs, nil
}

// VacuumAnalyzeDatabase runs VACUUM (ANALYZE) against one database to reclaim
// bloat and refresh planner statistics. Online and lock-light -- no FULL.
func (a *Activities) VacuumAnalyzeDatabase(ctx context.Context, dbname string) error {
	logger := activity.GetLogger(ctx)

	ctx, span := shared.StartPeerSpan(ctx, "postgres-primary", "postgres.vacuum_analyze")
	defer span.End()

	if err := a.pg.VacuumAnalyze(ctx, dbname); err != nil {
		return err
	}

	logger.Info("VACUUM (ANALYZE) complete", "database", dbname)
	return nil
}
