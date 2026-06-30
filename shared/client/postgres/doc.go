// Package postgres is a PostgreSQL client for the maintenance workers. It opens
// pooled database/sql connections (optionally TLS) and exposes the operations
// workers compose -- enumerating databases and running per-database
// VACUUM (ANALYZE).
package postgres
