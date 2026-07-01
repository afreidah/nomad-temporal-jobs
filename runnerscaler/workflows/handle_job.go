// -------------------------------------------------------------------------------
// Runner Scaler Child Workflow - Dispatch and Reap One Ephemeral Runner
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// HandleRunner dispatches one ephemeral ci-runner for a (repo, labels) bucket,
// then arms a backstop timer that reaps the runner if it is still around after
// the deadline. The runner is ephemeral, so on the happy path it takes a queued
// job and self-deregisters long before the timer fires, and the reap simply
// finds the Nomad job already gone. The dispatch runs under NoRetry: it creates
// a new runner each call, so a retried dispatch would double up. The runner is
// NOT bound to a specific job_id -- it takes whichever matching job is queued --
// so the parent poller decides how many of these to start by reconciling queued
// depth against active runners, not by keying one child per job.
// -------------------------------------------------------------------------------

package workflows

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/workflow"

	"munchbox/temporal-workers/runnerscaler/activities"
	"munchbox/temporal-workers/shared"
)

// defaultReapAfter is the backstop runner lifetime: a generous upper bound on a
// single CI job, after which a still-present runner is assumed wedged or never
// claimed and is reaped. Sized as a ceiling, not a tight reap -- it must exceed
// the longest legitimate job so a busy runner is never killed mid-build.
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
	// Wait out the backstop. A cancellation here (operator terminate) still falls
	// through to the reap below so the dispatched runner is never orphaned.
	if err := workflow.NewTimer(ctx, reapAfter).Get(ctx, nil); err != nil {
		logger.Info("Reap timer interrupted; reaping now", "job", dispatchedID, "error", err)
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
