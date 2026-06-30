// -------------------------------------------------------------------------------
// Shared Postgres Client - Instrumented PostgreSQL Connection Factory
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Opens OTel-instrumented PostgreSQL connection pools so queries appear as
// edges in the Tempo service graph. Centralizes the connection string, TLS
// settings, and span attributes so every worker connects the same way.
// -------------------------------------------------------------------------------

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/XSAM/otelsql"
	// pq registers the "postgres" driver with database/sql for its side effects;
	// it is never referenced directly, only by driver name in sql.Open.
	_ "github.com/lib/pq"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// PostgresConfig holds connection settings for a PostgreSQL client.
type PostgresConfig struct {
	Host        string
	Port        string
	User        string
	Password    string
	DBName      string
	SSLMode     string
	SSLRootCert string
}

// NewPostgresDB opens an OTel-instrumented connection pool and verifies it
// with a ping. The caller owns the returned pool and must Close it.
func NewPostgresDB(cfg PostgresConfig) (*sql.DB, error) {
	connStr := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.DBName, cfg.SSLMode)
	if cfg.SSLRootCert != "" {
		connStr += " sslrootcert=" + cfg.SSLRootCert
	}

	port := 5432
	if p, err := strconv.Atoi(cfg.Port); err == nil && p > 0 {
		port = p
	}

	db, err := otelsql.Open("postgres", connStr,
		otelsql.WithAttributes(
			semconv.DBSystemPostgreSQL,
			semconv.DBNamespace(cfg.DBName),
			semconv.ServerAddress(cfg.Host),
			semconv.ServerPort(port),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return db, nil
}

// Postgres provides cluster maintenance operations -- database enumeration and
// VACUUM -- against a PostgreSQL cluster. Each call opens a short-lived
// instrumented pool (listing connects to the maintenance database "postgres";
// VACUUM connects to its target database). Workers consume it through their own
// narrow interfaces. For a long-lived application pool with bespoke queries,
// use NewPostgresDB directly.
type Postgres struct {
	cfg PostgresConfig // DBName is overridden per operation
}

// NewPostgres returns a Postgres maintenance client. cfg.DBName is ignored --
// each operation selects the database it needs.
func NewPostgres(cfg PostgresConfig) *Postgres {
	return &Postgres{cfg: cfg}
}

// ListDatabases returns the non-template, connectable databases ordered by
// name, queried from the maintenance database.
func (p *Postgres) ListDatabases(ctx context.Context) ([]string, error) {
	return ListDatabaseNames(ctx, p.withDB("postgres"))
}

// VacuumAnalyze runs an online VACUUM (ANALYZE) against dbname to reclaim bloat
// and refresh planner statistics. No FULL, so it stays lock-light.
func (p *Postgres) VacuumAnalyze(ctx context.Context, dbname string) error {
	db, err := NewPostgresDB(p.withDB(dbname))
	if err != nil {
		return fmt.Errorf("connect to %q: %w", dbname, err)
	}
	defer func() { _ = db.Close() }()

	// VACUUM cannot run inside a transaction; ExecContext on the pool is autocommit.
	if _, err := db.ExecContext(ctx, "VACUUM (ANALYZE)"); err != nil {
		return fmt.Errorf("vacuum %q: %w", dbname, err)
	}
	return nil
}

// withDB returns a copy of the base config pointed at dbname.
func (p *Postgres) withDB(dbname string) PostgresConfig {
	c := p.cfg
	c.DBName = dbname
	return c
}

// ListDatabaseNames returns the non-template, connectable databases in the
// cluster, ordered by name. It opens a short-lived pool from cfg (which should
// point at a maintenance database such as "postgres") and closes it before
// returning. Retained as the primitive behind Postgres.ListDatabases and for
// callers that build a one-off config.
func ListDatabaseNames(ctx context.Context, cfg PostgresConfig) ([]string, error) {
	db, err := NewPostgresDB(cfg)
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
	return dbs, nil
}
