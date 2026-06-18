// Package activities implements the Temporal activities for the backup
// worker: Nomad and Consul Raft snapshots and PostgreSQL dumps (the latter
// gzipped in-process), uploads to S3-compatible storage, and retention
// cleanup. Snapshots are taken through native Go APIs; only pg_dump and
// pg_dumpall, which have no Go-native equivalent, are invoked as subprocesses.
package activities
