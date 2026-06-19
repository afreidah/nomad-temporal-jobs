// Package postgresmaint implements the PostgreSQL maintenance workflow and its
// activities: enumerate the cluster's databases and run an online
// VACUUM (ANALYZE) on each with bounded concurrency, through the shared
// instrumented Postgres client. A per-database failure is recorded and the run
// continues; the workflow returns an error if any database failed.
package postgresmaint
