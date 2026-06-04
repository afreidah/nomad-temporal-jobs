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
	"fmt"

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

// pgConfig builds a shared.PostgresConfig pointed at the given database.
func (a *Activities) pgConfig(dbname string) shared.PostgresConfig {
	return shared.PostgresConfig{
		Host:        a.config.PostgresHost,
		Port:        a.config.PostgresPort,
		User:        a.config.PostgresUser,
		Password:    a.config.PostgresPassword,
		DBName:      dbname,
		SSLMode:     a.config.PostgresSSLMode,
		SSLRootCert: a.config.PostgresSSLRootCert,
	}
}

// ListPostgresDatabases returns the non-template databases that accept
// connections, queried from the primary.
func (a *Activities) ListPostgresDatabases(ctx context.Context) ([]string, error) {
	logger := activity.GetLogger(ctx)

	ctx, span := shared.StartClientSpan(ctx, "postgres.list_databases",
		shared.PeerServiceAttr("postgres-primary"),
	)
	defer span.End()

	db, err := shared.NewPostgresDB(a.pgConfig("postgres"))
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = db.Close() }()

	const query = `SELECT datname FROM pg_database WHERE datistemplate = false AND datallowconn = true ORDER BY datname`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list databases: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var dbs []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan database name: %w", err)
		}
		dbs = append(dbs, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate databases: %w", err)
	}

	logger.Info("Discovered PostgreSQL databases", "count", len(dbs))
	return dbs, nil
}

// VacuumAnalyzeDatabase runs VACUUM (ANALYZE) against one database to reclaim
// bloat and refresh planner statistics. Online and lock-light -- no FULL.
func (a *Activities) VacuumAnalyzeDatabase(ctx context.Context, dbname string) error {
	logger := activity.GetLogger(ctx)

	ctx, span := shared.StartClientSpan(ctx, "postgres.vacuum_analyze",
		shared.PeerServiceAttr("postgres-primary"),
	)
	defer span.End()

	db, err := shared.NewPostgresDB(a.pgConfig(dbname))
	if err != nil {
		return fmt.Errorf("connect to %q: %w", dbname, err)
	}
	defer func() { _ = db.Close() }()

	// VACUUM cannot run inside a transaction; ExecContext on the pool is autocommit.
	if _, err := db.ExecContext(ctx, "VACUUM (ANALYZE)"); err != nil {
		return fmt.Errorf("vacuum %q: %w", dbname, err)
	}

	logger.Info("VACUUM (ANALYZE) complete", "database", dbname)
	return nil
}
