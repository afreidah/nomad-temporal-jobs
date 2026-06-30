// Package s3store is the shared S3 client for backup-style workloads:
// heartbeat-wrapped multipart upload, object listing, deletion, and
// oldest-object quota eviction. It reaches the AWS SDK through a small internal
// interface a fake satisfies in tests; each worker declares its own narrow
// interface over the subset of methods it uses.
package s3store
