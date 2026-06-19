// Package aptlycleanup implements the saga-style aptly repository cleanup
// workflow and its aptly-specific activity. The generic find / scale / wait /
// measure saga steps it orchestrates live in the shared nodes package; this
// package adds the one-shot `aptly db cleanup` run and the config/result types.
// The saga scales aptly offline so it releases the single-writer leveldb lock,
// runs the cleanup, and always scales it back via deferred compensation.
package aptlycleanup
