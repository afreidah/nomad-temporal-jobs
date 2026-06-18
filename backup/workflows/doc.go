// Package workflows holds the backup orchestration: the Nomad, Consul (which
// includes Vault), and PostgreSQL legs run concurrently and join before
// retention cleanup, with the per-database dumps fanned out under bounded
// concurrency. Pure orchestration -- all I/O happens in activities.
package workflows
