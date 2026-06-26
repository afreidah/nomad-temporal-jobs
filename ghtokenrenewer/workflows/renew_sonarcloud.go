// -------------------------------------------------------------------------------
// GitHub Token Renewer Workflow - Refresh Every Repo's SonarCloud Token Secret
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Lists the managed repos and renews each one's SonarCloud analysis token secret
// with bounded concurrency, minting a fresh per-project token from the master
// token. A per-repo failure is recorded and the run continues; the workflow
// returns an error if any repo failed. Pure orchestration -- all I/O happens in
// activities. Sibling of RenewTokens (GitHub CI tokens) on the same worker, but
// scheduled far less often because SonarCloud tokens are long-lived.
// -------------------------------------------------------------------------------

package workflows

import (
	"errors"
	"fmt"

	"go.temporal.io/sdk/workflow"

	"munchbox/temporal-workers/ghtokenrenewer/activities"
	"munchbox/temporal-workers/shared"
)

// SonarRenewConfig is the workflow input.
type SonarRenewConfig struct {
	// Concurrency bounds how many repos are renewed in parallel so a large fleet
	// doesn't burst the SonarCloud API. Default 4.
	Concurrency int `json:"concurrency"`
}

func (c *SonarRenewConfig) applyDefaults() {
	if c.Concurrency <= 0 {
		c.Concurrency = 4
	}
}

// SonarRenewResult summarizes a SonarCloud renewal run.
type SonarRenewResult struct {
	Renewed []activities.SonarRenewResult `json:"renewed"`
	Failed  []string                      `json:"failed,omitempty"`
	Success bool                          `json:"success"`
}

// RenewSonarCloudTokens refreshes the SonarCloud analysis secret on every repo in
// the Consul list, minting a fresh per-project token with bounded concurrency.
func RenewSonarCloudTokens(ctx workflow.Context, config SonarRenewConfig) (*SonarRenewResult, error) {
	logger := workflow.GetLogger(ctx)
	config.applyDefaults()

	quickCtx := workflow.WithActivityOptions(ctx, shared.QuickActivityOptions())

	var repos []string
	if err := workflow.ExecuteActivity(quickCtx, a.ListRepos).Get(quickCtx, &repos); err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	logger.Info("Renewing SonarCloud tokens", "count", len(repos), "concurrency", config.Concurrency)

	renewed := make([]activities.SonarRenewResult, len(repos))
	errs := make([]error, len(repos))

	sem := workflow.NewBufferedChannel(ctx, config.Concurrency)
	wg := workflow.NewWaitGroup(ctx)
	for i, repo := range repos {
		wg.Add(1)
		workflow.Go(ctx, func(gctx workflow.Context) {
			defer wg.Done()
			sem.Send(gctx, nil) // acquire a slot
			defer sem.Receive(gctx, nil)

			rctx := workflow.WithActivityOptions(gctx, shared.QuickActivityOptions())
			if err := workflow.ExecuteActivity(rctx, a.RenewSonarCloudToken, repo).Get(rctx, &renewed[i]); err != nil {
				logger.Warn("Repo SonarCloud token renewal failed", "repo", repo, "error", err)
				errs[i] = fmt.Errorf("%s: %w", repo, err)
			}
		})
	}
	wg.Wait(ctx)

	// Partition results after the barrier -- deterministic, no concurrent appends.
	result := &SonarRenewResult{}
	for i, repo := range repos {
		if errs[i] != nil {
			result.Failed = append(result.Failed, repo)
		} else {
			result.Renewed = append(result.Renewed, renewed[i])
		}
	}

	if err := errors.Join(errs...); err != nil {
		result.Success = false
		return result, fmt.Errorf("one or more repos failed: %w", err)
	}
	result.Success = true
	logger.Info("SonarCloud token renewal complete", "renewed", len(result.Renewed))
	return result, nil
}
