// Package workflows implements the runner-scaler workflows. PollAndDispatch is
// the scheduled parent: each tick it reads the watched repos and runner
// profiles, lists each repo's queued self-hosted jobs, and starts one
// HandleQueuedJob child per job. The children are keyed runner-<repo>-<job_id>
// with a reject-duplicate ID policy, so Temporal itself guarantees one runner
// per job without an external state store -- a job still queued on the next tick
// can't spawn a second runner. HandleQueuedJob dispatches the ephemeral runner
// and, via a backstop timer, reaps a runner that never picked its job up. All
// I/O lives in activities; the workflows are pure orchestration.
package workflows
