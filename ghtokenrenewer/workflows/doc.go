// Package workflows implements RenewTokens: list the managed repos and renew
// each one's Actions CI-token secret with bounded concurrency, minting a fresh
// GitHub App token per repo. A per-repo failure is recorded and the run
// continues; the workflow returns an error if any repo failed. Pure
// orchestration -- all I/O happens in the activities package.
package workflows
