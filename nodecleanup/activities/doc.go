// Package activities implements the Temporal activities for the cleanup
// worker, which hosts four independent infrastructure-maintenance operations
// on one task queue: orphaned Nomad data-directory removal over SSH/SFTP
// (with optional Docker pruning), container-registry GC, aptly repository
// cleanup, and PostgreSQL VACUUM maintenance. The registry and aptly
// operations run as sagas (scale a job down, do the work, scale back). All
// remote work uses native Go clients -- no remote shell.
package activities
