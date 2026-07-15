// -------------------------------------------------------------------------------
// GitHub Token Renewer Workflow - Refresh Every Repo's CI Token Secret
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Lists the managed repos and renews each one's Actions token secret with
// bounded concurrency, minting a fresh GitHub App token per repo. A per-repo
// failure is recorded and the run continues; the workflow returns an error if
// any repo failed. Pure orchestration -- all I/O happens in activities.
// -------------------------------------------------------------------------------

package workflows

import (
	"fmt"

	"go.temporal.io/sdk/workflow"

	"munchbox/temporal-workers/ghtokenrenewer/activities"
	"munchbox/temporal-workers/shared"
)

// --- Nil-typed activity stub for compile-time method references ---
var a *activities.Activities

// RenewConfig is the workflow input.
type RenewConfig struct {
	// Concurrency bounds how many repos are renewed in parallel so a large fleet
	// doesn't burst the GitHub API. Default 4.
	Concurrency int `json:"concurrency"`
}

func (c *RenewConfig) applyDefaults() {
	if c.Concurrency <= 0 {
		c.Concurrency = 4
	}
}

// RenewResult summarizes a renewal run.
type RenewResult struct {
	Renewed []activities.RepoRenewResult `json:"renewed"`
	Failed  []string                     `json:"failed,omitempty"`
	Success bool                         `json:"success"`
}

// RenewTokens refreshes the CI token secret on every repo in the Consul list,
// minting a fresh GitHub App token per repo with bounded concurrency.
func RenewTokens(ctx workflow.Context, config RenewConfig) (*RenewResult, error) {
	logger := workflow.GetLogger(ctx)
	config.applyDefaults()

	quickCtx := workflow.WithActivityOptions(ctx, shared.QuickActivityOptions())

	var repos []string
	if err := workflow.ExecuteActivity(quickCtx, a.ListRepos).Get(quickCtx, &repos); err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	logger.Info("Renewing repo tokens", "count", len(repos), "concurrency", config.Concurrency)

	renewed, failed, err := renewAll[activities.RepoRenewResult](
		ctx, config.Concurrency, a.RenewRepoToken, "Repo token renewal failed", repos)
	result := &RenewResult{Renewed: renewed, Failed: failed}
	if err != nil {
		return result, fmt.Errorf("one or more repos failed: %w", err)
	}
	result.Success = true
	logger.Info("Token renewal complete", "renewed", len(result.Renewed))
	return result, nil
}
