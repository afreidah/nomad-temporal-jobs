// -------------------------------------------------------------------------------
// Shared Postgres Client - Instrumented PostgreSQL Connection Factory
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Opens OTel-instrumented PostgreSQL connection pools so queries appear as
// edges in the Tempo service graph. Centralizes the connection string, TLS
// settings, and span attributes so every worker connects the same way.
// -------------------------------------------------------------------------------

package shared

import (
	"database/sql"
	"fmt"
	"strconv"

	"github.com/XSAM/otelsql"
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
