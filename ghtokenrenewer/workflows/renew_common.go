// -------------------------------------------------------------------------------
// GitHub Token Renewer Workflow - Shared Per-Repo Renewal Fan-Out
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// renewAll is the concurrency + partition core shared by RenewTokens (GitHub CI
// tokens) and RenewSonarCloudTokens (SonarCloud analysis tokens): the two
// workflows differ only in which per-repo activity they run and their result
// type, so the fan-out lives here once, generic over that result type.
// -------------------------------------------------------------------------------

package workflows

import (
	"errors"
	"fmt"

	"go.temporal.io/sdk/workflow"

	"munchbox/temporal-workers/shared"
)

// renewAll runs renewActivity once per repo with bounded concurrency, then
// partitions the outcomes after the barrier: repos whose activity errored go to
// failed (in deterministic repo order), the rest yield their result. The
// returned error is the join of every per-repo error, so it is non-nil iff at
// least one repo failed. renewActivity is the per-repo activity method (e.g.
// a.RenewRepoToken) and warnMsg is logged on each per-repo failure.
func renewAll[T any](ctx workflow.Context, concurrency int, renewActivity any, warnMsg string, repos []string) (renewed []T, failed []string, err error) {
	logger := workflow.GetLogger(ctx)

	results := make([]T, len(repos))
	errs := make([]error, len(repos))

	sem := workflow.NewBufferedChannel(ctx, concurrency)
	wg := workflow.NewWaitGroup(ctx)
	for i, repo := range repos {
		wg.Add(1)
		workflow.Go(ctx, func(gctx workflow.Context) {
			defer wg.Done()
			sem.Send(gctx, nil) // acquire a slot
			defer sem.Receive(gctx, nil)

			rctx := workflow.WithActivityOptions(gctx, shared.QuickActivityOptions())
			if aerr := workflow.ExecuteActivity(rctx, renewActivity, repo).Get(rctx, &results[i]); aerr != nil {
				logger.Warn(warnMsg, "repo", repo, "error", aerr)
				errs[i] = fmt.Errorf("%s: %w", repo, aerr)
			}
		})
	}
	wg.Wait(ctx)

	// Partition after the barrier -- deterministic, no concurrent appends.
	for i, repo := range repos {
		if errs[i] != nil {
			failed = append(failed, repo)
		} else {
			renewed = append(renewed, results[i])
		}
	}
	return renewed, failed, errors.Join(errs...)
}
