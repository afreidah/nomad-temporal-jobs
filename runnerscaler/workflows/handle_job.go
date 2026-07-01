// -------------------------------------------------------------------------------
// Runner Scaler Child Workflow - Dispatch and Reap One Ephemeral Runner
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// HandleRunner dispatches one ephemeral ci-runner for a (repo, labels) bucket,
// waits for that runner's alloc to go terminal, then reaps it -- so the child
// completes (and the dead dispatched job is purged) promptly after the CI job
// finishes rather than lingering for the full backstop. reapAfter is now the
// wait's ceiling: a runner that wedges or never claims a job times the wait out
// and is reaped anyway. The dispatch runs under NoRetry: it creates a new runner
// each call, so a retried dispatch would double up. The runner is NOT bound to a
// specific job_id -- it takes whichever matching job is queued -- so the parent
// poller decides how many of these to start by reconciling queued depth against
// active runners, not by keying one child per job.
// -------------------------------------------------------------------------------

package workflows

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/workflow"

	"munchbox/temporal-workers/runnerscaler/activities"
	"munchbox/temporal-workers/shared"
)

// defaultReapAfter is the backstop ceiling on the terminal-wait: an upper bound
// on a single CI job, after which a still-running runner is assumed wedged and is
// reaped. On the happy path the wait returns as soon as the runner finishes, well
// before this; it must exceed the longest legitimate job so a busy runner is
// never killed mid-build.
const defaultReapAfter = time.Hour

// RunnerSpec is the child input: the repo the runner serves, the labels to
// register it with, the profile image (empty => the Nomad job default), and an
// optional reap override (0 => defaultReapAfter).
type RunnerSpec struct {
	Repo      string        `json:"repo"`
	Labels    []string      `json:"labels"`
	Image     string        `json:"image,omitempty"`
	ReapAfter time.Duration `json:"reap_after,omitempty"`
}

// HandleRunner dispatches one ephemeral runner for spec's (repo, labels) and
// reaps it after the backstop deadline.
func HandleRunner(ctx workflow.Context, spec RunnerSpec) error {
	logger := workflow.GetLogger(ctx)

	// Dispatch must not be retried: it creates a new runner each attempt, so a
	// lost response on retry would spawn a duplicate. The registration token is
	// minted inside the activity, so it never enters workflow history.
	dispatchCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout:    2 * time.Minute,
		ScheduleToCloseTimeout: 2 * time.Minute,
		RetryPolicy:            shared.NoRetry(),
	})
	var dispatchedID string
	err := workflow.ExecuteActivity(dispatchCtx, a.DispatchRunner, activities.DispatchSpec{
		Repo:   spec.Repo,
		Labels: spec.Labels,
		Image:  spec.Image,
	}).Get(dispatchCtx, &dispatchedID)
	if err != nil {
		return fmt.Errorf("dispatch runner for %s: %w", spec.Repo, err)
	}

	reapAfter := spec.ReapAfter
	if reapAfter <= 0 {
		reapAfter = defaultReapAfter
	}
	// Wait until the runner's alloc goes terminal so we reap promptly, with
	// reapAfter as the backstop ceiling (StartToCloseTimeout): a wedged runner
	// times the wait out and we reap anyway. NoRetry -- a timeout means "reap
	// now", not "wait another ceiling". A cancellation (operator terminate) also
	// falls through to the reap below so the dispatched runner is never orphaned.
	waitCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: reapAfter,
		HeartbeatTimeout:    time.Minute,
		RetryPolicy:         shared.NoRetry(),
	})
	if err := workflow.ExecuteActivity(waitCtx, a.WaitRunnerDone, dispatchedID).Get(waitCtx, nil); err != nil {
		logger.Info("Runner wait ended early (backstop deadline or cancellation); reaping now", "job", dispatchedID, "error", err)
	}

	// Reap on a disconnected context so a closing/cancelled workflow can still
	// stop the Nomad job. The reaper treats an already-gone job as success.
	reapCtx, cancel := workflow.NewDisconnectedContext(ctx)
	defer cancel()
	reapCtx = workflow.WithActivityOptions(reapCtx, shared.QuickActivityOptions())
	if err := workflow.ExecuteActivity(reapCtx, a.ReapRunner, dispatchedID).Get(reapCtx, nil); err != nil {
		return fmt.Errorf("reap runner %s: %w", dispatchedID, err)
	}
	return nil
}
